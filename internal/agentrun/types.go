// Package agentrun implements the bounded website orchestrator. Database
// diagnosis remains in the existing task service; models never execute SQL.
package agentrun

import (
	"context"
	"errors"
	"time"

	"sqlsentinel/internal/identity"
)

const (
	MaxModelCalls = 4
	MaxJobs       = 1
	RunSeconds    = 180
	MaxEvents     = 24
)

var (
	ErrNotFound       = errors.New("agent_run_not_found")
	ErrConflict       = errors.New("agent_run_conflict")
	ErrForbidden      = errors.New("agent_run_forbidden")
	ErrInvalid        = errors.New("agent_run_input_invalid")
	ErrSQLRequired    = errors.New("sql_required")
	ErrUnavailable    = errors.New("agent_run_unavailable")
	ErrBudget         = errors.New("agent_run_budget_exhausted")
	ErrLeaseLost      = errors.New("agent_run_lease_lost")
	ErrOutcomeUnknown = errors.New("agent_run_outcome_unknown")
)

func ErrorCode(err error) string {
	for _, known := range []error{ErrNotFound, ErrConflict, ErrForbidden, ErrInvalid, ErrSQLRequired, ErrUnavailable, ErrBudget, ErrLeaseLost, ErrOutcomeUnknown} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return ErrUnavailable.Error()
}

// A dependency outage must not be reported as revoked membership. Check
// implementations expose only these typed failures; raw errors stay private.
func checkError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrForbidden) {
		return ErrForbidden
	}
	return ErrUnavailable
}

type Model interface {
	DraftWithSystem(context.Context, string, string) (string, error)
}
type Check func(context.Context, identity.Principal, string) error
type Tasks interface {
	Submit(context.Context, identity.Principal, string, string) (Job, error)
	Get(context.Context, identity.Principal, string) (Job, error)
	Cancel(context.Context, identity.Principal, string) (Job, error)
}
type Config struct {
	Version      string
	DefaultModel string
	ModelAliases []string
	ToolsEnabled bool
}
type Input struct {
	RequestID    string `json:"request_id"`
	SQL          string `json:"sql"`
	Message      string `json:"message,omitempty"`
	ModelAlias   string `json:"model_alias,omitempty"`
	ToolsEnabled *bool  `json:"tools_enabled,omitempty"`
}
type Job struct {
	ID         string    `json:"job_id"`
	DeliveryID string    `json:"delivery_id"`
	Status     string    `json:"status"`
	Evidence   *Evidence `json:"evidence,omitempty"`
}

// Evidence is produced by the trusted task adapter, not by the outer model.
type Evidence struct {
	Outcome       string   `json:"outcome"`
	Level         string   `json:"level"`
	Summary       string   `json:"summary"`
	CandidateHash string   `json:"candidate_hash,omitempty"`
	ReportPath    string   `json:"report_path,omitempty"`
	Recommended   bool     `json:"recommended"`
	Facts         []string `json:"facts,omitempty"`
}
type Event struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}
type Run struct {
	ID            string    `json:"id"`
	RequestID     string    `json:"request_id"`
	SQL           string    `json:"sql"`
	Message       string    `json:"message,omitempty"`
	ModelAlias    string    `json:"model_alias"`
	ToolsEnabled  bool      `json:"tools_enabled"`
	ConfigVersion string    `json:"config_version"`
	Status        string    `json:"status"`
	StopReason    string    `json:"stop_reason,omitempty"`
	Answer        string    `json:"answer,omitempty"`
	ModelSummary  bool      `json:"model_summary"`
	Job           *Job      `json:"job,omitempty"`
	Events        []Event   `json:"events"`
	ModelCalls    int       `json:"model_calls_reserved"`
	CostCNY       *int64    `json:"cost_cny"`
	UsageKnown    bool      `json:"usage_known"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Deadline      time.Time `json:"deadline"`

	principal     identity.Principal
	session       string
	phase         string
	owner         string
	attempt       int
	lease         time.Time
	pendingCancel bool
}
type PublicConfig struct {
	Enabled              bool     `json:"enabled"`
	Version              string   `json:"version"`
	ModelAliases         []string `json:"model_aliases"`
	DefaultModel         string   `json:"default_model"`
	ToolsEnabled         bool     `json:"tools_enabled"`
	MaxModelCalls        int      `json:"max_model_calls"`
	MaxJobs              int      `json:"max_jobs"`
	RunSeconds           int      `json:"run_seconds"`
	InnerModelCalls      int      `json:"inner_model_calls"`
	InnerEvaluations     int      `json:"inner_evaluations"`
	TenantID             string   `json:"tenant_id"`
	ProjectID            string   `json:"project_id"`
	Tables               []string `json:"tables"`
	ModelAvailable       bool     `json:"model_available"`
	TaskServiceAvailable bool     `json:"task_service_available"`
}

func terminal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "outcome_unknown"
}
