package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sqlsentinel/internal/agentmeshbridge"
)

func TestWriteNewBridgeReportDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.json")
	report := agentmeshbridge.Report{Status: agentmeshbridge.StatusUnavailable, Code: agentmeshbridge.ReasonConfigurationMissing, EvidenceLevel: "L1"}
	if err := writeNewBridgeReport(path, report); err != nil {
		t.Fatal(err)
	}
	if err := writeNewBridgeReport(path, report); err == nil {
		t.Fatal("second report write unexpectedly overwrote first report")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got agentmeshbridge.Report
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != report.Code || got.EvidenceLevel != "L1" || got.EligibleForPerformanceClaim {
		t.Fatalf("report = %#v", got)
	}
}
