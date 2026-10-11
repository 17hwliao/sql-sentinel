package kafka

import (
	"context"
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkflowLeaseAndTerminalInvariants(t *testing.T) {
	testWorkflowStates(t, func() Store { return NewMemoryStore() }, 24)
}

func testWorkflowStates(t *testing.T, factory func() Store, cycles int) {
	t.Helper()
	ctx := context.Background()
	prefix := time.Now().UnixNano()
	for cycle := 0; cycle < cycles; cycle++ {
		s := factory()
		in := ReceiveInput{DeliveryID: fmt.Sprintf("state-%d-%d", prefix, cycle), PRNumber: 1, SQL: []byte("SELECT id FROM orders"), CandidateSpec: []byte(`{"kind":"rewrite"}`)}
		r, err := s.Receive(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		dup, err := s.Receive(ctx, in)
		if err != nil || !dup.Duplicate || dup.Job.ID != r.Job.ID {
			t.Fatal("delivery replay created another job")
		}
		now := time.Now().UTC().Add(time.Second)
		var wins atomic.Int32
		var wg sync.WaitGroup
		winner := make(chan string, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				owner := fmt.Sprintf("owner-%d", i)
				_, ok, e := s.Claim(ctx, r.Job.ID, owner, now, time.Second)
				if e != nil {
					t.Error(e)
				}
				if ok {
					wins.Add(1)
					winner <- owner
				}
			}(i)
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatal("multiple lease winners")
		}
		old := <-winner
		later := now.Add(2 * time.Second)
		job, ok, err := s.Claim(ctx, r.Job.ID, "replacement", later, time.Minute)
		if err != nil || !ok || job.Attempts != 2 {
			t.Fatal("expired lease did not recover")
		}
		if ok, err = s.Complete(ctx, r.Job.ID, old, later); err != nil || ok {
			t.Fatal("stale owner completed recovered job")
		}
		next := later.Add(time.Second)
		if ok, err = s.Retry(ctx, r.Job.ID, "replacement", later, next, "temporary"); err != nil || !ok {
			t.Fatal("retry transition failed")
		}
		if _, ok, err = s.Claim(ctx, r.Job.ID, "early", later, time.Minute); err != nil || ok {
			t.Fatal("retry ran before schedule")
		}
		if _, ok, err = s.Claim(ctx, r.Job.ID, "terminal", next, time.Minute); err != nil || !ok {
			t.Fatal("due retry did not claim")
		}
		switch cycle % 3 {
		case 0:
			ok, err = s.Complete(ctx, r.Job.ID, "terminal", next)
		case 1:
			ok, err = s.Reject(ctx, r.Job.ID, "terminal", next, "invalid")
		case 2:
			ok, err = s.DeadLetter(ctx, r.Job.ID, "terminal", next, "exhausted")
		}
		if err != nil || !ok {
			t.Fatal("terminal transition failed")
		}
		final, _ := s.Get(ctx, in.DeliveryID)
		if _, ok, err = s.Claim(ctx, r.Job.ID, "replay", next.Add(time.Hour), time.Minute); err != nil || ok {
			t.Fatal("terminal job reopened")
		}
		again, _ := s.Get(ctx, in.DeliveryID)
		if final.Status != again.Status || again.Attempts != 3 {
			t.Fatal("terminal replay mutated state")
		}
	}
}

func TestPersistentWorkflowStateInvariants(t *testing.T) {
	dsn := os.Getenv("SQL_SENTINEL_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated sentinel_improvement database required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("database open failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(16)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var database string
	if err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil || database != "sentinel_improvement" {
		t.Fatal("dedicated test database required")
	}
	s := &MySQLStore{DB: db}
	if err = s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	testWorkflowStates(t, func() Store { return s }, 6)
}

func TestMemoryWorkflowDoesNotExposeStoredSlices(t *testing.T) {
	s := NewMemoryStore()
	r := receiveTestJob(t, s)
	original := append([]byte(nil), r.Job.SQL...)
	r.Job.SQL[0] = '!'
	got, _ := s.Get(context.Background(), r.Job.DeliveryID)
	if string(got.SQL) != string(original) {
		t.Fatal("returned SQL mutated stored job")
	}
	rows, _ := s.PendingOutbox(context.Background(), 10, time.Now().Add(time.Second))
	payload := append([]byte(nil), rows[0].Payload...)
	rows[0].Payload[0] = '!'
	again, _ := s.PendingOutbox(context.Background(), 10, time.Now().Add(time.Second))
	if string(again[0].Payload) != string(payload) {
		t.Fatal("returned event mutated stored outbox")
	}
}
