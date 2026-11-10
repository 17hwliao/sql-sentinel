package kafka

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrAgentBudget = errors.New("agent_budget_exhausted")
var ErrAgentAuthorization = errors.New("agent_authority_revoked")

type AgentUsage struct {
	ModelCalls  int `json:"model_calls_reserved"`
	Evaluations int `json:"evaluations_reserved"`
}

// Additive migration: old tasks/options are untouched. Reservations survive
// process crashes and durable retries, including ambiguous provider failures.
const AgentSchema = `CREATE TABLE IF NOT EXISTS sql_sentinel_agent_usage (
 job_id CHAR(64) NOT NULL PRIMARY KEY,
 model_calls INT NOT NULL DEFAULT 0,
 evaluations INT NOT NULL DEFAULT 0
) ENGINE=InnoDB;`

func validAgentOptions(o JobOptions) bool {
	return o.AgentMode == "agent" && o.AgentVersion == "agent.v1" && !o.Benchmark && o.AgentDecisions >= 1 && o.AgentDecisions <= 5 && o.AgentEvaluations >= 1 && o.AgentEvaluations <= 2 && o.AgentTables != "" && len(o.AgentTables) <= 1040 && (o.AuthorizationRole == "writer" || o.AuthorizationRole == "admin")
}

type agentQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkAgent(ctx context.Context, q agentQuerier, job Job, lock bool) error {
	if !validAgentOptions(job.Options) || job.LeaseOwner == "" || job.Attempts < 1 {
		return ErrAgentAuthorization
	}
	query := `SELECT j.status,j.lease_owner,j.attempts,COALESCE(j.lease_until>UTC_TIMESTAMP(6),false),o.options FROM sql_sentinel_jobs j JOIN sql_sentinel_job_options o ON o.job_id=j.job_id WHERE j.job_id=?`
	if lock {
		query += " FOR UPDATE"
	}
	var state JobStatus
	var owner string
	var attempt int
	var live bool
	var raw []byte
	if err := q.QueryRowContext(ctx, query, job.ID).Scan(&state, &owner, &attempt, &live, &raw); err != nil {
		return err
	}
	if state != Running || owner != job.LeaseOwner || attempt != job.Attempts || !live {
		return ErrLeaseLost
	}
	var options JobOptions
	if json.Unmarshal(raw, &options) != nil || options != job.Options {
		return ErrAgentAuthorization
	}
	if options.AuthorizationSession != "" {
		var count int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sql_sentinel_sessions WHERE token_id=? AND subject_id=? AND tenant_id=? AND project_id=? AND role_name=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP(6)`, options.AuthorizationSession, options.Actor, options.TenantID, options.ProjectID, options.AuthorizationRole).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrAgentAuthorization
		}
	} else if options.Actor != "operator" || options.TenantID != "operator" || options.ProjectID != "default" || options.AuthorizationRole != "admin" {
		return ErrAgentAuthorization
	}
	return nil
}

func (s *MySQLStore) CheckAgent(ctx context.Context, job Job) error {
	return checkAgent(ctx, s.DB, job, false)
}

func (s *MySQLStore) ReserveAgent(ctx context.Context, job Job, kind string) error {
	if kind != "model" && kind != "evaluation" {
		return ErrAgentAuthorization
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkAgent(ctx, tx, job, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO sql_sentinel_agent_usage(job_id) VALUES (?)", job.ID); err != nil {
		return err
	}
	column, limit := "model_calls", job.Options.AgentDecisions
	if kind == "evaluation" {
		column, limit = "evaluations", job.Options.AgentEvaluations
	}
	res, err := tx.ExecContext(ctx, "UPDATE sql_sentinel_agent_usage SET "+column+"="+column+"+1 WHERE job_id=? AND "+column+"<?", job.ID, limit)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAgentBudget
	}
	return tx.Commit()
}

func (s *MySQLStore) AgentUsage(ctx context.Context, id string) (AgentUsage, error) {
	var u AgentUsage
	err := s.DB.QueryRowContext(ctx, "SELECT model_calls,evaluations FROM sql_sentinel_agent_usage WHERE job_id=?", id).Scan(&u.ModelCalls, &u.Evaluations)
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	return u, err
}

// Cancellation is an explicit durable business transition. A cancelled MCP
// request or a disconnected HTTP client never invokes this operation implicitly.
func (s *MySQLStore) Cancel(ctx context.Context, job Job, actor string) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state JobStatus
	if err = tx.QueryRowContext(ctx, "SELECT status FROM sql_sentinel_jobs WHERE job_id=? FOR UPDATE", job.ID).Scan(&state); err != nil {
		return false, err
	}
	if state == Cancelled {
		return false, nil
	}
	if state == Completed || state == Rejected || state == DeadLetter {
		return false, ErrRejected
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, "UPDATE sql_sentinel_jobs SET status='CANCELLED',last_error='user_cancelled',lease_owner='',lease_until=NULL,completed_at=?,updated_at=? WHERE job_id=?", now, now, job.ID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO sql_sentinel_job_history(job_id,status,reason,created_at) VALUES (?,'CANCELLED','user_cancelled',?)", job.ID, now); err != nil {
		return false, err
	}
	// A cancelled PR task deliberately emits no comment. Close its publisher
	// pending state in the same transaction rather than leaving it unreachable.
	if _, err = tx.ExecContext(ctx, "UPDATE sql_sentinel_sources SET comment_state='cancelled',comment_published_at=? WHERE job_id=? AND comment_published_at IS NULL", now, job.ID); err != nil {
		return false, err
	}
	// Only safe facts; partial evidence may already exist from a failed attempt.
	value := map[string]any{"job_id": job.ID, "sql_sha256": job.SQLSHA256, "outcome": "cancelled", "stop_reason": "user_cancelled", "evidence_level": "L1", "eligible_for_performance_claim": false, "candidate_spec": nil}
	usage := AgentUsage{}
	if job.Options.AgentMode == "agent" {
		if e := tx.QueryRowContext(ctx, "SELECT model_calls,evaluations FROM sql_sentinel_agent_usage WHERE job_id=?", job.ID).Scan(&usage.ModelCalls, &usage.Evaluations); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return false, e
		}
		value["mode"] = "agent"
		value["version"] = job.Options.AgentVersion
		value["agent_usage"] = usage
		value["agent_trace"] = []any{}
		value["agent"] = map[string]any{"mode": "agent", "version": job.Options.AgentVersion, "status": "cancelled", "stop_reason": "user_cancelled", "observations": []any{}, "agent_trace": []any{}, "evidence_level": "L1", "eligible_for_performance_claim": false}
	}
	raw, _ := json.Marshal(value)
	update := `JSON_REMOVE(JSON_SET(report,'$.outcome','cancelled','$.stop_reason','user_cancelled','$.candidate_spec',NULL,'$.agent.status','cancelled','$.agent.stop_reason','user_cancelled'),'$.agent.selected_candidate','$.agent.selected_candidate_id','$.agent.selected_candidate_sha256','$.agent.summary','$.candidate_spec_sha256')`
	if job.Options.AgentMode == "agent" {
		update = "JSON_SET(" + update + ",'$.agent_usage',JSON_EXTRACT(VALUES(report),'$.agent_usage'))"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sql_sentinel_reports(job_id,report) VALUES (?,?) ON DUPLICATE KEY UPDATE report=`+update, job.ID, raw); err != nil {
		return false, err
	}
	metadata, _ := json.Marshal(map[string]string{"status": "CANCELLED"})
	if _, err = tx.ExecContext(ctx, "INSERT INTO sql_sentinel_audit(tenant_id,project_id,actor,action,resource_id,metadata) VALUES (?,?,?,'job.cancelled',?,?)", job.Options.TenantID, job.Options.ProjectID, actor, job.ID, metadata); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func AgentTables(o JobOptions) []string { return strings.Split(o.AgentTables, ",") }
