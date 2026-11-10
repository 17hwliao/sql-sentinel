package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/benchmark"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/shadowdata"
	"sqlsentinel/internal/verifierrpc"
	"strings"
	"time"
)

// The same guard runs throughout tool SQL and on a bounded watchdog. Cleanup
// deliberately bypasses it so cancellation cannot leave an index behind.
type agentGuardDB struct {
	shadowdata.DB
	check func(context.Context) error
}

func (d agentGuardDB) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	if err := d.check(ctx); err != nil {
		return nil, err
	}
	return d.DB.ExecContext(ctx, q, a...)
}
func (d agentGuardDB) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	if err := d.check(ctx); err != nil {
		return nil, err
	}
	return d.DB.QueryContext(ctx, q, a...)
}
func (d agentGuardDB) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	if d.check(ctx) != nil {
		bad, cancel := context.WithCancel(ctx)
		cancel()
		return d.DB.QueryRowContext(bad, q, a...)
	}
	return d.DB.QueryRowContext(ctx, q, a...)
}

func agentStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, workflow.ErrLeaseLost):
		return agentgraph.ErrAgentFence
	case errors.Is(err, workflow.ErrAgentAuthorization):
		return agentgraph.ErrAgentAuthorization
	case errors.Is(err, workflow.ErrAgentBudget):
		return agentgraph.ErrAgentBudget
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return err
	default:
		return agentgraph.ErrAgentInfrastructure
	}
}

func (r Runner) runAgent(ctx context.Context, job workflow.Job, tables []string) error {
	if job.Options.AgentVersion != agentgraph.AgentVersion || job.Options.Benchmark || string(job.CandidateSpec) != "{}" || !sameAgentTables(tables, workflow.AgentTables(job.Options)) {
		return workflow.ErrRejected
	}
	store := &workflow.MySQLStore{DB: r.Control}
	check := func(step context.Context) error {
		for _, table := range tables {
			if r.AllowedTables != nil && !r.AllowedTables[table] {
				return agentgraph.ErrAgentAuthorization
			}
		}
		return agentStoreError(store.CheckAgent(step, job))
	}
	run, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				step, stop := context.WithTimeout(run, time.Second)
				err := check(step)
				stop()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-done }()
	r.agentGuard = check
	tools := newAgentTools(r, job, tables, check, func(step context.Context, kind string) error {
		return agentStoreError(store.ReserveAgent(step, job, kind))
	})
	verifierrpc.Notify(run, "agent_initial_evidence")
	input, initialErr := tools.Initial(run)
	result := agentgraph.LoopResult{Mode: "agent", Version: agentgraph.AgentVersion, Status: "infrastructure_failed", StopReason: "initial_evidence_failed", EvidenceLevel: "L1", Trace: []agentgraph.AgentStep{}, Observations: []agentgraph.Observation{}}
	if initialErr == nil {
		input.MaxDecisions = job.Options.AgentDecisions
		input.MaxEvaluations = job.Options.AgentEvaluations
		verifierrpc.Notify(run, "agent_decision_loop")
		result = agentgraph.RunAgent(run, input, r.Model, tools)
	} else if errors.Is(initialErr, context.DeadlineExceeded) {
		result.Status = "timeout"
		result.StopReason = "task_deadline_exceeded"
	} else if errors.Is(initialErr, agentgraph.ErrAgentAuthorization) {
		result.Status = "authorization_failed"
		result.StopReason = "authority_revoked"
	} else if errors.Is(initialErr, agentgraph.ErrAgentBudget) {
		result.Status = "budget_exhausted"
		result.StopReason = "initial_evidence_limit"
	}
	if cause := context.Cause(run); cause != nil && !errors.Is(cause, context.DeadlineExceeded) {
		if errors.Is(cause, agentgraph.ErrAgentFence) {
			return workflow.ErrLeaseLost
		}
		if errors.Is(cause, agentgraph.ErrAgentAuthorization) {
			result.Status = "authorization_failed"
			result.StopReason = "authority_revoked"
			result.Selected = nil
			result.SelectedSHA256 = ""
		}
		if errors.Is(cause, agentgraph.ErrAgentInfrastructure) {
			result.Status = "infrastructure_failed"
			result.StopReason = "control_plane_unavailable"
			result.Selected = nil
			result.SelectedSHA256 = ""
		}
	}
	if result.Selected != nil {
		if err := tools.unchanged(run, true); err != nil {
			result.Selected = nil
			result.SelectedSHA256 = ""
			result.SelectedCandidateID = ""
			result.Status = "infrastructure_failed"
			result.StopReason = "final_evidence_context_changed"
			if errors.Is(err, context.DeadlineExceeded) {
				result.Status = "timeout"
				result.StopReason = "task_deadline_exceeded"
			}
			if errors.Is(err, agentgraph.ErrAgentFence) {
				return workflow.ErrLeaseLost
			}
			if errors.Is(err, agentgraph.ErrAgentAuthorization) {
				result.Status = "authorization_failed"
				result.StopReason = "authority_revoked"
			}
		}
	}
	// A short detached window saves partial results after the attempt deadline,
	// but the database fence still forbids cancelled or replaced owners.
	result = finalizeAgentResult(result)
	save, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	usage, err := store.AgentUsage(save, job.ID)
	if err != nil {
		return err
	}
	bundle := map[string]any{"job_id": job.ID, "sql_sha256": job.SQLSHA256, "input_candidate_spec_sha256": job.CandidateSpecSHA256, "candidate_spec": result.Selected, "mode": "agent", "version": agentgraph.AgentVersion, "outcome": result.Status, "stop_reason": result.StopReason, "agent": result, "agent_trace": result.Trace, "agent_usage": usage, "evidence_level": "L1", "eligible_for_performance_claim": false, "pipeline": map[string]any{"evidence_level": "L1", "eligible_for_performance_claim": false}, "versions": map[string]string{"schema": "backend.v2", "agent": agentgraph.AgentVersion, "model": r.ModelVersion}, "unverified": []string{"measured_performance", "production_workload", "provider_final_cost"}}
	if result.Selected != nil {
		raw, _ := json.Marshal(result.Selected)
		bundle["candidate_spec_sha256"] = benchmark.Digest(raw)
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if len(raw) > 2<<20 {
		return errors.New("report_too_large")
	}
	verifierrpc.Notify(save, "report_persistence")
	tx, err := r.Control.BeginTx(save, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var live string
	if err = tx.QueryRowContext(save, `SELECT job_id FROM sql_sentinel_jobs WHERE job_id=? AND status='RUNNING' AND lease_owner=? AND attempts=? AND lease_until>UTC_TIMESTAMP(6) FOR UPDATE`, job.ID, job.LeaseOwner, job.Attempts).Scan(&live); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return workflow.ErrLeaseLost
		}
		return err
	}
	if live != job.ID {
		return workflow.ErrLeaseLost
	}
	if result.Selected != nil && job.Options.AuthorizationSession != "" {
		var session string
		if err = tx.QueryRowContext(save, `SELECT token_id FROM sql_sentinel_sessions WHERE token_id=? AND subject_id=? AND tenant_id=? AND project_id=? AND role_name=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP(6) FOR SHARE`, job.Options.AuthorizationSession, job.Options.Actor, job.Options.TenantID, job.Options.ProjectID, job.Options.AuthorizationRole).Scan(&session); err != nil {
			return workflow.ErrRejected
		}
	}
	if _, err = tx.ExecContext(save, `INSERT INTO sql_sentinel_reports(job_id,report) VALUES (?,?) ON DUPLICATE KEY UPDATE report=VALUES(report),created_at=UTC_TIMESTAMP(6)`, job.ID, raw); err != nil {
		return err
	}
	// Any replacement of a report invalidates old approvals, even if an operator
	// seeded one early. Agent execution never starts benchmark jobs itself.
	if _, err = tx.ExecContext(save, "DELETE FROM sql_sentinel_approvals WHERE job_id=?", job.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	switch result.Status {
	case "recommended", "no_change", "budget_exhausted", "timeout":
		return nil
	case "lease_lost":
		return workflow.ErrLeaseLost
	case "protocol_failed", "model_failed", "authorization_failed", "cancelled":
		return workflow.ErrRejected
	default:
		return errors.New("agent_infrastructure_failed")
	}
}

func agentModeLabel(options workflow.JobOptions) string {
	if options.AgentMode == "" {
		return "fixed"
	}
	return strings.TrimSpace(options.AgentVersion)
}
