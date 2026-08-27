package report

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"sqlsentinel/internal/measure"
)

// Side 是单侧的统计结果，含原始样本。
// 原始样本必须落进报告：只留中位数的话，事后无法复核噪声计算，也看不出离群点分布。
type Side struct {
	Name        string    `json:"name"`
	Rounds      int       `json:"rounds"`
	RawMs       []float64 `json:"raw_ms"`
	MedianMs    float64   `json:"median_ms"`
	ScaledMADMs float64   `json:"scaled_mad_ms"`
	NoiseRatio  float64   `json:"noise_ratio"`
}

// Phase 是一个测量阶段（标定或正式测量）的两侧结果。
type Phase struct {
	Mode      string `json:"mode"`
	Baseline  Side   `json:"baseline"`
	Candidate Side   `json:"candidate"`
}

// Verdict 是判定块。所有数值都直接取自 measure.Decision，报告层不重算。
type Verdict struct {
	MaxNoiseRatio          float64 `json:"max_noise_ratio"`
	SignificanceMultiplier float64 `json:"significance_multiplier"`
	ThresholdRatio         float64 `json:"threshold_ratio"`
	ThresholdFrom          string  `json:"threshold_from"`
	RelativeDelta          float64 `json:"relative_delta"`
	Verdict                string  `json:"verdict"`
}

// RejectionReason 是一条拒绝原因。Code 是稳定蛇形标识供程序判等，
// Description 是随版本可变的人类说明 —— 两者的稳定性要求不同，所以分开两个字段。
type RejectionReason struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// AdmissionThresholds 把判定所用的两个门槛落盘，读者不必翻代码复核。
type AdmissionThresholds struct {
	NoiseRatioLimit        float64 `json:"noise_ratio_limit"`
	SignificanceMultiplier float64 `json:"significance_multiplier"`
}

// Admission 是证据准入块。
//
// DirectionVerdict 与 EligibleForPerformanceClaim 是**两个独立字段**：
// 前者是方向性统计判定（未测量时缺失），后者是能否对外声称性能收益。
// 噪声超标时前者仍可以是 Better 而后者必须是 false ——
// 下游只能把 eligible_for_performance_claim=true 的报告当性能收益证据，
// 单看方向判定就会复现「读到 Better 就误报收益」的事故。
type Admission struct {
	DirectionVerdict            *string             `json:"direction_verdict,omitempty"`
	EligibleForPerformanceClaim bool                `json:"eligible_for_performance_claim"`
	EvidenceLevel               string              `json:"evidence_level"`
	RejectionReasons            []RejectionReason   `json:"rejection_reasons"`
	Thresholds                  AdmissionThresholds `json:"admission_thresholds"`
}

// DatasetInfo 记录复现数据集所需的全部版本信息。
type DatasetInfo struct {
	Seed                uint64 `json:"seed"`
	Rows                int64  `json:"rows"`
	DatasetVersion      string `json:"dataset_version"`
	SchemaVersion       string `json:"schema_version"`
	GeneratorVersion    string `json:"generator_version"`
	DistributionVersion string `json:"distribution_version"`
}

// SnapshotInfo 记录门禁现场重算的快照结论。
type SnapshotInfo struct {
	Digest string `json:"digest"`
	Rows   int64  `json:"rows"`
	Match  bool   `json:"match"`
}

// CaseInfo 记录被测的 Query Case。
type CaseInfo struct {
	Name string   `json:"name"`
	SQL  string   `json:"sql"`
	Args []string `json:"args"`
}

// EnvInfo 记录环境，用于判断报告是否可跨机器比较。
type EnvInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	GoVersion string `json:"go_version"`
	MySQL     string `json:"mysql_version"`
}

// MeasurementProtocol 记录样本如何采集。raw_ms 是按本协议折算出的单次耗时，
// 因此消费者必须同时读取 executions_per_round，不能把批量墙钟时间误当成单条 SQL 延迟。
type MeasurementProtocol struct {
	ExecutionsPerRound int `json:"executions_per_round"`
}

// Result 是一次 bench 的完整结果，也是**唯一**的事实来源。
// JSON 与终端摘要都只读本对象，任何一方自己重算统计量都会导致两者悄悄不一致。
type Result struct {
	GeneratedAt         string              `json:"generated_at"`
	Env                 EnvInfo             `json:"env"`
	Dataset             DatasetInfo         `json:"dataset"`
	Snapshot            SnapshotInfo        `json:"snapshot"`
	Case                CaseInfo            `json:"case"`
	CandidateIndex      string              `json:"candidate_index"`
	MeasurementProtocol MeasurementProtocol `json:"measurement_protocol"`

	Calibration Phase  `json:"calibration"`
	Measurement *Phase `json:"measurement,omitempty"`

	// Verdict 在 --rounds 0（只标定）时为空：没有测量就没有结论，
	// 不能给一个默认的 NotSignificant 冒充结果。
	Verdict *Verdict `json:"verdict,omitempty"`

	// Admission 是证据准入结论。无论是否做过正式测量都存在：
	// 「只标定了」本身就是一种准入状态，而不是字段缺失。
	Admission Admission `json:"admission"`
}

// NewAdmission 由 measure 的准入结论构造报告块，逐字段搬运。
func NewAdmission(a measure.Admission) Admission {
	out := Admission{
		EligibleForPerformanceClaim: a.EligibleForPerformanceClaim,
		EvidenceLevel:               string(a.EvidenceLevel),
		RejectionReasons:            make([]RejectionReason, 0, len(a.RejectionReasons)),
		Thresholds: AdmissionThresholds{
			NoiseRatioLimit:        a.Thresholds.NoiseRatioLimit,
			SignificanceMultiplier: a.Thresholds.SignificanceMultiplier,
		},
	}
	if a.DirectionVerdict != nil {
		v := string(*a.DirectionVerdict)
		out.DirectionVerdict = &v
	}
	for _, r := range a.RejectionReasons {
		out.RejectionReasons = append(out.RejectionReasons,
			RejectionReason{Code: string(r), Description: r.Description()})
	}
	return out
}

// NewSide 由统计摘要与原始样本构造报告侧。
func NewSide(stats measure.SideStats, raw []time.Duration) Side {
	ms := make([]float64, len(raw))
	for i, d := range raw {
		ms[i] = toMs(d)
	}
	return Side{
		Name:        stats.Name,
		Rounds:      stats.Samples,
		RawMs:       ms,
		MedianMs:    toMs(stats.Median),
		ScaledMADMs: toMs(stats.ScaledMAD),
		NoiseRatio:  stats.NoiseRatio,
	}
}

// NewVerdict 由判定结果构造报告判定块，逐字段搬运，不做任何再计算。
func NewVerdict(d measure.Decision) Verdict {
	return Verdict{
		MaxNoiseRatio:          d.MaxNoiseRatio,
		SignificanceMultiplier: measure.SignificanceMultiplier,
		ThresholdRatio:         d.ThresholdRatio,
		ThresholdFrom:          d.ThresholdFrom,
		RelativeDelta:          d.RelativeDelta,
		Verdict:                string(d.Verdict),
	}
}

func toMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// WriteJSON 输出机器可读报告。
func WriteJSON(w io.Writer, r Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteSummary 输出终端摘要。只格式化 Result 已有的字段，不重算任何统计量。
func WriteSummary(w io.Writer, r Result) {
	fmt.Fprintf(w, "\n=== A/B 测量报告 ===\n")
	fmt.Fprintf(w, "生成时间   %s\n", r.GeneratedAt)
	fmt.Fprintf(w, "环境       %s/%s  Go %s  MySQL %s\n",
		r.Env.OS, r.Env.Arch, r.Env.GoVersion, r.Env.MySQL)
	fmt.Fprintf(w, "数据集     seed=%d rows=%d version=%s\n",
		r.Dataset.Seed, r.Dataset.Rows, r.Dataset.DatasetVersion)
	fmt.Fprintf(w, "           schema=%s generator=%s distribution=%s\n",
		r.Dataset.SchemaVersion, r.Dataset.GeneratorVersion, r.Dataset.DistributionVersion)
	fmt.Fprintf(w, "快照       rows=%d digest=%s 一致=%t\n",
		r.Snapshot.Rows, shortHex(r.Snapshot.Digest), r.Snapshot.Match)
	fmt.Fprintf(w, "Case       %s\n", r.Case.Name)
	fmt.Fprintf(w, "候选索引   %s\n", r.CandidateIndex)
	fmt.Fprintf(w, "测量协议   每轮完整执行 %d 次；raw_ms 为 batch_elapsed / %d\n",
		r.MeasurementProtocol.ExecutionsPerRound, r.MeasurementProtocol.ExecutionsPerRound)

	writePhase(w, r.Calibration)
	if r.Measurement != nil {
		writePhase(w, *r.Measurement)
	}

	if r.Verdict == nil {
		fmt.Fprintf(w, "\n判定       未测量（只做了标定），无方向性结果\n")
	} else {
		v := *r.Verdict
		fmt.Fprintf(w, "\n噪声基数   %.2f%%（取自 %s，两侧较大者）\n", v.MaxNoiseRatio*100, v.ThresholdFrom)
		fmt.Fprintf(w, "判定门槛   %.2f%%  = %.1f × %.2f%%\n",
			v.ThresholdRatio*100, v.SignificanceMultiplier, v.MaxNoiseRatio*100)
		fmt.Fprintf(w, "中位数变化 %+.2f%%\n", v.RelativeDelta*100)
		fmt.Fprintf(w, "方向性结果 %s\n", v.Verdict)
	}

	writeAdmission(w, r.Admission)
}

// writeAdmission 输出证据准入。方向性结果与能否声称收益分两行呈现：
// 只看 Better 就当收益，正是本切片要防的事故。
func writeAdmission(w io.Writer, a Admission) {
	fmt.Fprintf(w, "\n=== 性能收益声称资格 ===\n")
	fmt.Fprintf(w, "允许声称性能收益 %t\n", a.EligibleForPerformanceClaim)

	if len(a.RejectionReasons) == 0 {
		fmt.Fprintf(w, "拒绝原因         无\n")
		return
	}
	for _, r := range a.RejectionReasons {
		fmt.Fprintf(w, "拒绝原因         %s — %s\n", r.Code, r.Description)
	}
}

func writePhase(w io.Writer, p Phase) {
	fmt.Fprintf(w, "\n--- %s ---\n", p.Mode)
	fmt.Fprintf(w, "  %-10s %-8s %-12s %-14s %s\n", "侧", "轮数", "中位数(ms)", "scaledMAD(ms)", "噪声比")
	for _, s := range []Side{p.Baseline, p.Candidate} {
		fmt.Fprintf(w, "  %-10s %-8d %-12.3f %-14.3f %.2f%%\n",
			s.Name, s.Rounds, s.MedianMs, s.ScaledMADMs, s.NoiseRatio*100)
	}
}

func shortHex(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:16] + "…"
}
