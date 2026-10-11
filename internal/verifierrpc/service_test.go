package verifierrpc

import (
	"context"
	"errors"
	"net"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/securegrpc"
	"sqlsentinel/internal/testcert"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMTLSVerifierFenceProgressAndCancellation(t *testing.T) {
	ca, sc, sk, cc, ck, e := testcert.Generate(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("v", 32)
	server, e := securegrpc.Server(securegrpc.Config{CA: ca, Cert: sc, Key: sk, Token: token})
	if e != nil {
		t.Fatal(e)
	}
	defer server.Stop()
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	job := workflow.Job{ID: strings.Repeat("a", 32), Status: workflow.Running, Attempts: 1, LeaseOwner: "claim-1", LeaseUntil: time.Now().Add(time.Minute)}
	var jobMu sync.Mutex
	cancelled := make(chan struct{}, 1)
	RegisterVerifierServer(server, NewServer(func(context.Context, string) (workflow.Job, error) {
		jobMu.Lock()
		defer jobMu.Unlock()
		return job, nil
	}, func(ctx context.Context, j workflow.Job) error {
		Notify(ctx, "shadow_verification")
		if j.CandidateSpecSHA256 == "cancel" {
			<-ctx.Done()
			cancelled <- struct{}{}
			return ctx.Err()
		}
		return nil
	}))
	go server.Serve(listener)
	conn, e := securegrpc.Dial(listener.Addr().String(), securegrpc.Config{CA: ca, Cert: cc, Key: ck, Token: token, ServerName: "localhost"})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phases := []string{}
	run := Client(conn, func(p string) { phases = append(phases, p) })
	if e = run(ctx, job); e != nil || len(phases) < 2 || phases[0] != "accepted" || phases[len(phases)-1] != "completed" {
		t.Fatal(phases, e)
	}
	wrong := job
	wrong.LeaseOwner = "old-owner"
	if e = run(ctx, wrong); !errors.Is(e, workflow.ErrLeaseLost) {
		t.Fatal("old owner executed", e)
	}
	wrong = job
	wrong.Attempts++
	if e = run(ctx, wrong); !errors.Is(e, workflow.ErrLeaseLost) {
		t.Fatal("old attempt executed", e)
	}
	jobMu.Lock()
	job.CandidateSpecSHA256 = "cancel"
	jobMu.Unlock()
	short, stop := context.WithTimeout(ctx, 80*time.Millisecond)
	defer stop()
	if e = run(short, job); e == nil {
		t.Fatal("cancelled run succeeded")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach executor")
	}
}
