package agentgraph

import (
	"context"
	"encoding/json"
	"errors"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/shadowdata"
)

const AgentVersion = "agent.v1"

var (
	ErrAgentBudget         = errors.New("agent_budget_exhausted")
	ErrAgentFence          = errors.New("agent_lease_lost")
	ErrAgentAuthorization  = errors.New("agent_authorization_failed")
	ErrAgentInfrastructure = errors.New("agent_infrastructure_failed")
)

// All scope and budget fields originate in the durable task, never an action.
type LoopInput struct {
	JobID          string             `json:"job_id"`
	Attempt        int                `json:"attempt"`
	SQL            string             `json:"sql"`
	SQLSHA256      string             `json:"sql_sha256"`
	Tables         []string           `json:"tables"`
	Schema         []shadowdata.Table `json:"schema"`
	BaselinePlan   json.RawMessage    `json:"baseline_plan"`
	SnapshotSHA256 string             `json:"snapshot_sha256"`
	SchemaSHA256   string             `json:"schema_sha256"`
	MySQLVersion   string             `json:"mysql_version"`
	AllowHistory   bool               `json:"history_available"`
	MaxDecisions   int                `json:"max_decisions"`
	MaxEvaluations int                `json:"max_evaluations"`
}

type Action struct {
	Action              string          `json:"action"`
	Table               string          `json:"table,omitempty"`
	Candidate           *candidate.Spec `json:"candidate,omitempty"`
	EvidenceIDs         []string        `json:"evidence_ids,omitempty"`
	SelectedCandidateID string          `json:"selected_candidate_id,omitempty"`
	Summary             string          `json:"summary,omitempty"`
}

// Facts are bounded, structured, public observations. No raw provider errors,
// prompts, private reasoning or credentials may enter a report or next prompt.
type Observation struct {
	ID              string          `json:"id"`
	Action          string          `json:"action"`
	Status          string          `json:"status"`
	Code            string          `json:"code,omitempty"`
	Facts           json.RawMessage `json:"facts,omitempty"`
	Candidate       *candidate.Spec `json:"candidate,omitempty"`
	CandidateSHA256 string          `json:"candidate_sha256,omitempty"`
	SnapshotSHA256  string          `json:"snapshot_sha256,omitempty"`
	SchemaSHA256    string          `json:"schema_sha256,omitempty"`
	MySQLVersion    string          `json:"mysql_version,omitempty"`
	EvidenceLevel   string          `json:"evidence_level,omitempty"`
	Verified        bool            `json:"verified"`
}

type AgentTools interface {
	Check(context.Context) error
	Reserve(context.Context, string) error // "model" or "evaluation"; durable before dispatch
	Inspect(context.Context, string) (Observation, error)
	Search(context.Context, string) (Observation, error)
	Evaluate(context.Context, candidate.Spec) (Observation, error)
}

type AgentStep struct {
	Sequence      int    `json:"sequence"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	ObservationID string `json:"observation_id,omitempty"`
	Code          string `json:"code,omitempty"`
}

type LoopResult struct {
	Mode                        string          `json:"mode"`
	Version                     string          `json:"version"`
	Status                      string          `json:"status"`
	StopReason                  string          `json:"stop_reason"`
	Summary                     string          `json:"summary,omitempty"`
	Selected                    *candidate.Spec `json:"selected_candidate,omitempty"`
	SelectedCandidateID         string          `json:"selected_candidate_id,omitempty"`
	SelectedSHA256              string          `json:"selected_candidate_sha256,omitempty"`
	EvidenceIDs                 []string        `json:"evidence_ids,omitempty"`
	Observations                []Observation   `json:"observations"`
	Trace                       []AgentStep     `json:"agent_trace"`
	ModelCalls                  int             `json:"model_calls"`
	Evaluations                 int             `json:"evaluations"`
	EvidenceLevel               string          `json:"evidence_level"`
	EligibleForPerformanceClaim bool            `json:"eligible_for_performance_claim"`
}
