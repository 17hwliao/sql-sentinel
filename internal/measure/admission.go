package measure

import "fmt"

// NoiseRatioLimit 是标定噪声比的准入线。
// 任一侧超过它，本次实验就没有资格支撑性能收益结论 —— 与方向判定是否显著无关。
// 这两件事必须分开：方向判定回答「谁更快」，准入回答「这次测量够不够格下结论」。
const NoiseRatioLimit = 0.10

// EvidenceLevel 是证据等级，取值沿用 README 的 L0–L3。
type EvidenceLevel string

const (
	// EvidenceL1 计划级推断：测量做了，但不足以支撑性能收益结论。
	EvidenceL1 EvidenceLevel = "L1"
	// EvidenceL2 基准数据集验证通过：噪声准入合格且方向判定显著。
	EvidenceL2 EvidenceLevel = "L2"
)

// RejectionReason 是稳定的机器可读原因码。
// 用固定 snake_case 标识而不是人话：下游要靠它判等，措辞一改就会静默失配。
type RejectionReason string

const (
	ReasonBaselineNoiseExceedsLimit  RejectionReason = "baseline_noise_ratio_exceeds_limit"
	ReasonCandidateNoiseExceedsLimit RejectionReason = "candidate_noise_ratio_exceeds_limit"
	ReasonDirectionNotSignificant    RejectionReason = "direction_verdict_not_significant"
	ReasonDirectionWorse             RejectionReason = "direction_verdict_worse"
	ReasonNoMeasurement              RejectionReason = "no_measurement_rounds"
)

// reasonOrder 固定原因码的输出顺序。
// 不按判定过程的先后或 map 遍历输出：顺序不稳定会让报告在数据没变时产生 diff，
// 也会让下游对「第一条原因」的任何假设失效。
var reasonOrder = []RejectionReason{
	ReasonNoMeasurement,
	ReasonBaselineNoiseExceedsLimit,
	ReasonCandidateNoiseExceedsLimit,
	ReasonDirectionNotSignificant,
	ReasonDirectionWorse,
}

// Description 返回原因码的人类可读说明。原因码本身保持稳定，说明可以改。
func (r RejectionReason) Description() string {
	switch r {
	case ReasonBaselineNoiseExceedsLimit:
		return fmt.Sprintf("baseline 标定噪声比超过准入线 %.0f%%", NoiseRatioLimit*100)
	case ReasonCandidateNoiseExceedsLimit:
		return fmt.Sprintf("candidate 标定噪声比超过准入线 %.0f%%", NoiseRatioLimit*100)
	case ReasonDirectionNotSignificant:
		return "中位数变化未严格超过方向判定门槛"
	case ReasonDirectionWorse:
		return "候选方案更慢，不存在读收益"
	case ReasonNoMeasurement:
		return "只做了标定，未进行正式测量"
	default:
		return string(r)
	}
}

// AdmissionThresholds 把判定所用的门槛随报告一起落盘，
// 使读者不必翻代码或文档就能复核结论。
type AdmissionThresholds struct {
	NoiseRatioLimit        float64
	SignificanceMultiplier float64
}

func DefaultThresholds() AdmissionThresholds {
	return AdmissionThresholds{
		NoiseRatioLimit:        NoiseRatioLimit,
		SignificanceMultiplier: SignificanceMultiplier,
	}
}

// Admission 是证据准入结论。
//
// DirectionVerdict 与 EligibleForPerformanceClaim 是**两个独立字段**：
// 前者是纯统计的方向判定，后者是能否对外声称收益。
// 噪声超标时前者仍可能是 Better，而后者必须是 false ——
// 把两者合成一个字段，就是 README/plan 里那条限定丢失的根源。
type Admission struct {
	// DirectionVerdict 为 nil 表示未做正式测量，因此没有方向判定。
	// 这里不能退化成 NotSignificant：那是「测了但差异不显著」，
	// 与「根本没测」是两种不同的证据状态。
	DirectionVerdict *Verdict

	EligibleForPerformanceClaim bool
	EvidenceLevel               EvidenceLevel
	RejectionReasons            []RejectionReason
	Thresholds                  AdmissionThresholds
}

// Admit 由标定噪声与方向判定算出准入结论。
//
// calibration 是两侧**标定**阶段的统计摘要 —— 与方向判定门槛所用的基数一致，
// 否则两个结论可能建立在不同数据上，互相矛盾却都自称成立。
// decision 为 nil 表示未做正式测量。
func Admit(baselineCal, candidateCal SideStats, decision *Decision) Admission {
	a := Admission{
		EvidenceLevel: EvidenceL1,
		Thresholds:    DefaultThresholds(),
	}

	hit := map[RejectionReason]bool{}

	if baselineCal.NoiseRatio > NoiseRatioLimit {
		hit[ReasonBaselineNoiseExceedsLimit] = true
	}
	if candidateCal.NoiseRatio > NoiseRatioLimit {
		hit[ReasonCandidateNoiseExceedsLimit] = true
	}

	if decision == nil {
		hit[ReasonNoMeasurement] = true
	} else {
		v := decision.Verdict
		a.DirectionVerdict = &v
		switch v {
		case NotSignificant:
			hit[ReasonDirectionNotSignificant] = true
		case Worse:
			hit[ReasonDirectionWorse] = true
		}
	}

	for _, r := range reasonOrder {
		if hit[r] {
			a.RejectionReasons = append(a.RejectionReasons, r)
		}
	}

	if len(a.RejectionReasons) == 0 {
		a.EligibleForPerformanceClaim = true
		a.EvidenceLevel = EvidenceL2
	}
	return a
}
