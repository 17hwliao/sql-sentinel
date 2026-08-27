package shadowgate

import (
	"encoding/json"
	"errors"
	"testing"

	"sqlsentinel/internal/candidate"
)

func validSpec() candidate.Spec {
	return candidate.Spec{Table: "orders", IndexName: "idx_cand_status_amount", Columns: []candidate.Column{
		{Name: "status"}, {Name: "amount_cents", Direction: "DESC"},
	}}
}

func TestPrepareAcceptsConstrainedInputs(t *testing.T) {
	p, err := Prepare("SELECT id FROM orders WHERE status = 'paid'", validSpec())
	if err != nil {
		t.Fatal(err)
	}
	if p.DDL != "CREATE INDEX `idx_cand_status_amount` ON `orders` (`status` ASC, `amount_cents` DESC)" {
		t.Fatalf("unexpected DDL: %s", p.DDL)
	}
}

func TestPrepareRejectsBeforeDatabaseAccess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sql     string
		spec    candidate.Spec
		wantErr error
		success bool
	}{
		{"write SQL", "DELETE FROM orders", validSpec(), ErrSQLRejected, false},
		{"bind marker", "SELECT id FROM orders WHERE id = ?", validSpec(), ErrPlaceholderUnsupported, false},
		{"question mark literal", "SELECT '?' FROM orders", validSpec(), nil, true},
		{"unsupported table", "SELECT id FROM orders", candidate.Spec{Table: "audit", IndexName: "idx_cand_audit", Columns: []candidate.Column{{Name: "id"}}}, ErrUnsupportedTable, false},
		{"unsafe CandidateSpec", "SELECT id FROM orders", candidate.Spec{Table: "orders", IndexName: "not_allowed", Columns: []candidate.Column{{Name: "id"}}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Prepare(tc.sql, tc.spec)
			if tc.success {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an admission error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestSameIndexRequiresExactColumnsAndDirections(t *testing.T) {
	expected := []indexColumn{{Name: "status", Direction: "ASC"}, {Name: "amount_cents", Direction: "DESC"}}
	if !sameIndex(expected, expected) {
		t.Fatal("matching index rejected")
	}
	if sameIndex(expected, []indexColumn{{Name: "status", Direction: "ASC"}, {Name: "amount_cents", Direction: "ASC"}}) {
		t.Fatal("direction mismatch accepted")
	}
	if sameIndex(expected, []indexColumn{{Name: "amount_cents", Direction: "DESC"}, {Name: "status", Direction: "ASC"}}) {
		t.Fatal("column order mismatch accepted")
	}
}

func TestReportUsesSharedPerformanceClaimKey(t *testing.T) {
	b, err := json.Marshal(Report{EligibleForPerformanceClaim: false})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["eligible_for_performance_claim"]; !ok {
		t.Fatal("missing shared eligibility key")
	}
	if _, ok := fields["performance_claim_eligible"]; ok {
		t.Fatal("legacy eligibility key must not be emitted")
	}
}
