package benchmark

import (
	"encoding/json"
	"os"
	"testing"
)

func TestConsolePerformanceFixtureMatchesGoWireContract(t *testing.T) {
	raw, err := os.ReadFile("../../web/test/fixtures/performance.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture Report
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if !fixture.Eligible || !fixture.ResultEquivalent || fixture.BaselineMeasurement.MedianMS != 12.5 || fixture.CandidateMeasurement.MedianMS != 6.25 || fixture.CandidateCalibration.NoiseRatio != 0.01 {
		t.Fatal("frontend fixture no longer matches actual Go wire names")
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"eligible_for_performance_claim", "BaselineMeasurement", "CandidateMeasurement", "CandidateCalibration"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("wire contract changed: %s", name)
		}
	}
}
