package kafka

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MySQLStore is the durable control plane. It intentionally stores the
// untrusted SQL/CandidateSpec only behind the database boundary; neither is
// present in Event or in the status response.
type MySQLStore struct{ DB *sql.DB }

const Schema = `
CREATE TABLE IF NOT EXISTS sql_sentinel_jobs (
  job_id CHAR(64) NOT NULL PRIMARY KEY,
  delivery_id VARCHAR(64) NOT NULL UNIQUE,
  pr_number BIGINT NOT NULL,
  sql_text MEDIUMBLOB NOT NULL,
  candidate_spec MEDIUMBLOB NOT NULL,
  sql_sha256 CHAR(64) NOT NULL,
  candidate_spec_sha256 CHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  next_attempt_at DATETIME(6) NOT NULL,
  lease_owner VARCHAR(128) NOT NULL DEFAULT '',
  lease_until DATETIME(6) NULL,
  last_error VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  completed_at DATETIME(6) NULL,
  KEY idx_jobs_claim (status, next_attempt_at),
  KEY idx_jobs_lease (status, lease_until)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_job_history (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  job_id CHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL,
  reason VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  KEY idx_history_job (job_id, id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sql_sentinel_outbox (
  event_id CHAR(64) NOT NULL PRIMARY KEY,
  job_id CHAR(64) NOT NULL,
  topic VARCHAR(249) NOT NULL,
  payload JSON NOT NULL,
  attempts INT NOT NULL DEFAULT 0,
  available_at DATETIME(6) NOT NULL,
  lease_owner VARCHAR(128) NULL,
  lease_until DATETIME(6) NULL,
  published_at DATETIME(6) NULL,
  last_error VARCHAR(1024) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  KEY idx_outbox_ready (published_at, available_at, lease_until)
) ENGINE=InnoDB;`

func (s *MySQLStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("mysql store DB is required")
	}
	for _, stmt := range splitSchema(Schema) {
		if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	// Existing control planes may predate relay leasing. Check metadata before
	// applying each DDL statement: this works on MySQL variants that do not
	// support ADD ... IF NOT EXISTS and never rewrites existing job rows.
	upgrades := []struct {
		name string
		kind string
		ddl  string
	}{
		{"lease_owner", "column", `ALTER TABLE sql_sentinel_outbox ADD COLUMN lease_owner VARCHAR(128) NULL AFTER available_at`},
		{"lease_until", "column", `ALTER TABLE sql_sentinel_outbox ADD COLUMN lease_until DATETIME(6) NULL AFTER lease_owner`},
		{"idx_outbox_claim", "index", `ALTER TABLE sql_sentinel_outbox ADD INDEX idx_outbox_claim (published_at, available_at, lease_until, created_at)`},
	}
	for _, upgrade := range upgrades {
		var count int
		query := `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'sql_sentinel_outbox' AND column_name = ?`
		if upgrade.kind == "index" {
			query = `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'sql_sentinel_outbox' AND index_name = ?`
		}
		if err := s.DB.QueryRowContext(ctx, query, upgrade.name).Scan(&count); err != nil {
			return fmt.Errorf("inspect outbox lease %s: %w", upgrade.kind, err)
		}
		if count == 0 {
			if _, err := s.DB.ExecContext(ctx, upgrade.ddl); err != nil {
				return fmt.Errorf("upgrade outbox lease %s: %w", upgrade.name, err)
			}
		}
	}
	return nil
}

func splitSchema(schema string) []string {
	var out []string
	for _, part := range strings.Split(schema, ";") {
		if stmt := strings.TrimSpace(part); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func (s *MySQLStore) Receive(ctx context.Context, in ReceiveInput) (ReceiveResult, error) {
	if in.DeliveryID == "" || in.PRNumber < 1 || len(in.SQL) == 0 || len(in.CandidateSpec) == 0 {
		return ReceiveResult{}, errors.New("invalid receive input")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ReceiveResult{}, err
	}
	now := time.Now().UTC()
	jobID, eventID := newID("job", now, int(now.Nanosecond())), newID("event", now, int(now.Nanosecond())+1)
	_, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_jobs
		(job_id,delivery_id,pr_number,sql_text,candidate_spec,sql_sha256,candidate_spec_sha256,status,attempts,next_attempt_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,'RECEIVED',0,?,?,?)`, jobID, in.DeliveryID, in.PRNumber, in.SQL, in.CandidateSpec, digest(in.SQL), digest(in.CandidateSpec), now, now, now)
	if err != nil {
		_ = tx.Rollback()
		if isDuplicate(err) {
			job, getErr := s.Get(ctx, in.DeliveryID)
			return ReceiveResult{Job: job, Duplicate: getErr == nil}, getErr
		}
		return ReceiveResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_job_history (job_id,status,created_at) VALUES (?,?,?)`, jobID, Received, now); err != nil {
		_ = tx.Rollback()
		return ReceiveResult{}, err
	}
	payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: jobID, DeliveryID: in.DeliveryID, Attempt: 1}).MarshalSafe()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_outbox (event_id,job_id,topic,payload,available_at,created_at) VALUES (?,?,?,?,?,?)`, eventID, jobID, "sql-sentinel.jobs", payload, now, now); err != nil {
		_ = tx.Rollback()
		return ReceiveResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET status='QUEUED',updated_at=? WHERE job_id=?`, now, jobID); err != nil {
		_ = tx.Rollback()
		return ReceiveResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_job_history (job_id,status,created_at) VALUES (?,?,?)`, jobID, Queued, now); err != nil {
		_ = tx.Rollback()
		return ReceiveResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ReceiveResult{}, err
	}
	job, err := s.Get(ctx, in.DeliveryID)
	return ReceiveResult{Job: job}, err
}

func (s *MySQLStore) Claim(ctx context.Context, jobID, owner string, now time.Time, lease time.Duration) (Job, bool, error) {
	until := now.Add(lease)
	res, err := s.DB.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET status='RUNNING',attempts=attempts+1,lease_owner=?,lease_until=?,updated_at=?
		WHERE job_id=? AND ((status IN ('QUEUED','RETRYING') AND next_attempt_at<=?) OR (status='RUNNING' AND lease_until<=?))`, owner, until, now, jobID, now, now)
	if err != nil {
		return Job{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	if n == 1 {
		if err := s.recordHistory(ctx, jobID, Running, ""); err != nil {
			return Job{}, false, err
		}
	}
	job, getErr := s.getByID(ctx, jobID)
	if getErr != nil {
		return Job{}, false, getErr
	}
	return job, n == 1, nil
}

// Renew extends only a live lease owned by this worker. A stale worker cannot
// revive or finish a job that another worker has already taken over.
func (s *MySQLStore) Renew(ctx context.Context, jobID, owner string, now time.Time, lease time.Duration) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET lease_until=?,updated_at=? WHERE job_id=? AND status='RUNNING' AND lease_owner=? AND lease_until>?`, now.Add(lease), now, jobID, owner, now)
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	return count == 1, err
}

func (s *MySQLStore) Complete(ctx context.Context, jobID, owner string, now time.Time) (bool, error) {
	return s.finish(ctx, jobID, owner, now, Completed, "")
}
func (s *MySQLStore) Reject(ctx context.Context, jobID, owner string, now time.Time, reason string) (bool, error) {
	return s.finish(ctx, jobID, owner, now, Rejected, reason)
}
func (s *MySQLStore) DeadLetter(ctx context.Context, jobID, owner string, now time.Time, reason string) (bool, error) {
	return s.finish(ctx, jobID, owner, now, DeadLetter, reason)
}

func (s *MySQLStore) finish(ctx context.Context, jobID, owner string, now time.Time, status JobStatus, reason string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET status=?,last_error=?,lease_owner='',lease_until=NULL,completed_at=?,updated_at=?
		WHERE job_id=? AND status='RUNNING' AND lease_owner=? AND lease_until>?`, status, reason, now, now, jobID, owner, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 1 {
		err = s.recordHistory(ctx, jobID, status, reason)
	}
	return n == 1, err
}

func (s *MySQLStore) Retry(ctx context.Context, jobID, owner string, now, next time.Time, reason string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET status='RETRYING',last_error=?,next_attempt_at=?,lease_owner='',lease_until=NULL,updated_at=?
		WHERE job_id=? AND status='RUNNING' AND lease_owner=? AND lease_until>?`, reason, next, now, jobID, owner, now)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if n == 1 {
		job, getErr := s.getTx(ctx, tx, `job_id=?`, jobID)
		if getErr != nil {
			_ = tx.Rollback()
			return false, getErr
		}
		eventID := newID("retry", now, job.Attempts)
		payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: job.ID, DeliveryID: job.DeliveryID, Attempt: job.Attempts + 1}).MarshalSafe()
		if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_outbox (event_id,job_id,topic,payload,available_at,created_at) VALUES (?,?,?,?,?,?)`, eventID, job.ID, "sql-sentinel.jobs", payload, next, now); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_job_history (job_id,status,reason,created_at) VALUES (?,?,?,?)`, jobID, Retrying, reason, now)
	}
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, err
}

func (s *MySQLStore) recordHistory(ctx context.Context, jobID string, status JobStatus, reason string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sql_sentinel_job_history (job_id,status,reason,created_at) VALUES (?,?,?,?)`, jobID, status, reason, time.Now().UTC())
	return err
}

func (s *MySQLStore) Get(ctx context.Context, deliveryID string) (Job, error) {
	return s.get(ctx, `delivery_id=?`, deliveryID)
}

func (s *MySQLStore) ListDeadLetters(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 {
		return nil, errors.New("dead-letter limit must be positive")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT job_id,delivery_id,pr_number,sql_text,candidate_spec,sql_sha256,candidate_spec_sha256,status,attempts,next_attempt_at,lease_owner,lease_until,last_error,created_at,updated_at,completed_at FROM sql_sentinel_jobs WHERE status='DEAD_LETTER' ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *MySQLStore) History(ctx context.Context, deliveryID string, limit int) ([]HistoryEntry, error) {
	if limit < 1 {
		return nil, errors.New("history limit must be positive")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT h.status,h.created_at FROM sql_sentinel_job_history h JOIN sql_sentinel_jobs j ON j.job_id=h.job_id WHERE j.delivery_id=? ORDER BY h.id DESC LIMIT ?`, deliveryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []HistoryEntry
	for rows.Next() {
		var entry HistoryEntry
		if err := rows.Scan(&entry.Status, &entry.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		if _, err := s.Get(ctx, deliveryID); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *MySQLStore) ReplayDeadLetter(ctx context.Context, deliveryID string, now time.Time) (Job, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	job, err := s.getTx(ctx, tx, `delivery_id=? FOR UPDATE`, deliveryID)
	if err != nil {
		return Job{}, false, err
	}
	if job.Status != DeadLetter {
		return job, false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE sql_sentinel_jobs SET status='QUEUED',attempts=0,next_attempt_at=?,lease_owner='',lease_until=NULL,last_error='',completed_at=NULL,updated_at=? WHERE job_id=? AND status='DEAD_LETTER'`, now, now, job.ID)
	if err != nil {
		return Job{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	if count != 1 {
		return job, false, nil
	}
	eventID := newID("replay", now, job.Attempts)
	payload, _ := (Event{SchemaVersion: 1, EventID: eventID, JobID: job.ID, DeliveryID: job.DeliveryID, Attempt: 1}).MarshalSafe()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sql_sentinel_outbox (event_id,job_id,topic,payload,available_at,created_at) VALUES (?,?,?,?,?,?)`, eventID, job.ID, "sql-sentinel.jobs", payload, now, now); err != nil {
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sql_sentinel_job_history (job_id,status,reason,created_at) VALUES (?,?,?,?)`, job.ID, Queued, "manual replay", now); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	job, err = s.getByID(ctx, job.ID)
	return job, true, err
}

func (s *MySQLStore) getByID(ctx context.Context, jobID string) (Job, error) {
	return s.get(ctx, `job_id=?`, jobID)
}

func (s *MySQLStore) getTx(ctx context.Context, tx *sql.Tx, where string, arg any) (Job, error) {
	var j Job
	var completed, lease sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT job_id,delivery_id,pr_number,sql_text,candidate_spec,sql_sha256,candidate_spec_sha256,status,attempts,next_attempt_at,lease_owner,lease_until,last_error,created_at,updated_at,completed_at FROM sql_sentinel_jobs WHERE `+where, arg).
		Scan(&j.ID, &j.DeliveryID, &j.PRNumber, &j.SQL, &j.CandidateSpec, &j.SQLSHA256, &j.CandidateSpecSHA256, &j.Status, &j.Attempts, &j.NextAttemptAt, &j.LeaseOwner, &lease, &j.LastError, &j.CreatedAt, &j.UpdatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if lease.Valid {
		j.LeaseUntil = lease.Time
	}
	if completed.Valid {
		j.CompletedAt = &completed.Time
	}
	return j, nil
}

func (s *MySQLStore) get(ctx context.Context, where string, arg any) (Job, error) {
	var j Job
	var completed, lease sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT job_id,delivery_id,pr_number,sql_text,candidate_spec,sql_sha256,candidate_spec_sha256,status,attempts,next_attempt_at,lease_owner,lease_until,last_error,created_at,updated_at,completed_at FROM sql_sentinel_jobs WHERE `+where, arg).
		Scan(&j.ID, &j.DeliveryID, &j.PRNumber, &j.SQL, &j.CandidateSpec, &j.SQLSHA256, &j.CandidateSpecSHA256, &j.Status, &j.Attempts, &j.NextAttemptAt, &j.LeaseOwner, &lease, &j.LastError, &j.CreatedAt, &j.UpdatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if lease.Valid {
		j.LeaseUntil = lease.Time
	}
	if completed.Valid {
		j.CompletedAt = &completed.Time
	}
	return j, nil
}

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var completed, lease sql.NullTime
	err := row.Scan(&job.ID, &job.DeliveryID, &job.PRNumber, &job.SQL, &job.CandidateSpec, &job.SQLSHA256, &job.CandidateSpecSHA256, &job.Status, &job.Attempts, &job.NextAttemptAt, &job.LeaseOwner, &lease, &job.LastError, &job.CreatedAt, &job.UpdatedAt, &completed)
	if err != nil {
		return Job{}, err
	}
	if lease.Valid {
		job.LeaseUntil = lease.Time
	}
	if completed.Valid {
		job.CompletedAt = &completed.Time
	}
	return job, nil
}

func (s *MySQLStore) ClaimOutbox(ctx context.Context, limit int, now time.Time, owner string, lease time.Duration) ([]OutboxRecord, error) {
	if limit < 1 {
		return nil, errors.New("outbox limit must be positive")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT event_id,job_id,topic,payload,attempts,available_at FROM sql_sentinel_outbox WHERE published_at IS NULL AND available_at<=? AND (lease_until IS NULL OR lease_until<=?) ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED`, now, now, limit)
	if err != nil {
		return nil, err
	}
	var out []OutboxRecord
	for rows.Next() {
		var r OutboxRecord
		if err := rows.Scan(&r.EventID, &r.JobID, &r.Topic, &r.Payload, &r.Attempts, &r.AvailableAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()
	for _, row := range out {
		if _, err := tx.ExecContext(ctx, `UPDATE sql_sentinel_outbox SET lease_owner=?,lease_until=? WHERE event_id=? AND published_at IS NULL`, owner, now.Add(lease), row.EventID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *MySQLStore) MarkPublished(ctx context.Context, eventID, owner string, now time.Time, publishErr error) error {
	if publishErr == nil {
		res, err := s.DB.ExecContext(ctx, `UPDATE sql_sentinel_outbox SET published_at=?,last_error='',lease_owner=NULL,lease_until=NULL WHERE event_id=? AND published_at IS NULL AND lease_owner=? AND lease_until>?`, now, eventID, owner, now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrOutboxLeaseLost
		}
		return nil
	}
	var attempts int
	if err := s.DB.QueryRowContext(ctx, `SELECT attempts FROM sql_sentinel_outbox WHERE event_id=? AND published_at IS NULL AND lease_owner=? AND lease_until>?`, eventID, owner, now).Scan(&attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOutboxLeaseLost
		}
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE sql_sentinel_outbox SET attempts=attempts+1,last_error=?,available_at=?,lease_owner=NULL,lease_until=NULL WHERE event_id=? AND published_at IS NULL AND lease_owner=?`, publishErr.Error(), now.Add(RetryDelay(time.Second, time.Minute, attempts+1)), eventID, owner)
	return err
}

func isDuplicate(err error) bool { return strings.Contains(strings.ToLower(err.Error()), "duplicate") }
