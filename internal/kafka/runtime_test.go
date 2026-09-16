package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func receiveTestJob(t *testing.T, store *MemoryStore) ReceiveResult {
	t.Helper()
	result, err := store.Receive(context.Background(), ReceiveInput{DeliveryID: "delivery-1", PRNumber: 42, SQL: []byte("SELECT secret_column FROM orders"), CandidateSpec: []byte(`{"table":"orders","index_name":"idx_cand_x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReceiveIsIdempotentAndOutboxIsSafeReference(t *testing.T) {
	store := NewMemoryStore()
	first := receiveTestJob(t, store)
	second, err := store.Receive(context.Background(), ReceiveInput{DeliveryID: "delivery-1", PRNumber: 42, SQL: []byte("different"), CandidateSpec: []byte("different")})
	if err != nil || !second.Duplicate || second.Job.ID != first.Job.ID {
		t.Fatalf("duplicate receive = %+v, err=%v", second, err)
	}
	rows, err := store.ClaimOutbox(context.Background(), 10, time.Now().Add(time.Second), "test", time.Minute)
	if err != nil || len(rows) != 1 {
		t.Fatalf("outbox = %+v, err=%v", rows, err)
	}
	for _, forbidden := range []string{"secret_column", "idx_cand_x", "candidate_spec", "sql_text"} {
		if strings.Contains(string(rows[0].Payload), forbidden) {
			t.Fatalf("payload leaks %q: %s", forbidden, rows[0].Payload)
		}
	}
	var event Event
	if err := json.Unmarshal(rows[0].Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.JobID != first.Job.ID || event.DeliveryID != first.Job.DeliveryID {
		t.Fatalf("event reference = %+v", event)
	}
}

func TestRelayPublishThenMarkAndWorkerDuplicateClaim(t *testing.T) {
	store := NewMemoryStore()
	received := receiveTestJob(t, store)
	broker := NewMemoryBroker()
	count, err := (Relay{Store: store, Publisher: broker, BatchSize: 10}).RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("relay count=%d err=%v", count, err)
	}
	var runs int
	worker := Worker{Store: store, Config: Config{Owner: "worker-a", Lease: time.Hour}, Runner: func(context.Context, Job) error { runs++; return nil }}
	message, err := broker.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := worker.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runner runs=%d, want 1", runs)
	}
	job, err := store.Get(context.Background(), received.Job.DeliveryID)
	if err != nil || job.Status != Completed {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

func TestLeaseIsCompareAndSwapWithExpiry(t *testing.T) {
	store := NewMemoryStore()
	received := receiveTestJob(t, store)
	now := time.Now().UTC()
	if _, claimed, err := store.Claim(context.Background(), received.Job.ID, "worker-a", now, time.Minute); err != nil || !claimed {
		t.Fatalf("first claim=%v err=%v", claimed, err)
	}
	if _, claimed, err := store.Claim(context.Background(), received.Job.ID, "worker-b", now.Add(30*time.Second), time.Minute); err != nil || claimed {
		t.Fatalf("live lease claim=%v err=%v", claimed, err)
	}
	if _, claimed, err := store.Claim(context.Background(), received.Job.ID, "worker-b", now.Add(2*time.Minute), time.Minute); err != nil || !claimed {
		t.Fatalf("expired lease claim=%v err=%v", claimed, err)
	}
	if updated, err := store.Complete(context.Background(), received.Job.ID, "worker-a", now.Add(2*time.Minute)); err != nil || updated {
		t.Fatalf("stale owner completion=%v err=%v", updated, err)
	}
}

func TestRenewOnlyExtendsLiveOwnerLease(t *testing.T) {
	store := NewMemoryStore()
	received := receiveTestJob(t, store)
	now := time.Now().UTC()
	if _, claimed, err := store.Claim(context.Background(), received.Job.ID, "worker-a", now, time.Minute); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	if renewed, err := store.Renew(context.Background(), received.Job.ID, "worker-b", now.Add(time.Second), time.Minute); err != nil || renewed {
		t.Fatalf("foreign renewal=%t err=%v", renewed, err)
	}
	if renewed, err := store.Renew(context.Background(), received.Job.ID, "worker-a", now.Add(30*time.Second), time.Minute); err != nil || !renewed {
		t.Fatalf("owner renewal=%t err=%v", renewed, err)
	}
	if _, claimed, err := store.Claim(context.Background(), received.Job.ID, "worker-b", now.Add(70*time.Second), time.Minute); err != nil || claimed {
		t.Fatalf("renewed lease was stolen claimed=%t err=%v", claimed, err)
	}
}

func TestWorkerHeartbeatPreventsLeaseTakeoverAndDrainsClaimedJob(t *testing.T) {
	store := NewMemoryStore()
	received := receiveTestJob(t, store)
	started := make(chan struct{})
	worker := Worker{
		Store:  store,
		Config: Config{Owner: "worker-a", Lease: 60 * time.Millisecond, LeaseRenewEvery: 10 * time.Millisecond, RunTimeout: time.Second},
		Runner: func(ctx context.Context, _ Job) error {
			close(started)
			select {
			case <-time.After(120 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	raw, _ := (Event{SchemaVersion: 1, EventID: "heartbeat-event", JobID: received.Job.ID, DeliveryID: received.Job.DeliveryID, Attempt: 1}).MarshalSafe()
	parent, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Handle(parent, Message{Value: raw}) }()
	<-started
	stop() // A shutdown must not cancel a job already under this worker's lease.
	time.Sleep(80 * time.Millisecond)
	if _, claimed, err := store.Claim(context.Background(), received.Job.ID, "worker-b", time.Now().UTC(), time.Minute); err != nil || claimed {
		t.Fatalf("heartbeat lease was stolen claimed=%t err=%v", claimed, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("claimed job did not drain: %v", err)
	}
	job, err := store.Get(context.Background(), received.Job.DeliveryID)
	if err != nil || job.Status != Completed {
		t.Fatalf("drained job=%+v err=%v", job, err)
	}
}

func TestWorkerPersistsExplicitRejection(t *testing.T) {
	store := NewMemoryStore()
	received := receiveTestJob(t, store)
	worker := Worker{Store: store, Config: Config{Owner: "worker-a", Lease: time.Minute}, Runner: func(context.Context, Job) error { return ErrRejected }}
	raw, _ := (Event{SchemaVersion: 1, EventID: "rejected-event", JobID: received.Job.ID, DeliveryID: received.Job.DeliveryID, Attempt: 1}).MarshalSafe()
	if err := worker.Handle(context.Background(), Message{Value: raw}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(context.Background(), received.Job.DeliveryID)
	if err != nil || job.Status != Rejected {
		t.Fatalf("rejected job=%+v err=%v", job, err)
	}
}

func TestWorkerRetryBackoffAndDeadLetter(t *testing.T) {
	store := NewMemoryStore()
	received := receiveTestJob(t, store)
	now := time.Now().UTC()
	attempts := 0
	worker := Worker{Store: store, Config: Config{Owner: "worker-a", MaxAttempts: 2, Lease: time.Hour, RetryBase: time.Second, RetryMax: time.Minute}, Now: func() time.Time { return now }, Runner: func(context.Context, Job) error { attempts++; return errors.New("shadow unavailable") }}
	event := Event{SchemaVersion: 1, EventID: "event", JobID: received.Job.ID, DeliveryID: received.Job.DeliveryID, Attempt: 1}
	raw, _ := event.MarshalSafe()
	if err := worker.Handle(context.Background(), Message{Value: raw}); err != nil {
		t.Fatal(err)
	}
	job, _ := store.Get(context.Background(), received.Job.DeliveryID)
	if job.Status != Retrying || job.NextAttemptAt.Sub(now) != time.Second {
		t.Fatalf("retry job=%+v", job)
	}
	rows, err := store.ClaimOutbox(context.Background(), 10, job.NextAttemptAt, "test", time.Minute)
	if err != nil || len(rows) != 2 {
		t.Fatalf("retry outbox=%+v err=%v", rows, err)
	}
	now = job.NextAttemptAt.Add(time.Second)
	if err := worker.Handle(context.Background(), Message{Value: raw}); err != nil {
		t.Fatal(err)
	}
	job, _ = store.Get(context.Background(), received.Job.DeliveryID)
	if attempts != 2 || job.Status != DeadLetter {
		t.Fatalf("dead-letter job=%+v attempts=%d", job, attempts)
	}
}

func deadLetterTestJob(t *testing.T, store *MemoryStore) ReceiveResult {
	t.Helper()
	received := receiveTestJob(t, store)
	worker := Worker{Store: store, Config: Config{Owner: "worker-a", MaxAttempts: 1, Lease: time.Minute}, Runner: func(context.Context, Job) error { return errors.New("shadow unavailable") }}
	raw, _ := (Event{SchemaVersion: 1, EventID: "dead-event", JobID: received.Job.ID, DeliveryID: received.Job.DeliveryID, Attempt: 1}).MarshalSafe()
	if err := worker.Handle(context.Background(), Message{Value: raw}); err != nil {
		t.Fatal(err)
	}
	return received
}

func TestDeadLetterReplayResetsAttemptEpochAndPreservesHistory(t *testing.T) {
	store := NewMemoryStore()
	received := deadLetterTestJob(t, store)
	deadLetters, err := store.ListDeadLetters(context.Background(), 10)
	if err != nil || len(deadLetters) != 1 || deadLetters[0].ID != received.Job.ID {
		t.Fatalf("dead letters=%+v err=%v", deadLetters, err)
	}
	history, err := store.History(context.Background(), received.Job.DeliveryID, 10)
	if err != nil || len(history) < 4 || history[len(history)-1].Status != DeadLetter {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	replayed, ok, err := store.ReplayDeadLetter(context.Background(), received.Job.DeliveryID, time.Now().UTC())
	if err != nil || !ok || replayed.Status != Queued || replayed.Attempts != 0 {
		t.Fatalf("replay=%+v ok=%t err=%v", replayed, ok, err)
	}
	if _, claimed, err := store.Claim(context.Background(), replayed.ID, "worker-b", time.Now().UTC(), time.Minute); err != nil || !claimed {
		t.Fatalf("replayed claim=%t err=%v", claimed, err)
	}
	if _, ok, err := store.ReplayDeadLetter(context.Background(), received.Job.DeliveryID, time.Now().UTC()); err != nil || ok {
		t.Fatalf("non-dead replay accepted=%t err=%v", ok, err)
	}
}

func TestControlHandlerRequiresTokenAndDoesNotExposePayloads(t *testing.T) {
	store := NewMemoryStore()
	deadLetterTestJob(t, store)
	handler := ControlHandler(store, "local-control-token")
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/dead-letters", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/dead-letters?limit=1", nil)
	request.Header.Set("Authorization", "Bearer local-control-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dead-letter status=%d body=%s", response.Code, response.Body)
	}
	for _, forbidden := range []string{"secret_column", "idx_cand_x", "shadow unavailable", "sql_text"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("dead-letter response leaks %q: %s", forbidden, response.Body)
		}
	}
	replay := httptest.NewRequest(http.MethodPost, "/jobs/delivery-1/replay", nil)
	replay.Header.Set("Authorization", "Bearer local-control-token")
	replayResponse := httptest.NewRecorder()
	JobHandler(store, "local-control-token").ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || !strings.Contains(replayResponse.Body.String(), `"replayed":true`) {
		t.Fatalf("replay status=%d body=%s", replayResponse.Code, replayResponse.Body)
	}
}

func TestStatusHandlerDoesNotExposePayloads(t *testing.T) {
	store := NewMemoryStore()
	receiveTestJob(t, store)
	request := httptest.NewRequest(http.MethodGet, "/jobs/delivery-1", nil)
	response := httptest.NewRecorder()
	StatusHandler(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	body := response.Body.String()
	for _, forbidden := range []string{"secret_column", "idx_cand_x", "sql_text"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("status leaks %q: %s", forbidden, body)
		}
	}
}

func TestDecodeEventRejectsUnknownOrTrailingFields(t *testing.T) {
	valid := `{"schema_version":1,"event_id":"e","job_id":"j","delivery_id":"d","attempt":1}`
	if _, err := DecodeEvent([]byte(valid + `{"x":1}`)); err == nil {
		t.Fatal("trailing event accepted")
	}
	if _, err := DecodeEvent([]byte(`{"schema_version":1,"event_id":"e","job_id":"j","delivery_id":"d","attempt":1,"sql":"leak"}`)); err == nil {
		t.Fatal("unknown sensitive field accepted")
	}
}
