package benchmark

import (
	"testing"
	"time"
)

func TestL3RequiresAllIndependentEvidence(t *testing.T) {
	now := time.Now()
	p := Representative{DatasetID: "redacted-dataset", SnapshotSHA256: "snap", WorkloadSHA256: "sql", ApprovedBy: "operator", Redacted: true, ExpiresAt: now.Add(time.Hour)}
	for _, field := range []string{"valid", "changed_snapshot", "changed_query", "expired", "synthetic", "no_read_benefit", "different_result"} {
		r := Report{EvidenceLevel: "L2", Eligible: true, ResultEquivalent: true, SnapshotSHA256: "snap", WorkloadSHA256: "sql"}
		copy := p
		switch field {
		case "changed_snapshot":
			copy.SnapshotSHA256 = "other"
		case "changed_query":
			copy.WorkloadSHA256 = "other"
		case "expired":
			copy.ExpiresAt = now.Add(-time.Second)
		case "synthetic":
			copy.Redacted = false
		case "no_read_benefit":
			r.Eligible = false
		case "different_result":
			r.ResultEquivalent = false
		}
		Qualify(&r, &copy, now)
		if (r.EvidenceLevel == "L3") != (field == "valid") {
			t.Fatalf("%s => %+v", field, r)
		}
	}
}
