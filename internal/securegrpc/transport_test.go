package securegrpc

import (
	"context"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"net"
	"sqlsentinel/internal/testcert"
	"strings"
	"testing"
	"time"
)

func TestMutualTLSAuthenticationAndDeadline(t *testing.T) {
	ca, sc, sk, cc, ck, e := testcert.Generate(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("x", 32)
	server, e := Server(Config{CA: ca, Cert: sc, Key: sk, Token: token})
	if e != nil {
		t.Fatal(e)
	}
	defer server.Stop()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go server.Serve(listener)
	cfg := Config{CA: ca, Cert: cc, Key: ck, Token: token, ServerName: "localhost"}
	conn, e := Dial(listener.Addr().String(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, e = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); e != nil {
		t.Fatal(e)
	}
	if _, e = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{}); status.Code(e) != codes.InvalidArgument {
		t.Fatal("unbounded call accepted", e)
	}
	cfg.Token = strings.Repeat("z", 32)
	wrong, e := Dial(listener.Addr().String(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer wrong.Close()
	if _, e = healthpb.NewHealthClient(wrong).Check(ctx, &healthpb.HealthCheckRequest{}); status.Code(e) != codes.Unauthenticated {
		t.Fatal("wrong token accepted", e)
	}
	cfg.Token = token
	cfg.ServerName = "wrong.invalid"
	wrongName, _ := Dial(listener.Addr().String(), cfg)
	defer wrongName.Close()
	short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	if _, e = healthpb.NewHealthClient(wrongName).Check(short, &healthpb.HealthCheckRequest{}); e == nil {
		t.Fatal("wrong TLS identity accepted")
	}
	cfg.ServerName = "localhost"
	cfg.Cert = sc
	cfg.Key = sk
	wrongPurpose, _ := Dial(listener.Addr().String(), cfg)
	defer wrongPurpose.Close()
	purposeContext, purposeStop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer purposeStop()
	if _, e = healthpb.NewHealthClient(wrongPurpose).Check(purposeContext, &healthpb.HealthCheckRequest{}); e == nil {
		t.Fatal("server certificate used as client")
	}
}
