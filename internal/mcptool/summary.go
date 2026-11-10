package mcptool

import (
	"encoding/json"
	"regexp"
	"sqlsentinel/internal/candidate"
	"sqlsentinel/internal/plancompare"
)

// Model-facing reports are a projection, not an unbounded copy of SQL, plans,
// schema, historical text or dependency errors. The original HTTP report and
// its digest remain authoritative, and are available through the scoped API.
const maxProjection = 24 << 10

var publicCode = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

func codeValue(v string) string {
	if publicCode.MatchString(v) {
		return v
	}
	return ""
}

type publicObservation struct {
	ID              string `json:"id"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	Code            string `json:"code,omitempty"`
	EvidenceLevel   string `json:"evidence_level,omitempty"`
	Verified        bool   `json:"verified"`
	CandidateSHA256 string `json:"candidate_sha256,omitempty"`
}

type sourceObservation struct {
	publicObservation
	Facts json.RawMessage `json:"facts"`
}

func boundedPlan(plan plancompare.Summary) plancompare.Summary {
	if len(plan.Tables) > 8 {
		plan.Tables = plan.Tables[:8]
	}
	for i := range plan.Tables {
		row := &plan.Tables[i]
		row.TableName = codeValue(row.TableName)
		row.AccessType = codeValue(row.AccessType)
		row.Key = codeValue(row.Key)
		row.PossibleKeys = nil
		if row.RowsExaminedPerScan != nil && *row.RowsExaminedPerScan < 0 {
			row.RowsExaminedPerScan = nil
		}
	}
	return plan
}

func reportProjection(raw []byte) (map[string]any, error) {
	var report struct {
		JobID         string          `json:"job_id"`
		Mode          string          `json:"mode"`
		Version       string          `json:"version"`
		Outcome       string          `json:"outcome"`
		StopReason    string          `json:"stop_reason"`
		CandidateSpec *candidate.Spec `json:"candidate_spec"`
		Agent         struct {
			Status                  string              `json:"status"`
			StopReason              string              `json:"stop_reason"`
			EvidenceLevel           string              `json:"evidence_level"`
			SelectedCandidateID     string              `json:"selected_candidate_id"`
			SelectedCandidateSHA256 string              `json:"selected_candidate_sha256"`
			Observations            []sourceObservation `json:"observations"`
			ModelCalls              int                 `json:"model_calls"`
			Evaluations             int                 `json:"evaluations"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	var spec *candidate.Spec
	if report.CandidateSpec != nil && candidate.Validate(*report.CandidateSpec) == nil {
		spec = report.CandidateSpec
	}
	observations := make([]publicObservation, 0, 16)
	var selectedPlan any
	for _, source := range report.Agent.Observations {
		observation := source.publicObservation
		if len(observations) == 16 {
			break
		}
		observation.ID = codeValue(observation.ID)
		observation.Action = codeValue(observation.Action)
		observation.Status = codeValue(observation.Status)
		observation.Code = codeValue(observation.Code)
		if observation.EvidenceLevel != "L1" {
			observation.EvidenceLevel = ""
		}
		if !digest.MatchString(observation.CandidateSHA256) {
			observation.CandidateSHA256 = ""
		}
		if observation.ID == report.Agent.SelectedCandidateID && observation.Verified && observation.Action == "evaluate_index" && observation.Status == "ok" {
			var facts struct {
				Baseline         plancompare.Summary `json:"baseline"`
				Candidate        plancompare.Summary `json:"candidate"`
				Measured         bool                `json:"measured"`
				CleanupConfirmed bool                `json:"cleanup_confirmed"`
			}
			if json.Unmarshal(source.Facts, &facts) == nil && !facts.Measured && facts.CleanupConfirmed {
				selectedPlan = map[string]any{"baseline": boundedPlan(facts.Baseline), "candidate": boundedPlan(facts.Candidate), "measured": false, "cleanup_confirmed": true}
			}
		}
		observations = append(observations, observation)
	}
	selectedHash := report.Agent.SelectedCandidateSHA256
	if !digest.MatchString(selectedHash) {
		selectedHash = ""
	}
	level := ""
	if report.Agent.EvidenceLevel == "L1" {
		level = "L1"
	}
	value := map[string]any{
		"job_id": codeValue(report.JobID), "mode": codeValue(report.Mode), "version": codeValue(report.Version),
		"outcome": codeValue(report.Outcome), "stop_reason": codeValue(report.StopReason), "candidate_spec": spec,
		"agent": map[string]any{
			"status": codeValue(report.Agent.Status), "stop_reason": codeValue(report.Agent.StopReason),
			"evidence_level": level, "eligible_for_performance_claim": false,
			"selected_candidate_id": codeValue(report.Agent.SelectedCandidateID), "selected_candidate_sha256": selectedHash,
			"observations": observations,
		},
	}
	if selectedPlan != nil {
		value["selected_plan"] = selectedPlan
	}
	// Calls are not tokens, prices or billing. Unknown counts are not invented.
	var shape map[string]json.RawMessage
	_ = json.Unmarshal(raw, &shape)
	var agentShape map[string]json.RawMessage
	_ = json.Unmarshal(shape["agent"], &agentShape)
	agent := value["agent"].(map[string]any)
	if _, ok := agentShape["model_calls"]; ok && report.Agent.ModelCalls >= 0 && report.Agent.ModelCalls <= 5 {
		agent["model_calls"] = report.Agent.ModelCalls
	}
	if _, ok := agentShape["evaluations"]; ok && report.Agent.Evaluations >= 0 && report.Agent.Evaluations <= 2 {
		agent["evaluations"] = report.Agent.Evaluations
	}
	if encoded, err := json.Marshal(value); err != nil || len(encoded) > maxProjection {
		return nil, errConfiguration
	}
	return value, nil
}

func taskProjection(raw []byte) map[string]any {
	var task struct {
		JobID      string `json:"job_id"`
		DeliveryID string `json:"delivery_id"`
		Status     string `json:"status"`
		Duplicate  bool   `json:"duplicate"`
	}
	_ = json.Unmarshal(raw, &task)
	return map[string]any{"job_id": codeValue(task.JobID), "delivery_id": codeValue(task.DeliveryID), "status": codeValue(task.Status), "duplicate": task.Duplicate}
}

func statusNext(status string) (bool, string) {
	switch status {
	case "COMPLETED", "REJECTED", "DEAD_LETTER", "CANCELLED":
		return true, "get_diagnosis_report"
	case "QUEUED", "RUNNING", "RETRYING":
		return false, "wait_then_get_diagnosis_status"
	default:
		return false, "inspect_authorized_http_status"
	}
}
