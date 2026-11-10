package service

import (
	"bytes"
	"encoding/json"
	"sqlsentinel/internal/agentgraph"
	workflow "sqlsentinel/internal/kafka"
)

func finalizeAgentResult(r agentgraph.LoopResult) agentgraph.LoopResult {
	if r.Status != "recommended" {
		if r.Selected != nil || r.SelectedCandidateID != "" {
			r.Summary = ""
		}
		r.Selected = nil
		r.SelectedSHA256 = ""
		r.SelectedCandidateID = ""
	}
	return r
}

func agentApprovalEligible(job workflow.Job, raw, specRaw []byte) bool {
	if job.Options.AgentMode == "" {
		return true
	}
	var bundle struct {
		Mode     string                `json:"mode"`
		Version  string                `json:"version"`
		Outcome  string                `json:"outcome"`
		SQLSHA   string                `json:"sql_sha256"`
		InputSHA string                `json:"input_candidate_spec_sha256"`
		Agent    agentgraph.LoopResult `json:"agent"`
	}
	if json.Unmarshal(raw, &bundle) != nil || bundle.Mode != "agent" || bundle.Version != agentgraph.AgentVersion || bundle.SQLSHA != job.SQLSHA256 || bundle.InputSHA != job.CandidateSpecSHA256 || bundle.Outcome != "recommended" {
		return false
	}
	a := bundle.Agent
	if a.Version != bundle.Version || a.Status != "recommended" || a.Selected == nil || a.SelectedCandidateID == "" || a.EvidenceLevel != "L1" || a.EligibleForPerformanceClaim {
		return false
	}
	normalized, err := json.Marshal(a.Selected)
	if err != nil || !bytes.Equal(normalized, specRaw) {
		return false
	}
	hash, err := agentgraph.CanonicalCandidateHash(*a.Selected)
	if err != nil || hash != a.SelectedSHA256 {
		return false
	}
	cited := false
	for _, id := range a.EvidenceIDs {
		if id == a.SelectedCandidateID {
			cited = true
		}
	}
	if !cited {
		return false
	}
	for _, o := range a.Observations {
		if o.ID == a.SelectedCandidateID && o.Action == "evaluate_index" && o.Status == "ok" && o.Verified && o.EvidenceLevel == "L1" && o.Candidate != nil && o.CandidateSHA256 == hash && len(o.SnapshotSHA256) == 64 && len(o.SchemaSHA256) == 64 && o.MySQLVersion != "" {
			actual, e := json.Marshal(o.Candidate)
			return e == nil && bytes.Equal(actual, specRaw)
		}
	}
	return false
}
