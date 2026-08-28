package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunEvaluateWritesReportWithoutRunningSecurityTests(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(filepath.Join(dir, "case.sql"), []byte("SELECT * FROM orders"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"dataset_version":"v1","cases":[{"id":"case","sql_file":"case.sql","expect_signal":"select_star"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	validation := filepath.Join(dir, "validation.json")
	if err := os.WriteFile(validation, []byte(`{"dataset":{"rows":1},"measurement":{},"verdict":{"verdict":"Better"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "evaluation.json")
	if err := runEvaluate([]string{"--manifest", manifest, "--validation", validation, "--out", out, "--run-security-tests=false"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("evaluation output not written: %v", err)
	}
}
