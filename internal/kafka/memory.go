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
	history map[string][]HistoryEntry
	seq     int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: map[string]Job{}, byDeliv: map[string]string{}, outbox: map[string]OutboxRecord{}, history: map[string][]HistoryEntry{}}
}

func (s *MemoryStore) Receive(_ context.Context, in ReceiveInput) (ReceiveResult, error) {
	if in.DeliveryID == "" || in.PRNumber < 1 || len(in.SQL) == 0 || len(in.CandidateSpec) == 0 {
		return ReceiveResult{}, errors.New("invalid receive input")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byDeliv[in.DeliveryID]; ok {
		return ReceiveResult{Job: s.jobs[id], Duplicate: true}, nil
	}
	now := time.Now().UTC()
	s.seq++
	jobID := newID("job", now, s.seq)
	eventID := newID("event", now, s.seq)
	job := Job{ID: jobID, DeliveryID: in.DeliveryID, PRNumber: in.PRNumber,
		SQL: append([]byte(nil), in.SQL...), CandidateSpec: append([]byte(nil), in.CandidateSpec...),
		SQLSHA256: digest(in.SQL), CandidateSpecSHA256: digest(in.CandidateSpec), Status: Received,
		Attempts: 0, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	s.jobs[jobID] = job
	s.byDeliv[in.DeliveryID] = jobID
	s.record(jobID, Received, now)
	payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: jobID, DeliveryID: in.DeliveryID, Attempt: 1}).MarshalSafe()
	s.outbox[eventID] = OutboxRecord{EventID: eventID, JobID: jobID, Topic: "sql-sentinel.jobs", Payload: payload, AvailableAt: now}
	job.Status = Queued
	job.UpdatedAt = now
	s.jobs[jobID] = job
	s.record(jobID, Queued, now)
	return ReceiveResult{Job: job}, nil
}

func (s *MemoryStore) Claim(_ context.Context, jobID, owner string, now time.Time, lease time.Duration) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return Job{}, false, ErrNotFound
	}
	if job.Status == Completed || job.Status == Rejected || job.Status == DeadLetter {
		return job, false, nil
	}
	if job.Status == Running && job.LeaseUntil.After(now) && job.LeaseOwner != owner {
		return job, false, nil
	}
	job.Status, job.LeaseOwner, job.LeaseUntil = Running, owner, now.Add(lease)
	job.Attempts++
	job.UpdatedAt = now
	s.jobs[jobID] = job
	s.record(jobID, Running, now)
	return job, true, nil
}

func (s *MemoryStore) Renew(_ context.Context, jobID, owner string, now time.Time, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return false, ErrNotFound
	}
	if job.Status != Running || job.LeaseOwner != owner || !job.LeaseUntil.After(now) {
		return false, nil
	}
	job.LeaseUntil, job.UpdatedAt = now.Add(lease), now
	s.jobs[jobID] = job
	return true, nil
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
	s.record(jobID, status, now)
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
	s.record(jobID, Retrying, now)
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
	return s.jobs[id], nil
}

func (s *MemoryStore) ListDeadLetters(_ context.Context, limit int) ([]Job, error) {
	if limit < 1 {
		return nil, errors.New("dead-letter limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Job, 0, limit)
	for _, job := range s.jobs {
		if job.Status == DeadLetter {
			result = append(result, job)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s *MemoryStore) History(_ context.Context, deliveryID string, limit int) ([]HistoryEntry, error) {
	if limit < 1 {
		return nil, errors.New("history limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byDeliv[deliveryID]
	if !ok {
		return nil, ErrNotFound
	}
	entries := s.history[id]
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return append([]HistoryEntry(nil), entries...), nil
}

func (s *MemoryStore) ReplayDeadLetter(_ context.Context, deliveryID string, now time.Time) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byDeliv[deliveryID]
	if !ok {
		return Job{}, false, ErrNotFound
	}
	job := s.jobs[id]
	if job.Status != DeadLetter {
		return job, false, nil
	}
	job.Status, job.Attempts, job.NextAttemptAt, job.LastError = Queued, 0, now, ""
	job.LeaseOwner, job.LeaseUntil, job.CompletedAt, job.UpdatedAt = "", time.Time{}, nil, now
	s.jobs[id] = job
	s.seq++
	eventID := newID("replay", now, s.seq)
	payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: job.ID, DeliveryID: job.DeliveryID, Attempt: 1}).MarshalSafe()
	s.outbox[eventID] = OutboxRecord{EventID: eventID, JobID: job.ID, Topic: "sql-sentinel.jobs", Payload: payload, AvailableAt: now}
	s.record(id, Queued, now)
	return job, true, nil
}

func (s *MemoryStore) record(jobID string, status JobStatus, now time.Time) {
	s.history[jobID] = append(s.history[jobID], HistoryEntry{Status: status, CreatedAt: now})
}

func (s *MemoryStore) ClaimOutbox(_ context.Context, limit int, now time.Time, owner string, lease time.Duration) ([]OutboxRecord, error) {
	if limit < 1 {
		return nil, errors.New("outbox limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]OutboxRecord, 0, limit)
	for _, record := range s.outbox {
		if record.Payload != nil && !record.AvailableAt.After(now) && (record.LeaseUntil.IsZero() || !record.LeaseUntil.After(now)) {
			record.LeaseOwner, record.LeaseUntil = owner, now.Add(lease)
			s.outbox[record.EventID] = record
			result = append(result, record)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (s *MemoryStore) MarkPublished(_ context.Context, eventID, owner string, now time.Time, publishErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.outbox[eventID]
	if !ok {
		return ErrNotFound
	}
	if record.LeaseOwner != owner || !record.LeaseUntil.After(now) {
		return ErrOutboxLeaseLost
	}
	if publishErr == nil {
		record.Payload = nil
	} else {
		record.Attempts++
		record.AvailableAt = time.Now().UTC().Add(RetryDelay(time.Second, time.Minute, record.Attempts))
	}
	record.LeaseOwner, record.LeaseUntil = "", time.Time{}
	s.outbox[eventID] = record
	return nil
}
