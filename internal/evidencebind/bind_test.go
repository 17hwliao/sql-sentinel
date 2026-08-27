package evidencebind

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"sqlsentinel/internal/diagnosis"
	"sqlsentinel/internal/plancompare"
	"sqlsentinel/internal/report"
	"sqlsentinel/internal/series"
)

func fixture() (diagnosis.Report, []byte, plancompare.Report, []byte, report.Result, series.Admission) {
	comparisonRaw := []byte(`{"generated_at":"t","evidence_level":"L1","eligible_for_performance_claim":false,"mysql_version":"8.4.11","snapshot":{"digest":"snap","rows":1},"sql_input":{"sql":"SELECT id FROM orders","signals":[]},"candidate_index":{"table":"orders","name":"idx_cand_x","ddl":"CREATE INDEX","created":false},"baseline":{"raw":{},"summary":{"tables":[],"uses_filesort":false}},"candidate":{"raw":{},"summary":{"tables":[],"uses_filesort":false}},"difference":{"baseline_uses_filesort":false,"candidate_uses_filesort":false,"filesort_changed":false,"baseline_keys":[],"candidate_keys":[],"candidate_only_keys":[]}}`)
	var comparison plancompare.Report
	if err := json.Unmarshal(comparisonRaw, &comparison); err != nil {
		panic(err)
	}
	sum := sha256.Sum256(comparisonRaw)
	diagnosisRaw := []byte(`diagnosis-source`)
	d := diagnosis.Report{
		EvidenceLevel: "L1",
		Source:        diagnosis.Source{SHA256: hex.EncodeToString(sum[:]), Snapshot: "snap", MySQLVersion: "8.4.11"},
		Hypotheses:    []diagnosis.Hypothesis{{Code: "candidate_index_changes_access_path"}},
	}
	m := report.Result{
		Env:            report.EnvInfo{MySQL: "8.4.11"},
		Snapshot:       report.SnapshotInfo{Digest: "snap"},
		Case:           report.CaseInfo{SQL: "SELECT id FROM orders"},
		CandidateIndex: "idx_cand_x",
	}
	s := series.Admission{MeasurementFile: "measurement.json", EligibleForPerformanceClaim: true, EvidenceLevel: "L2"}
	return d, diagnosisRaw, comparison, comparisonRaw, m, s
}

func TestBindInheritsOnlyAlreadyEligibleExactEvidence(t *testing.T) {
	d, dRaw, c, cRaw, m, s := fixture()
	r, err := Bind(d, dRaw, c, cRaw, m, s, "C:/reports/measurement.json")
	if err != nil {
		t.Fatal(err)
	}
	if !r.BindingComplete || !r.EligibleForPerformanceClaim || r.EvidenceLevel != "L2" || !r.Hypotheses[0].Bound {
		t.Fatalf("unexpected binding: %+v", r)
	}
}

func TestBindRejectsMismatchedMeasurementAndSeries(t *testing.T) {
	d, dRaw, c, cRaw, m, s := fixture()
	m.Case.SQL = "SELECT other"
	m.CandidateIndex = "idx_cand_other"
	s.EligibleForPerformanceClaim = false
	s.EvidenceLevel = "L1"
	r, err := Bind(d, dRaw, c, cRaw, m, s, "other.json")
	if err != nil {
		t.Fatal(err)
	}
	if r.BindingComplete || r.EligibleForPerformanceClaim || r.EvidenceLevel != "L1" || r.Hypotheses[0].Bound {
		t.Fatalf("mismatch was bound: %+v", r)
	}
	want := []string{ReasonMeasurementSQLMismatch, ReasonCandidateIndexMismatch, ReasonSeriesMeasurementMismatch, ReasonSeriesNotEligible, ReasonSeriesEvidenceNotL2}
	if len(r.RejectionReasons) != len(want) {
		t.Fatalf("reasons: %+v", r.RejectionReasons)
	}
	for i, code := range want {
		if r.RejectionReasons[i].Code != code {
			t.Fatalf("reason[%d]=%s want %s", i, r.RejectionReasons[i].Code, code)
		}
	}
}

func TestAnalyzeRejectsUnknownAndTrailingJSON(t *testing.T) {
	if _, err := Analyze([]byte(`{"unknown":true}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), "x.json"); err == nil {
		t.Fatal("unknown diagnosis field accepted")
	}
	if _, err := Analyze([]byte(`{} {}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), "x.json"); err == nil {
		t.Fatal("trailing diagnosis JSON accepted")
	}
}
