package series

import (
	"strings"
	"testing"

	"sqlsentinel/internal/report"
)

func testReport(noise float64, measurement bool) report.Result {
	r := report.Result{
		Env:                 report.EnvInfo{MySQL: "8.4.11"},
		Dataset:             report.DatasetInfo{DatasetVersion: "dataset-v1"},
		Snapshot:            report.SnapshotInfo{Digest: "sha256-same", Match: true},
		Case:                report.CaseInfo{Name: "case", SQL: "SELECT 1", Args: []string{"1"}},
		CandidateIndex:      "idx_candidate",
		MeasurementProtocol: report.MeasurementProtocol{ExecutionsPerRound: 10},
		Calibration:         report.Phase{Baseline: report.Side{NoiseRatio: noise}, Candidate: report.Side{NoiseRatio: noise}},
	}
	if measurement {
		better := "Better"
		r.Measurement = &report.Phase{}
		r.Verdict = &report.Verdict{Verdict: better}
		r.Admission = report.Admission{DirectionVerdict: &better, EligibleForPerformanceClaim: true, EvidenceLevel: "L2"}
	}
	return r
}

func inputs(noiseA, noiseB float64) ([]Input, Input) {
	return []Input{{Source: "cal-1.json", Report: testReport(noiseA, false)}, {Source: "cal-2.json", Report: testReport(noiseB, false)}},
		Input{Source: "measurement.json", Report: testReport(0.02, true)}
}

func TestEvaluateQuietSeriesIsL2(t *testing.T) {
	cals, measurement := inputs(0.03, 0.05)
	got, err := Evaluate(cals, measurement)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EligibleForPerformanceClaim || got.EvidenceLevel != "L2" || len(got.RejectionReasons) != 0 {
		t.Fatalf("unexpected series admission: %+v", got)
	}
}

func TestEvaluateCalibrationNoiseBlocksSeries(t *testing.T) {
	cals, measurement := inputs(0.03, 0.1019)
	got, err := Evaluate(cals, measurement)
	if err != nil {
		t.Fatal(err)
	}
	if got.EligibleForPerformanceClaim || got.EvidenceLevel != "L1" {
		t.Fatalf("noisy calibration must be L1: %+v", got)
	}
	if len(got.RejectionReasons) != 1 || got.RejectionReasons[0] != (Reason{Code: ReasonCalibrationNoiseExceedsLimit, Source: "cal-2.json"}) {
		t.Fatalf("unexpected reasons: %+v", got.RejectionReasons)
	}
}

// 风险：不同环境或不同采样协议的报告被混在一起，会形成没有意义的“系列平均”。
func TestEvaluateRejectsFingerprintMismatch(t *testing.T) {
	fields := []struct {
		name   string
		mutate func(*report.Result)
		want   string
	}{
		{"dataset", func(r *report.Result) { r.Dataset.DatasetVersion = "other" }, "dataset_version"},
		{"snapshot", func(r *report.Result) { r.Snapshot.Digest = "other" }, "snapshot.digest"},
		{"mysql", func(r *report.Result) { r.Env.MySQL = "8.4.12" }, "env.mysql_version"},
		{"case", func(r *report.Result) { r.Case.SQL = "SELECT 2" }, "case.sql"},
		{"index", func(r *report.Result) { r.CandidateIndex = "other" }, "candidate_index"},
		{"protocol", func(r *report.Result) { r.MeasurementProtocol.ExecutionsPerRound = 1 }, "executions_per_round"},
	}
	for _, tc := range fields {
		t.Run(tc.name, func(t *testing.T) {
			cals, measurement := inputs(0.03, 0.04)
			tc.mutate(&measurement.Report)
			_, err := Evaluate(cals, measurement)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want fingerprint error containing %q", err, tc.want)
			}
		})
	}
}

func TestEvaluateRejectsMissingOrUnqualifiedMeasurement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*report.Result)
		want   string
	}{
		{"missing", func(r *report.Result) { r.Measurement, r.Verdict, r.Admission.DirectionVerdict = nil, nil, nil }, ReasonMeasurementMissing},
		{"not-better", func(r *report.Result) { v := "NotSignificant"; r.Admission.DirectionVerdict = &v }, ReasonMeasurementDirectionNotBetter},
		{"not-eligible", func(r *report.Result) { r.Admission.EligibleForPerformanceClaim = false }, ReasonMeasurementNotEligible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cals, measurement := inputs(0.03, 0.04)
			tc.mutate(&measurement.Report)
			got, err := Evaluate(cals, measurement)
			if err != nil || got.EligibleForPerformanceClaim {
				t.Fatalf("admission=%+v err=%v", got, err)
			}
			found := false
			for _, reason := range got.RejectionReasons {
				found = found || reason.Code == tc.want
			}
			if !found {
				t.Fatalf("reasons=%+v, want %s", got.RejectionReasons, tc.want)
			}
		})
	}
}

func TestEvaluateRejectsDuplicateCalibrationFile(t *testing.T) {
	cals, measurement := inputs(0.03, 0.04)
	cals[1].Source = cals[0].Source
	if _, err := Evaluate(cals, measurement); err == nil || !strings.Contains(err.Error(), "重复输入") {
		t.Fatalf("err=%v, want duplicate calibration rejection", err)
	}
}
