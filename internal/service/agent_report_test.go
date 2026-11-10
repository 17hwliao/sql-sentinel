package service

import (
	"sqlsentinel/internal/agentgraph"
	"sqlsentinel/internal/candidate"
	"testing"
)

func TestAgentFinalStopClearsEverySelectionButKeepsObservations(t *testing.T) {
	for _, status := range []string{"authorization_failed", "infrastructure_failed", "timeout", "cancelled", "budget_exhausted", "no_change"} {
		t.Run(status, func(t *testing.T) {
			r := finalizeAgentResult(agentgraph.LoopResult{Status: status, Selected: &candidate.Spec{Table: "orders"}, SelectedSHA256: "old_hash", SelectedCandidateID: "old_observation", Summary: "a prior recommendation", Observations: []agentgraph.Observation{{ID: "old_observation", Verified: true}}})
			if r.Selected != nil || r.SelectedSHA256 != "" || r.SelectedCandidateID != "" || r.Summary != "" || len(r.Observations) != 1 {
				t.Fatal("non-recommended stop retained a final selection or erased historical facts")
			}
		})
	}
	r := finalizeAgentResult(agentgraph.LoopResult{Status: "recommended", Selected: &candidate.Spec{Table: "orders"}, SelectedSHA256: "hash", SelectedCandidateID: "observation"})
	if r.Selected == nil || r.SelectedCandidateID == "" {
		t.Fatal("valid recommendation lost")
	}
}
