package kafka

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLiveClaimIsNeverAcknowledgedAndExpires(t *testing.T) {
	s := NewMemoryStore()
	r := receiveTestJob(t, s)
	now := time.Now().UTC()
	if _, ok, err := s.Claim(context.Background(), r.Job.ID, "crashed", now, time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	raw, _ := (Event{SchemaVersion: 1, EventID: "e", JobID: r.Job.ID, DeliveryID: r.Job.DeliveryID, Attempt: 1}).MarshalSafe()
	runs := 0
	w := &Worker{Store: s, Now: func() time.Time { return now.Add(time.Second) }, Runner: func(context.Context, Job) error { runs++; return nil }}
	if err := w.Handle(context.Background(), Message{Value: raw}); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("live lease returned %v", err)
	}
	now = now.Add(2 * time.Minute)
	if err := w.Handle(context.Background(), Message{Value: raw}); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatal(runs)
	}
}
func TestSameOwnerCannotReclaimLiveLease(t *testing.T) {
	s := NewMemoryStore()
	r := receiveTestJob(t, s)
	now := time.Now().UTC()
	s.Claim(context.Background(), r.Job.ID, "same", now, time.Minute)
	if _, ok, err := s.Claim(context.Background(), r.Job.ID, "same", now, time.Minute); err != nil || ok {
		t.Fatal("same owner bypassed lease")
	}
}
