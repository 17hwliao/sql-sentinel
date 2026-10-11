package writebench

import (
	"sqlsentinel/internal/candidate"
	"testing"
)

func TestRejectUnsupportedFixtureAndColumns(t *testing.T) {
	for _, s := range []candidate.Spec{{Table: "production", IndexName: "idx_cand_x", Columns: []candidate.Column{{Name: "user_id"}}}, {Table: "orders", IndexName: "idx_cand_x", Columns: []candidate.Column{{Name: "secret"}}}} {
		if Validate(s) == nil {
			t.Fatal("unsafe fixture admitted")
		}
	}
	if p95([]float64{2, 1, 3, 4}) != 4 {
		t.Fatal("incorrect latency percentile")
	}
}
