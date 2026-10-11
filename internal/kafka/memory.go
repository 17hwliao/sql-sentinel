package kafka

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MemoryStore is a deterministic control-plane store used by tests and local
// demos. Its methods preserve the same uniqueness and CAS rules as MySQLStore.
type MemoryStore struct {
	mu      sync.Mutex
	jobs    map[string]Job
	byDeliv map[string]string
	outbox  map[string]OutboxRecord
	seq     int
}

func cloneJob(job Job) Job {
	job.SQL = append([]byte(nil), job.SQL...)
	job.CandidateSpec = append([]byte(nil), job.CandidateSpec...)
	if job.CompletedAt != nil {
		v := *job.CompletedAt
		job.CompletedAt = &v
	}
	return job
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: map[string]Job{}, byDeliv: map[string]string{}, outbox: map[string]OutboxRecord{}}
}

func (s *MemoryStore) Receive(_ context.Context, in ReceiveInput) (ReceiveResult, error) {
	if in.DeliveryID == "" || in.PRNumber < 1 || len(in.SQL) == 0 || len(in.CandidateSpec) == 0 {
		return ReceiveResult{}, errors.New("invalid receive input")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byDeliv[in.DeliveryID]; ok {
		if !sameDelivery(s.jobs[id], in) {
			return ReceiveResult{}, ErrDeliveryConflict
		}
		return ReceiveResult{Job: cloneJob(s.jobs[id]), Duplicate: true}, nil
	}
	now := time.Now().UTC()
	s.seq++
	jobID := newID("job", now, s.seq)
	eventID := newID("event", now, s.seq)
	job := Job{ID: jobID, DeliveryID: in.DeliveryID, PRNumber: in.PRNumber,
		Options: in.Options.normalized(),
		SQL:     append([]byte(nil), in.SQL...), CandidateSpec: append([]byte(nil), in.CandidateSpec...),
		SQLSHA256: digest(in.SQL), CandidateSpecSHA256: digest(in.CandidateSpec), Status: Received,
		Attempts: 0, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	s.jobs[jobID] = job
	s.byDeliv[in.DeliveryID] = jobID
	payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: jobID, DeliveryID: in.DeliveryID, Attempt: 1}).MarshalSafe()
	s.outbox[eventID] = OutboxRecord{EventID: eventID, JobID: jobID, Topic: "sql-sentinel.jobs", Payload: payload, AvailableAt: now}
	job.Status = Queued
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return ReceiveResult{Job: cloneJob(job)}, nil
}

func (s *MemoryStore) Claim(_ context.Context, jobID, owner string, now time.Time, lease time.Duration) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return Job{}, false, ErrNotFound
	}
	if job.Status == Completed || job.Status == Rejected || job.Status == DeadLetter {
		return cloneJob(job), false, nil
	}
	if job.Status == Running && job.LeaseUntil.After(now) {
		return cloneJob(job), false, nil
	}
	if (job.Status == Queued || job.Status == Retrying) && job.NextAttemptAt.After(now) {
		return cloneJob(job), false, nil
	}
	job.Status, job.LeaseOwner, job.LeaseUntil = Running, owner, now.Add(lease)
	job.Attempts++
	job.UpdatedAt = now
	s.jobs[jobID] = job
	return cloneJob(job), true, nil
}

func (s *MemoryStore) Complete(_ context.Context, jobID, owner string, now time.Time) (bool, error) {
	return s.finish(jobID, owner, now, Completed, "")
}

func (s *MemoryStore) Reject(_ context.Context, jobID, owner string, now time.Time, reason string) (bool, error) {
	return s.finish(jobID, owner, now, Rejected, reason)
}

func (s *MemoryStore) finish(jobID, owner string, now time.Time, status JobStatus, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return false, ErrNotFound
	}
	if job.Status != Running || job.LeaseOwner != owner || !job.LeaseUntil.After(now) {
		return false, nil
	}
	job.Status, job.LastError, job.LeaseOwner, job.LeaseUntil, job.UpdatedAt = status, reason, "", time.Time{}, now
	if status == Completed || status == Rejected {
		job.CompletedAt = &now
	}
	s.jobs[jobID] = job
	return true, nil
}

func (s *MemoryStore) Retry(_ context.Context, jobID, owner string, now, next time.Time, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return false, ErrNotFound
	}
	if job.Status != Running || job.LeaseOwner != owner || !job.LeaseUntil.After(now) {
		return false, nil
	}
	job.Status, job.LastError, job.NextAttemptAt, job.LeaseOwner, job.LeaseUntil, job.UpdatedAt = Retrying, reason, next, "", time.Time{}, now
	s.jobs[jobID] = job
	eventID := newID("retry", now, job.Attempts)
	payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: job.ID, DeliveryID: job.DeliveryID, Attempt: job.Attempts + 1}).MarshalSafe()
	s.outbox[eventID] = OutboxRecord{EventID: eventID, JobID: job.ID, Topic: "sql-sentinel.jobs", Payload: payload, AvailableAt: next}
	return true, nil
}

func (s *MemoryStore) DeadLetter(_ context.Context, jobID, owner string, now time.Time, reason string) (bool, error) {
	return s.finish(jobID, owner, now, DeadLetter, reason)
}

func (s *MemoryStore) Get(_ context.Context, deliveryID string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byDeliv[deliveryID]
	if !ok {
		return Job{}, ErrNotFound
	}
	return cloneJob(s.jobs[id]), nil
}

func (s *MemoryStore) PendingOutbox(_ context.Context, limit int, now time.Time) ([]OutboxRecord, error) {
	if limit < 1 {
		return nil, errors.New("outbox limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]OutboxRecord, 0, limit)
	for _, record := range s.outbox {
		if record.Payload != nil && !record.AvailableAt.After(now) {
			record.Payload = append([]byte(nil), record.Payload...)
			result = append(result, record)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s *MemoryStore) MarkPublished(_ context.Context, eventID string, _ time.Time, publishErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.outbox[eventID]
	if !ok {
		return ErrNotFound
	}
	if publishErr == nil {
		record.Payload = nil
	} else {
		record.Attempts++
		record.AvailableAt = time.Now().UTC().Add(RetryDelay(time.Second, time.Minute, record.Attempts))
	}
	s.outbox[eventID] = record
	return nil
}
