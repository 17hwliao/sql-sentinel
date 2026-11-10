package agentrun

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"sqlsentinel/internal/identity"
)

// Schema is additive. The input/session/config binding and reservation count
// survive process replacement. Pending cancellation is a durable cleanup intent.
const Schema = `CREATE TABLE IF NOT EXISTS sql_sentinel_agent_runs (
 run_id CHAR(32) NOT NULL PRIMARY KEY,
 tenant_id VARCHAR(64) NOT NULL,
 project_id VARCHAR(64) NOT NULL,
 actor VARCHAR(64) NOT NULL,
 request_id VARCHAR(64) NOT NULL,
 input_hash CHAR(64) NOT NULL,
 principal JSON NOT NULL,
 session_id VARCHAR(64) NOT NULL,
 document JSON NOT NULL,
 status VARCHAR(24) NOT NULL,
 phase VARCHAR(32) NOT NULL,
 model_calls INT NOT NULL DEFAULT 0,
 pending_cancel BOOLEAN NOT NULL DEFAULT FALSE,
 lease_owner VARCHAR(64) NOT NULL DEFAULT '',
 attempt INT NOT NULL DEFAULT 0,
 lease_until DATETIME(6) NULL,
 deadline DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY agent_run_request (tenant_id,project_id,actor,request_id),
 KEY agent_run_poll (status,pending_cancel,lease_until,created_at)
) ENGINE=InnoDB;`

func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

const columns = `run_id,principal,session_id,document,status,phase,model_calls,pending_cancel,lease_owner,attempt,lease_until,created_at,updated_at,deadline`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Run, error) {
	var r Run
	var principal, doc []byte
	var lease sql.NullTime
	err := row.Scan(&r.ID, &principal, &r.session, &doc, &r.Status, &r.phase, &r.ModelCalls, &r.pendingCancel, &r.owner, &r.attempt, &lease, &r.CreatedAt, &r.UpdatedAt, &r.Deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	// Preserve authoritative columns when restoring the public document.
	var body Run
	if json.Unmarshal(doc, &body) != nil || json.Unmarshal(principal, &r.principal) != nil {
		return Run{}, ErrUnavailable
	}
	body.ID = r.ID
	body.principal = r.principal
	body.session = r.session
	body.principal.SessionID = r.session
	body.phase = r.phase
	body.owner = r.owner
	body.attempt = r.attempt
	body.lease = lease.Time
	body.Status = r.Status
	body.ModelCalls = r.ModelCalls
	body.pendingCancel = r.pendingCancel
	body.CreatedAt = r.CreatedAt
	body.UpdatedAt = r.UpdatedAt
	body.Deadline = r.Deadline
	if body.Events == nil {
		body.Events = []Event{}
	}
	return body, nil
}
func (s *Service) EnsureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, Schema)
	return err
}

// CheckSchema is read-only so the API can run with least-privilege DML access.
func (s *Service) CheckSchema(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM sql_sentinel_agent_runs LIMIT 0")
	if err != nil {
		return err
	}
	return rows.Close()
}
func (s *Service) load(ctx context.Context, id string) (Run, error) {
	return scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM sql_sentinel_agent_runs WHERE run_id=?", id))
}

func (s *Service) claim(ctx context.Context) (Run, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM sql_sentinel_agent_runs WHERE (status IN ('queued','running','waiting_tool') OR pending_cancel=true) AND (lease_until IS NULL OR lease_until<=UTC_TIMESTAMP(6)) ORDER BY updated_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if err != nil {
		return r, err
	}
	owner, err := newID()
	if err != nil {
		return r, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sql_sentinel_agent_runs SET lease_owner=?,attempt=attempt+1,lease_until=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND) WHERE run_id=?`, owner, r.ID)
	if err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	r.owner = owner
	r.attempt++
	r.lease = time.Now().UTC().Add(30 * time.Second)
	return r, nil
}

// save fences writes and merges a concurrent cancellation. A cancelled model
// response may never resurrect its run or authorize a new tool submission.
func (s *Service) save(ctx context.Context, r *Run, release bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM sql_sentinel_agent_runs WHERE run_id=? FOR UPDATE", r.ID))
	if err != nil {
		return err
	}
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(lease_until>UTC_TIMESTAMP(6),false) FROM sql_sentinel_agent_runs WHERE run_id=?`, r.ID).Scan(&live); err != nil {
		return err
	}
	if current.owner != r.owner || current.attempt != r.attempt || !live {
		return ErrLeaseLost
	}
	r.ModelCalls = current.ModelCalls
	if current.pendingCancel && r.phase != "cancel_settled" {
		r.pendingCancel = true
		r.Status = "cancelled"
		r.StopReason = "cancel_requested"
		r.Answer = "运行已取消；后台正在确认其诊断任务的取消结果。"
	}
	r.UpdatedAt = time.Now().UTC()
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if release {
		_, err = tx.ExecContext(ctx, `UPDATE sql_sentinel_agent_runs SET document=?,status=?,phase=?,pending_cancel=?,lease_owner='',lease_until=NULL,updated_at=UTC_TIMESTAMP(6) WHERE run_id=?`, doc, r.Status, r.phase, r.pendingCancel, r.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE sql_sentinel_agent_runs SET document=?,status=?,phase=?,pending_cancel=?,updated_at=UTC_TIMESTAMP(6) WHERE run_id=?`, doc, r.Status, r.phase, r.pendingCancel, r.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) reserve(ctx context.Context, r *Run) error {
	if err := s.check(ctx, r.principal, r.SQL); err != nil {
		return checkError(err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sql_sentinel_agent_runs SET model_calls=model_calls+1,phase='model_inflight',status='running',updated_at=UTC_TIMESTAMP(6) WHERE run_id=? AND lease_owner=? AND attempt=? AND lease_until>UTC_TIMESTAMP(6) AND pending_cancel=false AND status IN ('queued','running','waiting_tool') AND model_calls<? AND deadline>UTC_TIMESTAMP(6)`, r.ID, r.owner, r.attempt, MaxModelCalls)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		current, e := s.load(ctx, r.ID)
		if e != nil {
			return e
		}
		if current.owner != r.owner || current.attempt != r.attempt || current.pendingCancel {
			return ErrLeaseLost
		}
		return ErrBudget
	}
	r.ModelCalls++
	r.phase = "model_inflight"
	r.Status = "running"
	return nil
}

func owns(p identity.Principal, r Run) bool {
	return p.Subject == r.principal.Subject && p.Tenant == r.principal.Tenant && p.Project == r.principal.Project
}
