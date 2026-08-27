package measure

import (
	"fmt"
	"time"
)

// SignificanceMultiplier 是噪声比到判定门槛的倍数。
// spec.md 规定：中位数变化必须**严格超过 2 × 两侧较大噪声比**才承认 Better/Worse。
// 用 1× 会把与噪声同量级的抖动当成收益 —— 噪声比本身只是一侧的离散程度，
// 两侧各自抖动叠加后，1× 附近的差异根本无法区分真实变化与巧合。
const SignificanceMultiplier = 2.0

// Verdict 是三态判定结果。没有「大概变快了」这一档 ——
// 差异未严格超过门槛就是 NotSignificant，不允许含糊表述。
type Verdict string

const (
	Better         Verdict = "Better"
	Worse          Verdict = "Worse"
	NotSignificant Verdict = "NotSignificant"
)

// SideStats 是单侧的统计摘要。
type SideStats struct {
	Name       string
	Samples    int
	Median     time.Duration
	ScaledMAD  time.Duration
	NoiseRatio float64
}

// Summarize 把单侧的原始延迟样本折叠成统计摘要。
func Summarize(name string, xs []time.Duration) SideStats {
	return SideStats{
		Name:       name,
		Samples:    len(xs),
		Median:     Median(xs),
		ScaledMAD:  ScaledMAD(xs),
		NoiseRatio: NoiseRatio(xs),
	}
}

// Decision 是一次 A/B 的完整判定，包含判定所依据的门槛及其来源。
type Decision struct {
	Baseline  SideStats
	Candidate SideStats

	// MaxNoiseRatio 是两侧 NoiseRatio 的较大值，即门槛的计算基数。
	MaxNoiseRatio float64
	// ThresholdRatio 是最终判定门槛 = SignificanceMultiplier × MaxNoiseRatio。
	ThresholdRatio float64
	// ThresholdFrom 记录较大噪声取自哪一侧，便于复核。
	ThresholdFrom string

	// RelativeDelta = (candidate 中位数 - baseline 中位数) / baseline 中位数。
	// 负值表示 candidate 更快。
	RelativeDelta float64

	Verdict Verdict
}

// Decide 由两侧统计摘要得出三态判定。
//
// 门槛 = SignificanceMultiplier × max(两侧 NoiseRatio)，而不是某个全局 noise floor：
// 全局地板会用一侧的噪声去衡量另一侧，噪声大的那侧的抖动就会被当成真实差异。
// 只有当相对差异**严格超过**该门槛时才承认方向性结论。
func Decide(baseline, candidate SideStats) Decision {
	maxNoise := baseline.NoiseRatio
	from := baseline.Name
	if candidate.NoiseRatio > maxNoise {
		maxNoise = candidate.NoiseRatio
		from = candidate.Name
	}

	d := Decision{
		Baseline:       baseline,
		Candidate:      candidate,
		MaxNoiseRatio:  maxNoise,
		ThresholdRatio: SignificanceMultiplier * maxNoise,
		ThresholdFrom:  from,
	}

	if baseline.Median <= 0 {
		d.Verdict = NotSignificant
		return d
	}

	d.RelativeDelta = float64(candidate.Median-baseline.Median) / float64(baseline.Median)

	mag := d.RelativeDelta
	if mag < 0 {
		mag = -mag
	}

	switch {
	case mag <= d.ThresholdRatio:
		d.Verdict = NotSignificant
	case d.RelativeDelta < 0:
		d.Verdict = Better
	default:
		d.Verdict = Worse
	}
	return d
}

// String 给出一行可读结论。
func (d Decision) String() string {
	return fmt.Sprintf("%s: baseline=%s candidate=%s delta=%+.2f%% 门槛=%.2f%% (=%.1f×%.2f%%，噪声取自 %s)",
		d.Verdict,
		d.Baseline.Median.Round(time.Microsecond),
		d.Candidate.Median.Round(time.Microsecond),
		d.RelativeDelta*100,
		d.ThresholdRatio*100,
		SignificanceMultiplier,
		d.MaxNoiseRatio*100,
		d.ThresholdFrom)
}
