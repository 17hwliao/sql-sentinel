package evaluation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sqlsentinel/internal/pipeline"
)

func TestBuildMeasuresCuratedCasesAndPreservesHistoricalQualificationBoundary(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "select_star.sql"), "SELECT * FROM orders;")
	write(t, filepath.Join(dir, "locking.sql"), "SELECT id FROM orders FOR UPDATE;")
	manifest := filepath.Join(dir, "manifest.json")
	write(t, manifest, `{
  "dataset_version":"eval-v1",
  "cases":[
    {"id":"star","sql_file":"select_star.sql","expect_signal":"select_star"},
    {"id":"lock","sql_file":"locking.sql","expect_rejection":"locking_read"}
  ]
}`)
	validation := filepath.Join(dir, "validation.json")
	write(t, validation, `{
  "dataset":{"rows":1000000,"dataset_version":"dataset-v1"},
  "case":{"name":"one-case"},
  "calibration":{"candidate":{"noise_ratio":0.1302}},
  "measurement":{},
  "verdict":{"verdict":"Better"}
}`)

	report, err := Build(Input{ManifestPath: manifest, ValidationPath: validation})
	if err != nil {
		t.Fatal(err)
	}
	if report.StaticRules.Detected != 2 || report.StaticRules.Total != 2 || report.StaticRules.Recall != 1 {
		t.Fatalf("static evaluation = %+v", report.StaticRules)
	}
	if report.Validation.Rows != 1000000 || report.Validation.DirectionVerdict != "Better" || report.Validation.CandidateCalibrationNoiseRatio != 0.1302 {
		t.Fatalf("validation summary = %+v", report.Validation)
	}
	if report.Validation.EvidenceLevel != EvidenceLevelL1 || report.Validation.EligibleForPerformanceClaim {
		t.Fatalf("historical direction must not become a claim: %+v", report.Validation)
	}
	if report.Security.Status != StatusNotRun || len(report.Security.Checks) != 5 {
		t.Fatalf("security checklist = %+v", report.Security)
	}
	SetEnvironment(&report, "abc123", "2026-08-28T00:00:00Z", true)
	if report.Environment.GitCommit != "abc123" || !report.Environment.WorkingTreeDirty || report.Environment.SampleDatasetVersion != "eval-v1" || report.Environment.SampleManifestSHA256 == "" {
		t.Fatalf("environment = %+v", report.Environment)
	}
}

func TestAttachPipelineTimingAndSecurityChecklist(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	write(t, filepath.Join(dir, "ok.sql"), "SELECT * FROM orders")
	write(t, manifest, `{"dataset_version":"v1","cases":[{"id":"ok","sql_file":"ok.sql","expect_signal":"select_star"}]}`)
	validation := filepath.Join(dir, "validation.json")
	write(t, validation, `{"dataset":{"rows":1},"measurement":{},"verdict":{"verdict":"Better"}}`)
	result, err := Build(Input{ManifestPath: manifest, ValidationPath: validation})
	if err != nil {
		t.Fatal(err)
	}
	pipelinePath := filepath.Join(dir, "pipeline.json")
	raw, err := json.Marshal(pipeline.Report{
		EvidenceLevel: "L1", EligibleForPerformanceClaim: false,
		StepDurations: []pipeline.StepDuration{{Step: pipeline.StepSQLAdmit, WallClockMS: 1.5}},
		Artifacts:     []pipeline.Artifact{{Step: pipeline.StepSQLAdmit, File: pipeline.AdmissionFile, SHA256: "abc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pipelinePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AttachPipelineTiming(&result, pipelinePath); err != nil {
		t.Fatal(err)
	}
	if result.Cost.PipelineTiming.Status != StatusMeasured || len(result.Cost.PipelineTiming.Steps) != 1 || len(result.Cost.PipelineTiming.Artifacts) != 1 {
		t.Fatalf("pipeline timing = %+v", result.Cost.PipelineTiming)
	}
	security := RunSecurityChecks(func(pkg string) error {
		if pkg == "./internal/webhook/..." {
			return errors.New("simulated failure")
		}
		return nil
	})
	if security.Status != "failed" || security.Checks[3].Status != "failed" || security.Checks[0].Status != "passed" {
		t.Fatalf("security result = %+v", security)
	}
}

func TestBuildRejectsUnsafeManifestAndTrailingValidation(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	write(t, manifest, `{"dataset_version":"v1","cases":[{"id":"bad","sql_file":"../outside.sql","expect_signal":"select_star"}]}`)
	validation := filepath.Join(dir, "validation.json")
	write(t, validation, `{"dataset":{"rows":1},"measurement":{},"verdict":{"verdict":"Better"}}`)
	if _, err := Build(Input{ManifestPath: manifest, ValidationPath: validation}); err == nil {
		t.Fatal("unsafe manifest path accepted")
	}

	write(t, filepath.Join(dir, "ok.sql"), "SELECT * FROM orders")
	write(t, manifest, `{"dataset_version":"v1","cases":[{"id":"ok","sql_file":"ok.sql","expect_signal":"select_star"}]}`)
	write(t, validation, `{"dataset":{"rows":1},"measurement":{},"verdict":{"verdict":"Better"}} {}`)
	if _, err := Build(Input{ManifestPath: manifest, ValidationPath: validation}); err == nil {
		t.Fatal("trailing validation JSON accepted")
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
