package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sqlsentinel/internal/agentrun"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/identity"
	workflow "sqlsentinel/internal/kafka"
	"sqlsentinel/internal/sqladmit"
)

// submitScopedDiagnosis is the shared business entry for HTTP, MCP (over HTTP)
// and the website runner. No loopback requests or bootstrap impersonation.
func (s *Server) submitScopedDiagnosis(ctx context.Context, p identity.Principal, delivery string, pr int, query string, candidate []byte, mode string) (workflow.ReceiveResult, error) {
	if !deliveryPattern.MatchString(delivery) || pr < 1 || len(query) > 64<<10 || !sqladmit.Admit(query).Accepted {
		return workflow.ReceiveResult{}, agentrun.ErrInvalid
	}
	if !s.tablesAllowed(p, query) || (p.Role != "writer" && p.Role != "admin") {
		return workflow.ReceiveResult{}, agentrun.ErrForbidden
	}
	if len(candidate) == 0 {
		candidate = []byte("{}")
	}
	if mode != "" && (mode != "agent.v1" || string(candidate) != "{}") {
		return workflow.ReceiveResult{}, agentrun.ErrInvalid
	}
	options := workflow.JobOptions{TenantID: p.Tenant, ProjectID: p.Project, Actor: p.Subject}
	if mode != "" {
		tables, _ := sqladmit.Inspect(query)
		options.AgentMode = "agent"
		options.AgentVersion = "agent.v1"
		options.AgentDecisions = 5
		options.AgentEvaluations = 2
		options.AgentTables = strings.Join(tables, ",")
		options.AuthorizationSession = p.SessionID
		options.AuthorizationRole = p.Role
	}
	return s.Store.Receive(ctx, workflow.ReceiveInput{DeliveryID: delivery, PRNumber: pr, SQL: []byte(query), CandidateSpec: candidate, Options: options})
}

// CheckAgentRunPrincipal rechecks the actual scoped session at each execution
// point. Gateway keys and shared identity do not grant SQL business permissions.
func (s *Server) CheckAgentRunPrincipal(ctx context.Context, p identity.Principal, query string) error {
	if s.Auth == nil || p.SessionID == "" || (p.Role != "writer" && p.Role != "admin") || !s.tablesAllowed(p, query) {
		return agentrun.ErrForbidden
	}
	var count int
	err := s.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sql_sentinel_sessions WHERE token_id=? AND subject_id=? AND tenant_id=? AND project_id=? AND role_name=? AND revoked_at IS NULL AND expires_at>UTC_TIMESTAMP(6)`, p.SessionID, p.Subject, p.Tenant, p.Project, p.Role).Scan(&count)
	if err != nil {
		return agentrun.ErrUnavailable
	}
	if count != 1 {
		return agentrun.ErrForbidden
	}
	return nil
}

type RunTasks struct{ Server *Server }

func (t RunTasks) Submit(ctx context.Context, p identity.Principal, delivery, query string) (agentrun.Job, error) {
	if err := t.Server.CheckAgentRunPrincipal(ctx, p, query); err != nil {
		return agentrun.Job{}, err
	}
	result, err := t.Server.submitScopedDiagnosis(ctx, p, delivery, 1, query, []byte("{}"), "agent.v1")
	if errors.Is(err, workflow.ErrDeliveryConflict) {
		return agentrun.Job{}, agentrun.ErrConflict
	}
	if err != nil {
		return agentrun.Job{}, agentrun.ErrOutcomeUnknown
	}
	return t.Get(ctx, p, result.Job.DeliveryID)
}
func (t RunTasks) scoped(ctx context.Context, p identity.Principal, delivery string) (workflow.Job, error) {
	j, err := t.Server.Store.Get(ctx, delivery)
	if errors.Is(err, workflow.ErrNotFound) {
		return j, agentrun.ErrNotFound
	}
	if err != nil {
		return j, agentrun.ErrUnavailable
	}
	if j.Options.TenantID != p.Tenant || j.Options.ProjectID != p.Project || j.Options.Actor != p.Subject {
		return workflow.Job{}, agentrun.ErrNotFound
	}
	if err = t.Server.CheckAgentRunPrincipal(ctx, p, string(j.SQL)); err != nil {
		return workflow.Job{}, err
	}
	return j, nil
}
func (t RunTasks) Get(ctx context.Context, p identity.Principal, delivery string) (agentrun.Job, error) {
	j, err := t.scoped(ctx, p, delivery)
	if err != nil {
		return agentrun.Job{}, err
	}
	out := agentrun.Job{ID: j.ID, DeliveryID: j.DeliveryID, Status: string(j.Status)}
	switch j.Status {
	case workflow.Completed, workflow.Rejected, workflow.DeadLetter, workflow.Cancelled:
	default:
		return out, nil
	}
	var raw []byte
	err = t.Server.Store.DB.QueryRowContext(ctx, `SELECT report FROM sql_sentinel_reports WHERE job_id=?`, j.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, agentrun.ErrUnavailable
	}
	out.Evidence = runEvidence(j, raw)
	return out, nil
}
func (t RunTasks) Cancel(ctx context.Context, p identity.Principal, delivery string) (agentrun.Job, error) {
	j, err := t.scoped(ctx, p, delivery)
	if err != nil {
		return agentrun.Job{}, err
	}
	_, err = t.Server.Store.Cancel(ctx, j, p.Subject)
	if err != nil && !errors.Is(err, workflow.ErrRejected) {
		return agentrun.Job{}, agentrun.ErrUnavailable
	}
	return t.Get(ctx, p, delivery)
}

func runEvidence(j workflow.Job, raw []byte) *agentrun.Evidence {
	// Never forward raw SQL errors, provider text, prompt, full trace or model
	// summaries. Public facts are constructed from verified report fields.
	if len(raw) > 2<<20 {
		return nil
	}
	var r struct {
		Outcome   string          `json:"outcome"`
		Mode      string          `json:"mode"`
		Candidate json.RawMessage `json:"candidate_spec"`
		Agent     struct {
			SelectedSHA string `json:"selected_candidate_sha256"`
		} `json:"agent"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	e := &agentrun.Evidence{Level: "L1", ReportPath: "/jobs/by-id/" + j.ID + "/report", Outcome: "partial", Facts: []string{"Plan-level evidence only; no measured performance claim; approval remains separate."}}
	switch r.Outcome {
	case "recommended", "no_change", "budget_exhausted", "timeout", "cancelled", "model_failed", "protocol_failed", "infrastructure_failed", "authorization_failed", "lease_lost":
		e.Outcome = r.Outcome
	}
	// MySQL JSON storage can reorder keys/whitespace. Approval compares a typed
	// canonical CandidateSpec, never the database's raw JSON presentation.
	var spec candidate.Spec
	validSpec := json.Unmarshal(r.Candidate, &spec) == nil && candidate.Validate(spec) == nil
	normalized, _ := json.Marshal(spec)
	if r.Outcome == "recommended" && validSpec && agentApprovalEligible(j, raw, normalized) && j.Options.AgentMode == "agent" {
		e.Recommended = true
		e.Outcome = "recommended"
		e.Summary = "A candidate has verified L1 plan evidence. Performance benefit has not been measured."
		e.CandidateHash = reportDigest(normalized)
		columns := make([]string, 0, len(spec.Columns))
		for _, c := range spec.Columns {
			columns = append(columns, c.Name)
		}
		e.Facts = append(e.Facts, fmt.Sprintf("Verified candidate: table=%s; columns=%s; validation=L1; production_applied=false.", spec.Table, strings.Join(columns, ",")))
	} else {
		if r.Outcome == "recommended" {
			e.Outcome = "partial"
		}
		e.Summary = "Diagnosis stopped without an approvable candidate. See the original report for retained evidence."
		if r.Outcome == "no_change" {
			e.Summary = "Diagnosis completed without a candidate recommendation."
		}
	}
	return e
}
