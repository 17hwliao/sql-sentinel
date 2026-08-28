package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sqlsentinel/internal/diagnosis"
	"sqlsentinel/internal/plancompare"
	"sqlsentinel/internal/shadowgate"
)

func TestRunStopsAfterRejectedSQLAndPreservesAdmissionEvidence(t *testing.T) {
	out := filepath.Join(t.TempDir(), "rejected")
	runnerCalled := false
	report, err := Run(context.Background(), Input{
		SQL:           []byte("SELECT id FROM orders FOR UPDATE"),
		CandidateSpec: []byte(`{"table":"orders"}`),
		OutputDir:     out,
		RunExplain: func(context.Context, string, []byte) (shadowgate.Report, []byte, error) {
			runnerCalled = true
			return shadowgate.Report{}, nil, errors.New("must not run")
		},
	})
	if err == nil {
		t.Fatal("rejected SQL must return a non-nil error after writing evidence")
	}
	if runnerCalled || report.StoppedStep != StepSQLAdmit || len(report.CompletedSteps) != 1 {
		t.Fatalf("unexpected stop result: %+v", report)
	}
	if len(report.RejectionReasons) != 1 || report.RejectionReasons[0].Code != "locking_read" {
		t.Fatalf("rejection reasons: %+v", report.RejectionReasons)
	}
	admissionRaw := mustRead(t, filepath.Join(out, AdmissionFile))
	var admission struct {
		InputSHA256 string `json:"input_sha256"`
		ReasonCode  string `json:"reason_code"`
	}
	if err := json.Unmarshal(admissionRaw, &admission); err != nil {
		t.Fatal(err)
	}
	if admission.InputSHA256 == "" || admission.ReasonCode != "locking_read" {
		t.Fatalf("admission provenance missing: %+v", admission)
	}
	if report.Artifacts[0].SHA256 != digest(admissionRaw) {
		t.Fatalf("admission artifact hash mismatch: %+v", report.Artifacts[0])
	}
	var persisted Report
	if err := json.Unmarshal(mustRead(t, filepath.Join(out, SummaryFile)), &persisted); err != nil {
		t.Fatalf("read persisted summary: %v", err)
	}
	if len(persisted.CompletedSteps) != 1 || persisted.CompletedSteps[0] != StepSQLAdmit || len(persisted.Artifacts) != 2 {
		t.Fatalf("persisted partial evidence missing: %+v", persisted)
	}
	if persisted.Artifacts[0].SHA256 != digest(admissionRaw) {
		t.Fatalf("persisted admission artifact hash mismatch: %+v", persisted.Artifacts[0])
	}
	assertCanonicalSummaryDigest(t, persisted)
	for _, file := range []string{CandidateFile, ComparisonFile, DiagnosisFile} {
		if _, err := os.Stat(filepath.Join(out, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected later artifact %s: %v", file, err)
		}
	}
}

func TestRunWritesAllStagesWithInputProvenance(t *testing.T) {
	out := filepath.Join(t.TempDir(), "success")
	report, err := Run(context.Background(), Input{
		SQL:           []byte("SELECT id FROM orders"),
		CandidateSpec: []byte(`{"table":"orders","index_name":"idx_cand_x"}`),
		OutputDir:     out,
		RunExplain: func(context.Context, string, []byte) (shadowgate.Report, []byte, error) {
			return shadowgate.Report{
				GeneratedAt: "2026-08-28T00:00:00Z", MySQLVersion: "8.4.11",
				Snapshot:    shadowgate.Snapshot{Digest: "snapshot", Rows: 1},
				SQL:         shadowgate.SQLInput{SQL: "SELECT id FROM orders"},
				Candidate:   shadowgate.CandidateIndex{Table: "orders", Name: "idx_cand_x", DDL: "CREATE INDEX"},
				ExplainJSON: json.RawMessage(`{"query_block":{"table":{"table_name":"orders","access_type":"ALL","rows_examined_per_scan":1}}}`),
			}, []byte(`{"query_block":{"table":{"table_name":"orders","access_type":"ALL","rows_examined_per_scan":2}}}`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.StoppedStep != "" || len(report.CompletedSteps) != 4 || len(report.Artifacts) != 5 {
		t.Fatalf("unexpected success report: %+v", report)
	}
	for _, file := range []string{AdmissionFile, CandidateFile, ComparisonFile, DiagnosisFile, SummaryFile} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Fatalf("missing %s: %v", file, err)
		}
	}
	var comparison plancompare.Report
	if err := json.Unmarshal(mustRead(t, filepath.Join(out, ComparisonFile)), &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.InputCandidateExplainSHA256 == "" || comparison.InputBaselineExplainSHA256 == "" {
		t.Fatalf("comparison provenance missing: %+v", comparison)
	}
	var candidate shadowgate.Report
	if err := json.Unmarshal(mustRead(t, filepath.Join(out, CandidateFile)), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.InputSQLSHA256 == "" || candidate.InputCandidateSpecSHA256 == "" {
		t.Fatalf("candidate provenance missing: %+v", candidate)
	}
	comparisonRaw := mustRead(t, filepath.Join(out, ComparisonFile))
	var diagnosisReport diagnosis.Report
	if err := json.Unmarshal(mustRead(t, filepath.Join(out, DiagnosisFile)), &diagnosisReport); err != nil {
		t.Fatal(err)
	}
	if diagnosisReport.Source.SHA256 != digest(comparisonRaw) {
		t.Fatalf("diagnosis comparison hash=%s want %s", diagnosisReport.Source.SHA256, digest(comparisonRaw))
	}
	assertCanonicalSummaryDigest(t, report)
}

func assertCanonicalSummaryDigest(t *testing.T, report Report) {
	t.Helper()
	if len(report.Artifacts) == 0 {
		t.Fatal("missing artifacts")
	}
	self := report.Artifacts[len(report.Artifacts)-1]
	if self.Step != "pipeline_summary" || self.DigestScope != "pipeline_report_with_own_sha256_blank" || self.SHA256 != digest(canonicalSummary(report)) {
		t.Fatalf("invalid summary self digest: %+v", self)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
