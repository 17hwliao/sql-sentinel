package kafka

import (
	"context"
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
	rows, err := r.Store.PendingOutbox(ctx, r.BatchSize, r.Now().UTC())
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		publishErr := r.Publisher.Publish(ctx, row.Topic, row.EventID, row.Payload)
		if markErr := r.Store.MarkPublished(ctx, row.EventID, r.Now().UTC(), publishErr); markErr != nil {
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
	// A new fencing identity for each claim, including restarts and retries.
	cfg.Owner = newID("claim", time.Now(), 0)
	if w.Now == nil {
		w.Now = time.Now
	}
	job, claimed, err := w.Store.Claim(ctx, event.JobID, cfg.Owner, w.Now().UTC(), cfg.Lease)
	if err != nil {
		return err
	}
	if !claimed {
		if job.Status == Completed || job.Status == Rejected || job.Status == DeadLetter {
			return nil
		}
		return ErrLeaseBusy
	}
	if job.Attempts > cfg.MaxAttempts {
		updated, err := w.Store.DeadLetter(ctx, job.ID, cfg.Owner, w.Now().UTC(), "attempts_exhausted")
		if err != nil {
			return err
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	if job.DeliveryID != event.DeliveryID || digest(job.SQL) != job.SQLSHA256 || digest(job.CandidateSpec) != job.CandidateSpecSHA256 {
		err = ErrRejected
	} else {
		run, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			if renew, ok := w.Store.(interface {
				Renew(context.Context, string, string, time.Time, time.Duration) (bool, error)
			}); ok {
				ticker := time.NewTicker(cfg.Lease / 3)
				defer ticker.Stop()
				for {
					select {
					case <-run.Done():
						return
					case <-ticker.C:
						step, stop := context.WithTimeout(run, cfg.Lease/3)
						updated, renewErr := renew.Renew(step, job.ID, cfg.Owner, time.Now().UTC(), cfg.Lease)
						stop()
						if renewErr != nil || !updated {
							cancel()
							return
						}
					}
				}
			}
		}()
		err = w.Runner(run, job)
		if run.Err() != nil {
			err = ErrLeaseLost
		}
		cancel()
		<-done
	}
	now := w.Now().UTC()
	if err == nil {
		updated, finishErr := w.Store.Complete(ctx, job.ID, cfg.Owner, now)
		if finishErr != nil {
			return finishErr
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	if errors.Is(err, ErrRejected) {
		updated, finishErr := w.Store.Reject(ctx, job.ID, cfg.Owner, now, truncateError(err))
		if finishErr != nil {
			return finishErr
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	if job.Attempts >= cfg.MaxAttempts {
		updated, finishErr := w.Store.DeadLetter(ctx, job.ID, cfg.Owner, now, truncateError(err))
		if finishErr != nil {
			return finishErr
		}
		if !updated {
			return ErrLeaseLost
		}
		return nil
	}
	next := now.Add(RetryDelay(cfg.RetryBase, cfg.RetryMax, job.Attempts))
	updated, retryErr := w.Store.Retry(ctx, job.ID, cfg.Owner, now, next, truncateError(err))
	if retryErr != nil {
		return retryErr
	}
	if !updated {
		return ErrLeaseLost
	}
	return nil
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
		for {
			err = w.Handle(ctx, message)
			if err == nil {
				break
			}
			if _, decodeErr := DecodeEvent(message.Value); decodeErr != nil || errors.Is(err, ErrNotFound) {
				if poison, ok := w.Store.(interface {
					Quarantine(context.Context, Message) error
				}); ok {
					if poison.Quarantine(ctx, message) == nil {
						break
					}
				}
			}
			select {
			case <-ctx.Done():
				<-w.sem
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		<-w.sem
		if err = w.Source.Commit(ctx, message); err != nil {
			return err
		}
	}
}

func truncateError(err error) string {
	if errors.Is(err, ErrRejected) {
		return "input_rejected"
	}
	if errors.Is(err, ErrLeaseLost) {
		return "lease_lost"
	}
	return "runner_failed"
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
