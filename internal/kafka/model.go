// Package kafka contains the durable, at-least-once Kafka workflow boundary.
// Kafka messages carry identifiers only; SQL and CandidateSpec remain in the
// control-plane store and are never copied into an event payload.
package kafka

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type JobStatus string

const (
	Received   JobStatus = "RECEIVED"
	Queued     JobStatus = "QUEUED"
	Running    JobStatus = "RUNNING"
	Completed  JobStatus = "COMPLETED"
	Rejected   JobStatus = "REJECTED"
	Retrying   JobStatus = "RETRYING"
	DeadLetter JobStatus = "DEAD_LETTER"
)

var ErrNotFound = errors.New("kafka workflow job not found")
var ErrLeaseLost = errors.New("kafka workflow lease lost")
var ErrRejected = errors.New("kafka workflow job rejected")

type Job struct {
	ID                  string     `json:"job_id"`
	DeliveryID          string     `json:"delivery_id"`
	PRNumber            int        `json:"pr_number"`
	SQL                 []byte     `json:"-"`
	CandidateSpec       []byte     `json:"-"`
	SQLSHA256           string     `json:"sql_sha256"`
	CandidateSpecSHA256 string     `json:"candidate_spec_sha256"`
	Status              JobStatus  `json:"status"`
	Attempts            int        `json:"attempts"`
	NextAttemptAt       time.Time  `json:"next_attempt_at,omitempty"`
	LeaseOwner          string     `json:"-"`
	LeaseUntil          time.Time  `json:"-"`
	LastError           string     `json:"last_error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
}

type ReceiveInput struct {
	DeliveryID    string
	PRNumber      int
	SQL           []byte
	CandidateSpec []byte
}

type ReceiveResult struct {
	Job       Job
	Duplicate bool
}

// Event is deliberately an opaque reference. Do not add SQL, prompt, secret,
// raw key, or SSE delta fields to this type.
type Event struct {
	SchemaVersion int    `json:"schema_version"`
	EventID       string `json:"event_id"`
	JobID         string `json:"job_id"`
	DeliveryID    string `json:"delivery_id"`
	Attempt       int    `json:"attempt"`
}

func (e Event) Validate() error {
	if e.SchemaVersion != 1 || e.EventID == "" || e.JobID == "" || e.DeliveryID == "" || e.Attempt < 1 {
		return errors.New("invalid workflow event")
	}
	return nil
}

func (e Event) MarshalSafe() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

func DecodeEvent(data []byte) (Event, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var event Event
	if err := dec.Decode(&event); err != nil {
		return Event{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Event{}, errors.New("trailing event data")
	}
	return event, event.Validate()
}

type OutboxRecord struct {
	EventID     string
	JobID       string
	Topic       string
	Payload     []byte
	Attempts    int
	AvailableAt time.Time
}

type Store interface {
	Receive(context.Context, ReceiveInput) (ReceiveResult, error)
	Claim(context.Context, string, string, time.Time, time.Duration) (Job, bool, error)
	Complete(context.Context, string, string, time.Time) (bool, error)
	Reject(context.Context, string, string, time.Time, string) (bool, error)
	Retry(context.Context, string, string, time.Time, time.Time, string) (bool, error)
	DeadLetter(context.Context, string, string, time.Time, string) (bool, error)
	Get(context.Context, string) (Job, error)
	PendingOutbox(context.Context, int, time.Time) ([]OutboxRecord, error)
	MarkPublished(context.Context, string, time.Time, error) error
}

type Publisher interface {
	Publish(context.Context, string, string, []byte) error
}

type Message struct {
	Topic     string
	Key       string
	Value     []byte
	Partition int
	Offset    int64
}

type Consumer interface {
	Receive(context.Context) (Message, error)
	Commit(context.Context, Message) error
}

type Runner func(context.Context, Job) error

type Config struct {
	Topic       string
	MaxAttempts int
	Lease       time.Duration
	RetryBase   time.Duration
	RetryMax    time.Duration
	Owner       string
}

func (c Config) withDefaults() Config {
	if c.Topic == "" {
		c.Topic = "sql-sentinel.jobs"
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 3
	}
	if c.Lease <= 0 {
		c.Lease = 5 * time.Minute
	}
	if c.RetryBase <= 0 {
		c.RetryBase = time.Second
	}
	if c.RetryMax <= 0 {
		c.RetryMax = time.Minute
	}
	if c.Owner == "" {
		c.Owner = "worker"
	}
	return c
}

func newID(prefix string, now time.Time, n int) string {
	s := fmt.Sprintf("%s:%d:%d:%d", prefix, now.UnixNano(), n, atomic.AddUint64(&idSequence, 1))
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}

var idSequence uint64

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func RetryDelay(base, max time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt && d < max; i++ {
		if d > max/2 {
			return max
		}
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}

func safeJob(j Job) map[string]any {
	return map[string]any{
		"job_id": j.ID, "delivery_id": j.DeliveryID, "pr_number": j.PRNumber,
		"sql_sha256": j.SQLSHA256, "candidate_spec_sha256": j.CandidateSpecSHA256,
		"status": j.Status, "attempts": j.Attempts, "next_attempt_at": j.NextAttemptAt,
		"created_at": j.CreatedAt, "updated_at": j.UpdatedAt,
	}
}
