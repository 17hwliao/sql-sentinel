package diagnosis

import (
	"encoding/json"
	"errors"
	"testing"

	"sqlsentinel/internal/plancompare"
)

func comparisonReport() plancompare.Report {
	bRows, cRows := int64(1000), int64(300)
	return plancompare.Report{
		GeneratedAt:   "2026-08-27T13:12:43Z",
		EvidenceLevel: "L1",
		Snapshot:      plancompare.Snapshot{Digest: "abc", Rows: 1000},
		SQL:           plancompare.SQLInput{SQL: "SELECT id FROM orders"},
		Baseline: plancompare.Plan{Summary: plancompare.Summary{
			UsesFilesort: true,
			Tables:       []plancompare.TableAccess{{TableName: "orders", RowsExaminedPerScan: &bRows}},
		}},
		Candidate: plancompare.Plan{Summary: plancompare.Summary{
			UsesFilesort: false,
			Tables:       []plancompare.TableAccess{{TableName: "orders", Key: "idx_cand_status_amount", RowsExaminedPerScan: &cRows}},
		}},
		Difference: plancompare.Difference{
			BaselineUsesFilesort:  true,
			CandidateUsesFilesort: false,
			CandidateOnlyKeys:     []string{"idx_cand_status_amount"},
		},
	}
}

func TestAnalyzeDerivesOnlySupportedHypotheses(t *testing.T) {
	raw, err := json.Marshal(comparisonReport())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.EvidenceLevel != "L1" || r.EligibleForPerformanceClaim || len(r.Hypotheses) != 3 {
		t.Fatalf("unexpected diagnosis: %+v", r)
	}
	for _, h := range r.Hypotheses {
		if len(h.RequiredVerification) != 2 || h.RequiredVerification[0] != "controlled_ab_measurement" {
			t.Fatalf("missing verification path: %+v", h)
		}
	}
}

func TestAnalyzeSkipsMissingOrIncomparableFacts(t *testing.T) {
	in := comparisonReport()
	in.Difference = plancompare.Difference{}
	in.Baseline.Summary.UsesFilesort = false
	in.Candidate.Summary.Tables[0].RowsExaminedPerScan = nil
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Analyze(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hypotheses) != 0 {
		t.Fatalf("unsupported hypotheses: %+v", r.Hypotheses)
	}
}

func TestAnalyzeRejectsUntrustedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*plancompare.Report)
		want   error
	}{
		{"wrong evidence level", func(r *plancompare.Report) { r.EvidenceLevel = "L2" }, ErrEvidenceLevel},
		{"performance eligible", func(r *plancompare.Report) { r.EligibleForPerformanceClaim = true }, ErrPerformanceEligible},
		{"missing SQL", func(r *plancompare.Report) { r.SQL.SQL = "" }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := comparisonReport()
			tc.mutate(&in)
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Analyze(raw)
			if err == nil {
				t.Fatal("untrusted input accepted")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := Analyze([]byte(`{"evidence_level":"L1"} {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := Analyze([]byte(`{"evidence_level":"L1","unexpected":true}`)); err == nil {
		t.Fatal("unknown JSON field accepted")
	}
}
