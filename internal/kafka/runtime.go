package kafka

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	segmentkafka "github.com/segmentio/kafka-go"
)

// Relay publishes committed outbox rows. A successful publish is marked only
// afterwards, so a crash produces a duplicate publish, never a lost job.
type Relay struct {
	Store     Store
	Publisher Publisher
	BatchSize int
	Owner     string
	Lease     time.Duration
	Now       func() time.Time
}

func (r Relay) RunOnce(ctx context.Context) (int, error) {
	if r.Store == nil || r.Publisher == nil {
		return 0, errors.New("relay store and publisher are required")
	}
	if r.BatchSize < 1 {
		r.BatchSize = 16
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Owner == "" {
		r.Owner = "relay"
	}
	if r.Lease <= 0 {
		r.Lease = 30 * time.Second
	}
	rows, err := r.Store.ClaimOutbox(ctx, r.BatchSize, r.Now().UTC(), r.Owner, r.Lease)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		publishErr := r.Publisher.Publish(ctx, row.Topic, row.EventID, row.Payload)
		if markErr := r.Store.MarkPublished(ctx, row.EventID, r.Owner, r.Now().UTC(), publishErr); markErr != nil {
			return count, markErr
		}
		if publishErr != nil {
			return count, fmt.Errorf("publish event %s: %w", row.EventID, publishErr)
		}
		count++
	}
	return count, nil
}

type Worker struct {
	Store  Store
	Source Consumer
	Runner Runner
	Config Config
	Now    func() time.Time
	sem    chan struct{}
}

func (w *Worker) Handle(ctx context.Context, message Message) error {
	if w.Store == nil || w.Runner == nil {
		return errors.New("worker store and runner are required")
	}
	event, err := DecodeEvent(message.Value)
	if err != nil {
		return fmt.Errorf("decode event: %w", err)
	}
	cfg := w.Config.withDefaults()
	if w.Now == nil {
		w.Now = time.Now
	}
	job, claimed, err := w.Store.Claim(ctx, event.JobID, cfg.Owner, w.Now().UTC(), cfg.Lease)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	err = w.runWithLease(ctx, cfg, job)
	now := w.Now().UTC()
	finalizeCtx, cancelFinalize := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinalize()
	if err == nil {
		updated, finishErr := w.Store.Complete(finalizeCtx, job.ID, cfg.Owner, now)
		if finishErr != nil {
			return finishErr
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	if errors.Is(err, ErrRejected) {
		updated, finishErr := w.Store.Reject(finalizeCtx, job.ID, cfg.Owner, now, truncateError(err))
		if finishErr != nil {
			return finishErr
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	if job.Attempts >= cfg.MaxAttempts {
		updated, finishErr := w.Store.DeadLetter(finalizeCtx, job.ID, cfg.Owner, now, truncateError(err))
		if finishErr != nil {
			return finishErr
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	next := now.Add(RetryDelay(cfg.RetryBase, cfg.RetryMax, job.Attempts))
	updated, retryErr := w.Store.Retry(finalizeCtx, job.ID, cfg.Owner, now, next, truncateError(err))
	if retryErr != nil {
		return retryErr
	}
	if !updated {
		return ErrLeaseLost
	}
	return nil
}

// runWithLease keeps a long-running diagnostic exclusively owned while it is
// healthy. The runner receives a bounded context detached from process
// cancellation so SIGTERM stops new consumption but lets an already claimed
// job finish or reach its timeout; a failed renewal cancels that runner.
func (w *Worker) runWithLease(ctx context.Context, cfg Config, job Job) error {
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.RunTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Runner(runCtx, job) }()
	ticker := time.NewTicker(cfg.LeaseRenewEvery)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			now := w.Now().UTC()
			renewed, err := w.Store.Renew(runCtx, job.ID, cfg.Owner, now, cfg.Lease)
			if err != nil {
				cancel()
				return err
			}
			if !renewed {
				cancel()
				return ErrLeaseLost
			}
		}
	}
}

func (w *Worker) Run(ctx context.Context) error {
	if w.Source == nil {
		return errors.New("worker consumer is required")
	}
	cfg := w.Config.withDefaults()
	w.Config = cfg
	if w.sem == nil {
		w.sem = make(chan struct{}, 1)
	}
	for {
		message, err := w.Source.Receive(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			return err
		}
		select {
		case w.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		err = w.Handle(ctx, message)
		<-w.sem
		if err != nil {
			return err
		}
		if err = w.Source.Commit(ctx, message); err != nil {
			return err
		}
	}
}

func truncateError(err error) string {
	s := err.Error()
	if len(s) > 1024 {
		return s[:1024]
	}
	return s
}

// StatusHandler intentionally serializes safeJob, not Job.
func StatusHandler(store Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"rejection_code":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/jobs/")
		if id == "" || strings.Contains(id, "/") {
			http.Error(w, `{"rejection_code":"invalid_delivery_id"}`, http.StatusBadRequest)
			return
		}
		job, err := store.Get(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, `{"rejection_code":"not_found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"rejection_code":"store_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(safeJob(job))
	})
}

// ControlHandler exposes bounded dead-letter inspection, attempt history, and
// explicit replay. It uses a separate bearer token because replay mutates the
// durable queue, while the ordinary job-status endpoint remains metadata-only.
func ControlHandler(store Store, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, `{"rejection_code":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/dead-letters" {
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			limit, err := boundedLimit(r, 50)
			if err != nil {
				http.Error(w, `{"rejection_code":"invalid_limit"}`, http.StatusBadRequest)
				return
			}
			jobs, err := store.ListDeadLetters(r.Context(), limit)
			if err != nil {
				http.Error(w, `{"rejection_code":"store_unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			result := make([]map[string]any, 0, len(jobs))
			for _, job := range jobs {
				result = append(result, safeJob(job))
			}
			writeSafeJSON(w, map[string]any{"dead_letters": result})
			return
		}

		const prefix = "/jobs/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
		if len(parts) != 2 || parts[0] == "" || (parts[1] != "history" && parts[1] != "replay") {
			http.NotFound(w, r)
			return
		}
		deliveryID := parts[0]
		if parts[1] == "history" {
			if r.Method != http.MethodGet {
				methodNotAllowed(w)
				return
			}
			limit, err := boundedLimit(r, 50)
			if err != nil {
				http.Error(w, `{"rejection_code":"invalid_limit"}`, http.StatusBadRequest)
				return
			}
			history, err := store.History(r.Context(), deliveryID, limit)
			if errors.Is(err, ErrNotFound) {
				http.Error(w, `{"rejection_code":"not_found"}`, http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, `{"rejection_code":"store_unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			writeSafeJSON(w, map[string]any{"delivery_id": deliveryID, "history": history})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		job, replayed, err := store.ReplayDeadLetter(r.Context(), deliveryID, time.Now().UTC())
		if errors.Is(err, ErrNotFound) {
			http.Error(w, `{"rejection_code":"not_found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"rejection_code":"store_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if !replayed {
			http.Error(w, `{"rejection_code":"replay_not_eligible"}`, http.StatusConflict)
			return
		}
		writeSafeJSON(w, map[string]any{"job_id": job.ID, "delivery_id": job.DeliveryID, "status": job.Status, "replayed": true})
	})
}

// JobHandler keeps GET /jobs/{delivery_id} as the safe status route and
// delegates only explicit control subroutes to the bearer-protected handler.
func JobHandler(store Store, token string) http.Handler {
	status := StatusHandler(store)
	control := ControlHandler(store, token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/history") || strings.HasSuffix(r.URL.Path, "/replay") {
			control.ServeHTTP(w, r)
			return
		}
		status.ServeHTTP(w, r)
	})
}

func authorized(r *http.Request, token string) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func boundedLimit(r *http.Request, fallback int) (int, error) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var limit int
		if _, err := fmt.Sscan(raw, &limit); err != nil || limit < 1 || limit > 100 {
			return 0, errors.New("limit must be 1 through 100")
		}
		return limit, nil
	}
	return fallback, nil
}

func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, `{"rejection_code":"method_not_allowed"}`, http.StatusMethodNotAllowed)
}

func writeSafeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// KafkaWriter adapts segmentio/kafka-go while workflow code remains interface-driven.
type KafkaWriter struct{ Writer *segmentkafka.Writer }

func (p KafkaWriter) Publish(ctx context.Context, topic, key string, value []byte) error {
	if p.Writer == nil {
		return errors.New("kafka writer is required")
	}
	return p.Writer.WriteMessages(ctx, segmentkafka.Message{Topic: topic, Key: []byte(key), Value: append([]byte(nil), value...)})
}

type KafkaReader struct{ Reader *segmentkafka.Reader }

func (c KafkaReader) Receive(ctx context.Context) (Message, error) {
	if c.Reader == nil {
		return Message{}, errors.New("kafka reader is required")
	}
	m, err := c.Reader.FetchMessage(ctx)
	if err != nil {
		return Message{}, err
	}
	return Message{Topic: m.Topic, Key: string(m.Key), Value: append([]byte(nil), m.Value...), Partition: m.Partition, Offset: m.Offset}, nil
}

func (c KafkaReader) Commit(ctx context.Context, message Message) error {
	if c.Reader == nil {
		return errors.New("kafka reader is required")
	}
	return c.Reader.CommitMessages(ctx, segmentkafka.Message{Topic: message.Topic, Partition: message.Partition, Offset: message.Offset})
}

// MemoryBroker is an executable at-least-once broker for tests and demos.
type MemoryBroker struct {
	mu       sync.Mutex
	messages []Message
	next     int
}

func NewMemoryBroker() *MemoryBroker { return &MemoryBroker{} }
func (b *MemoryBroker) Publish(_ context.Context, topic, key string, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, Message{Topic: topic, Key: key, Value: append([]byte(nil), value...)})
	return nil
}
func (b *MemoryBroker) Receive(ctx context.Context) (Message, error) {
	for {
		b.mu.Lock()
		if b.next < len(b.messages) {
			m := b.messages[b.next]
			b.mu.Unlock()
			return m, nil
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Message{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func (b *MemoryBroker) Commit(_ context.Context, _ Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.next < len(b.messages) {
		b.next++
	}
	return nil
}
