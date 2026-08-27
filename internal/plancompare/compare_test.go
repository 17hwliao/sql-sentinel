package plancompare

import "testing"

const baselinePlan = `{
  "query_block": {
    "ordering_operation": {
      "using_filesort": true,
      "table": {
        "table_name": "orders",
        "access_type": "ALL",
        "rows_examined_per_scan": "1000000"
      }
    }
  }
}`

const candidatePlan = `{
  "query_block": {
    "ordering_operation": {
      "using_filesort": false,
      "table": {
        "table_name": "orders",
        "access_type": "ref",
        "possible_keys": ["idx_cand_status_amount"],
        "key": "idx_cand_status_amount",
        "rows_examined_per_scan": "495956",
        "using_index": true
      }
    }
  }
}`

func TestBuildReportExtractsFactsWithoutPerformanceClaim(t *testing.T) {
	r, err := BuildReport(Metadata{MySQLVersion: "8.4.11"}, []byte(baselinePlan), []byte(candidatePlan))
	if err != nil {
		t.Fatal(err)
	}
	if r.EvidenceLevel != "L1" || r.EligibleForPerformanceClaim {
		t.Fatalf("unexpected evidence: %+v", r)
	}
	if !r.Baseline.Summary.UsesFilesort || r.Candidate.Summary.UsesFilesort || !r.Difference.FilesortChanged {
		t.Fatalf("filesort difference missing: %+v", r.Difference)
	}
	if len(r.Candidate.Summary.Tables) != 1 || r.Candidate.Summary.Tables[0].Key != "idx_cand_status_amount" {
		t.Fatalf("candidate table summary: %+v", r.Candidate.Summary.Tables)
	}
	if got := r.Candidate.Summary.Tables[0].RowsExaminedPerScan; got == nil || *got != 495956 {
		t.Fatalf("rows = %v", got)
	}
	if len(r.Difference.CandidateOnlyKeys) != 1 || r.Difference.CandidateOnlyKeys[0] != "idx_cand_status_amount" {
		t.Fatalf("candidate-only keys: %+v", r.Difference.CandidateOnlyKeys)
	}
}

func TestParseIgnoresUnknownAndMultiplePlanNodes(t *testing.T) {
	plan, err := Parse([]byte(`{"unknown":{"anything":true},"nested_loop":[{"table":{"table_name":"a","access_type":"ref"}},{"table":{"table_name":"b","key":"idx_b"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Summary.Tables) != 2 || plan.Summary.Tables[1].Key != "idx_b" {
		t.Fatalf("tables: %+v", plan.Summary.Tables)
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	if _, err := Parse([]byte(`{"query_block":`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
