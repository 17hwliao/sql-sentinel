package series

import (
	"fmt"
	"slices"

	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/report"
)

type Input struct {
	Source string
	Report report.Result
}

type Reason struct {
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
}

type Admission struct {
	CalibrationFiles            []string `json:"calibration_files"`
	MeasurementFile             string   `json:"measurement_file"`
	EligibleForPerformanceClaim bool     `json:"eligible_for_performance_claim"`
	EvidenceLevel               string   `json:"evidence_level"`
	RejectionReasons            []Reason `json:"rejection_reasons"`
}

const (
	ReasonSnapshotNotVerified           = "snapshot_not_verified"
	ReasonCalibrationNoiseExceedsLimit  = "calibration_noise_exceeds_limit"
	ReasonMeasurementMissing            = "measurement_missing_formal_rounds"
	ReasonMeasurementDirectionNotBetter = "measurement_direction_not_better"
	ReasonMeasurementNotEligible        = "measurement_not_eligible"
)

// Evaluate 汇总预先指定的 calibration 报告和一份 measurement 报告。
// 它只允许同一数据集、环境、Query Case、索引和采样协议的报告进入同一系列；
// 任一关键字段不同会返回错误，而不是选择其中“看起来更好”的一份继续计算。
func Evaluate(calibrations []Input, measurement Input) (Admission, error) {
	if len(calibrations) < 2 {
		return Admission{}, fmt.Errorf("至少需要两份 calibration 报告，收到 %d", len(calibrations))
	}
	seen := map[string]struct{}{}
	for _, in := range calibrations {
		if _, duplicate := seen[in.Source]; duplicate {
			return Admission{}, fmt.Errorf("calibration 报告 %q 被重复输入，不能用同一份报告充数", in.Source)
		}
		seen[in.Source] = struct{}{}
	}
	if measurement.Source == "" {
		return Admission{}, fmt.Errorf("measurement 报告路径不能为空")
	}

	base := fingerprintOf(calibrations[0].Report)
	for _, in := range append(append([]Input{}, calibrations...), measurement) {
		if in.Source == "" {
			return Admission{}, fmt.Errorf("报告路径不能为空")
		}
		if diff := base.diff(fingerprintOf(in.Report)); diff != "" {
			return Admission{}, fmt.Errorf("报告 %q 与首份 calibration 的 %s 不一致，拒绝汇总", in.Source, diff)
		}
	}

	out := Admission{EvidenceLevel: "L1", MeasurementFile: measurement.Source}
	for _, in := range calibrations {
		out.CalibrationFiles = append(out.CalibrationFiles, in.Source)
	}

	// 固定分类顺序：快照 → calibration 噪声 → measurement 角色/方向/单份准入。
	for _, in := range append(append([]Input{}, calibrations...), measurement) {
		if !in.Report.Snapshot.Match {
			out.RejectionReasons = append(out.RejectionReasons, Reason{Code: ReasonSnapshotNotVerified, Source: in.Source})
		}
	}
	for _, in := range calibrations {
		if in.Report.Measurement != nil || in.Report.Verdict != nil {
			return Admission{}, fmt.Errorf("calibration 报告 %q 包含正式测量或方向判定", in.Source)
		}
		if in.Report.Calibration.Baseline.NoiseRatio > measure.NoiseRatioLimit ||
			in.Report.Calibration.Candidate.NoiseRatio > measure.NoiseRatioLimit {
			out.RejectionReasons = append(out.RejectionReasons, Reason{Code: ReasonCalibrationNoiseExceedsLimit, Source: in.Source})
		}
	}

	m := measurement.Report
	if m.Measurement == nil || m.Verdict == nil || m.Admission.DirectionVerdict == nil {
		out.RejectionReasons = append(out.RejectionReasons, Reason{Code: ReasonMeasurementMissing, Source: measurement.Source})
	} else if *m.Admission.DirectionVerdict != string(measure.Better) {
		out.RejectionReasons = append(out.RejectionReasons, Reason{Code: ReasonMeasurementDirectionNotBetter, Source: measurement.Source})
	}
	if !m.Admission.EligibleForPerformanceClaim {
		out.RejectionReasons = append(out.RejectionReasons, Reason{Code: ReasonMeasurementNotEligible, Source: measurement.Source})
	}

	if len(out.RejectionReasons) == 0 {
		out.EligibleForPerformanceClaim = true
		out.EvidenceLevel = "L2"
	}
	return out, nil
}

type fingerprint struct {
	datasetVersion string
	snapshotDigest string
	mysqlVersion   string
	caseName       string
	caseSQL        string
	caseArgs       []string
	index          string
	executions     int
}

func fingerprintOf(r report.Result) fingerprint {
	return fingerprint{
		datasetVersion: r.Dataset.DatasetVersion,
		snapshotDigest: r.Snapshot.Digest,
		mysqlVersion:   r.Env.MySQL,
		caseName:       r.Case.Name,
		caseSQL:        r.Case.SQL,
		caseArgs:       r.Case.Args,
		index:          r.CandidateIndex,
		executions:     r.MeasurementProtocol.ExecutionsPerRound,
	}
}

func (a fingerprint) diff(b fingerprint) string {
	switch {
	case a.datasetVersion != b.datasetVersion:
		return "dataset_version"
	case a.snapshotDigest != b.snapshotDigest:
		return "snapshot.digest"
	case a.mysqlVersion != b.mysqlVersion:
		return "env.mysql_version"
	case a.caseName != b.caseName:
		return "case.name"
	case a.caseSQL != b.caseSQL:
		return "case.sql"
	case !slices.Equal(a.caseArgs, b.caseArgs):
		return "case.args"
	case a.index != b.index:
		return "candidate_index"
	case a.executions != b.executions:
		return "measurement_protocol.executions_per_round"
	default:
		return ""
	}
}
