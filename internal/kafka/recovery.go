package kafka

import (
	"context"
	"time"
)

func (s *MySQLStore) Renew(ctx context.Context, id, owner string, now time.Time, lease time.Duration) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET lease_until=?,updated_at=? WHERE job_id=? AND status='RUNNING' AND lease_owner=? AND lease_until>?`, now.Add(lease), now, id, owner, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecoverExpired atomically fences the crashed owner and creates a new durable
// event. It does not rely on retention of the original Kafka message.
func (s *MySQLStore) RecoverExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT job_id,delivery_id,attempts FROM sql_sentinel_jobs j WHERE (status='RUNNING' AND lease_until<=?) OR (status IN ('QUEUED','RETRYING') AND next_attempt_at<=? AND updated_at<=? AND NOT EXISTS (SELECT 1 FROM sql_sentinel_outbox o WHERE o.job_id=j.job_id AND o.published_at IS NULL)) ORDER BY updated_at LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, now.Add(-5*time.Minute), limit)
	if err != nil {
		return 0, err
	}
	var events []Event
	for rows.Next() {
		var id, delivery string
		var attempts int
		if err = rows.Scan(&id, &delivery, &attempts); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, Event{SchemaVersion: 1, EventID: newID("recovery", now, attempts), JobID: id, DeliveryID: delivery, Attempt: attempts + 1})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		payload, _ := event.MarshalSafe()
		if _, err = tx.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET status='RETRYING',lease_owner='',lease_until=NULL,next_attempt_at=?,updated_at=?,last_error='delivery_recovered' WHERE job_id=?`, now, now, event.JobID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_outbox (event_id,job_id,topic,payload,available_at,created_at) VALUES (?,?,?,?,?,?)`, event.EventID, event.JobID, "sql-sentinel.jobs", payload, now, now); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_job_history (job_id,status,reason,created_at) VALUES (?,'RETRYING','delivery_recovered',?)`, event.JobID, now); err != nil {
			return 0, err
		}
	}
	return len(events), tx.Commit()
}

func (s *MySQLStore) Quarantine(ctx context.Context, m Message) error {
	_, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO sql_sentinel_poison (topic,partition_id,offset_id,payload_sha256,reason) VALUES (?,?,?,?,'invalid_event')`, m.Topic, m.Partition, m.Offset, digest(m.Value))
	return err
}

func (s *MemoryStore) Renew(_ context.Context, id, owner string, now time.Time, lease time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return false, ErrNotFound
	}
	if job.Status != Running || job.LeaseOwner != owner || !job.LeaseUntil.After(now) {
		return false, nil
	}
	job.LeaseUntil = now.Add(lease)
	s.jobs[id] = job
	return true, nil
}
