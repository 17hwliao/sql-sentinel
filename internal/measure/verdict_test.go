package measure

import (
	"math"
	"testing"
	"time"
)

// 本文件覆盖的风险：判定逻辑本身出错，则所有结论作废，且错误不会自己暴露。
// 三条核心断言：
//   1. MAD 必须乘 1.4826；漏掉会把噪声低估约 33%，噪声被当成收益。
//   2. 门槛必须是 2 × 两侧较大噪声比，不能是全局 noise floor，也不能只用 1×。
//   3. 中位数差未严格超过门槛时必须返回 NotSignificant，不允许含糊表述。

const eps = 1e-9

// ms 构造一组延迟样本。
func ms(vals ...float64) []time.Duration {
	out := make([]time.Duration, len(vals))
	for i, v := range vals {
		out[i] = time.Duration(v * float64(time.Millisecond))
	}
	return out
}

// 断言 1：缩放系数正确。
// 样本 1,2,3,4,5 → 中位数 3，偏差 2,1,0,1,2 → MAD=1 → scaled=1.4826。
func TestScaledMADAppliesCorrectFactor(t *testing.T) {
	xs := ms(1, 2, 3, 4, 5)

	if got, want := Median(xs), 3*time.Millisecond; got != want {
		t.Fatalf("中位数 = %s，应为 %s", got, want)
	}
	if got, want := MAD(xs), 1*time.Millisecond; got != want {
		t.Fatalf("MAD = %s，应为 %s", got, want)
	}

	wantScaled := time.Duration(MADScale * float64(time.Millisecond))
	if got := ScaledMAD(xs); got != wantScaled {
		t.Fatalf("ScaledMAD = %s，应为 %s（MAD × 1.4826）", got, wantScaled)
	}
	if math.Abs(MADScale-1.4826) > eps {
		t.Fatalf("MADScale = %v，必须是 1.4826", MADScale)
	}

	// noise ratio = 1.4826ms / 3ms
	if got, want := NoiseRatio(xs), 1.4826/3.0; math.Abs(got-want) > 1e-6 {
		t.Fatalf("NoiseRatio = %v，应为 %v", got, want)
	}
}

func TestMedianHandlesEvenCount(t *testing.T) {
	if got, want := Median(ms(1, 2, 3, 4)), 2500*time.Microsecond; got != want {
		t.Fatalf("偶数样本中位数 = %s，应为 %s", got, want)
	}
}

// 断言 2：门槛 = 2 × 两侧 NoiseRatio 的较大值，且记录较大噪声的来源。
func TestThresholdIsTwiceMaxOfBothSidesNotGlobalFloor(t *testing.T) {
	// baseline 很稳（噪声小），candidate 抖（噪声大）
	quiet := Summarize("baseline", ms(100, 100, 100, 101, 100, 100, 99))
	noisy := Summarize("candidate", ms(100, 88, 112, 95, 108, 91, 105))

	if quiet.NoiseRatio >= noisy.NoiseRatio {
		t.Fatalf("测试前提不成立: baseline 噪声 %.4f 应小于 candidate 噪声 %.4f",
			quiet.NoiseRatio, noisy.NoiseRatio)
	}

	d := Decide(quiet, noisy)

	if math.Abs(d.MaxNoiseRatio-noisy.NoiseRatio) > eps {
		t.Fatalf("MaxNoiseRatio = %.6f，应取两侧较大值 %.6f（candidate 侧）",
			d.MaxNoiseRatio, noisy.NoiseRatio)
	}
	if want := SignificanceMultiplier * noisy.NoiseRatio; math.Abs(d.ThresholdRatio-want) > eps {
		t.Fatalf("门槛 = %.6f，应为 %.1f × %.6f = %.6f",
			d.ThresholdRatio, SignificanceMultiplier, noisy.NoiseRatio, want)
	}
	if d.ThresholdFrom != "candidate" {
		t.Fatalf("噪声来源 = %q，应为 candidate", d.ThresholdFrom)
	}

	// 反向：把噪声大的那侧放到 baseline，仍应取较大者
	rev := Decide(Summarize("baseline", ms(100, 88, 112, 95, 108, 91, 105)),
		Summarize("candidate", ms(100, 100, 100, 101, 100, 100, 99)))
	if rev.ThresholdFrom != "baseline" {
		t.Fatalf("噪声来源 = %q，应为 baseline", rev.ThresholdFrom)
	}
}

// 断言 2 的核心：只超过 1× 噪声、但未超过 2× 噪声时，必须是 NotSignificant。
// 这正是把 1× 当门槛会产生假阳性的区间。
func TestDeltaBetweenOneAndTwoTimesNoiseIsNotSignificant(t *testing.T) {
	// 两侧噪声都精确设为 10%，门槛 = 2 × 10% = 20%
	base := SideStats{Name: "baseline", Median: 100 * time.Millisecond, NoiseRatio: 0.10}
	cand := SideStats{Name: "candidate", Median: 115 * time.Millisecond, NoiseRatio: 0.10}

	d := Decide(base, cand)

	// 差异 15%：严格大于 1× 噪声（10%），但小于 2× 噪声（20%）
	if math.Abs(d.RelativeDelta-0.15) > eps {
		t.Fatalf("相对差异 = %.6f，应为 0.15", d.RelativeDelta)
	}
	if d.RelativeDelta <= d.MaxNoiseRatio {
		t.Fatalf("测试前提不成立: 差异 %.4f 应大于 1× 噪声 %.4f", d.RelativeDelta, d.MaxNoiseRatio)
	}
	if d.RelativeDelta >= d.ThresholdRatio {
		t.Fatalf("测试前提不成立: 差异 %.4f 应小于门槛 %.4f", d.RelativeDelta, d.ThresholdRatio)
	}
	if d.Verdict != NotSignificant {
		t.Fatalf("判定 = %s，应为 NotSignificant —— 差异 %.1f%% 超过 1× 噪声但未超过门槛 %.1f%%，"+
			"说明门槛用了 1× 而非 %.1f×",
			d.Verdict, d.RelativeDelta*100, d.ThresholdRatio*100, SignificanceMultiplier)
	}

	// 同一组噪声下，差异超过 2× 才允许给方向性结论
	beyond := Decide(base, SideStats{Name: "candidate", Median: 125 * time.Millisecond, NoiseRatio: 0.10})
	if beyond.Verdict != Worse {
		t.Fatalf("差异 25%% 已超过门槛 20%%，判定 = %s，应为 Worse", beyond.Verdict)
	}
}

// 断言 2 的关键推论：不得用 baseline 的噪声去衡量 candidate。
// 若错误地采用 baseline 噪声作全局地板，下面这组会被误判为 Worse。
func TestNoisyCandidateIsNotJudgedByBaselineNoise(t *testing.T) {
	quiet := Summarize("baseline", ms(100, 100, 100, 101, 100, 100, 99))
	// candidate 中位数高约 5%，但它自身噪声远大于 5%
	noisy := Summarize("candidate", ms(105, 92, 118, 99, 113, 96, 110))

	d := Decide(quiet, noisy)

	if d.MaxNoiseRatio <= quiet.NoiseRatio {
		t.Fatalf("噪声基数 %.4f 未超过 baseline 噪声 %.4f，说明用了全局地板",
			d.MaxNoiseRatio, quiet.NoiseRatio)
	}
	if d.Verdict != NotSignificant {
		t.Fatalf("判定 = %s，应为 NotSignificant（差异 %.2f%% 未超过门槛 %.2f%%）",
			d.Verdict, d.RelativeDelta*100, d.ThresholdRatio*100)
	}
}

// 断言 3：差异未超门槛必须是 NotSignificant。
func TestDeltaWithinThresholdIsNotSignificant(t *testing.T) {
	base := Summarize("baseline", ms(100, 96, 104, 98, 102, 97, 103))
	if base.NoiseRatio <= 0.02 {
		t.Fatalf("测试前提不成立: baseline 噪声 %.4f 过小", base.NoiseRatio)
	}

	// candidate 中位数只低 1%，远小于噪声门槛
	cand := Summarize("candidate", ms(99, 95, 103, 97, 101, 96, 102))

	d := Decide(base, cand)
	if d.Verdict != NotSignificant {
		t.Fatalf("判定 = %s，应为 NotSignificant（差异 %.2f%%，门槛 %.2f%%）",
			d.Verdict, d.RelativeDelta*100, d.ThresholdRatio*100)
	}
}

// 边界：差异恰好等于门槛时不算显著（门槛是「严格超过」才成立）。
func TestDeltaExactlyAtThresholdIsNotSignificant(t *testing.T) {
	// 两侧较大噪声 10% → 门槛 20%；把中位数差设成正好 20%
	base := SideStats{Name: "baseline", Median: 100 * time.Millisecond, NoiseRatio: 0.10}
	cand := SideStats{Name: "candidate", Median: 120 * time.Millisecond, NoiseRatio: 0.05}

	d := Decide(base, cand)
	if math.Abs(d.ThresholdRatio-0.20) > eps {
		t.Fatalf("门槛 = %.6f，应为 0.20", d.ThresholdRatio)
	}
	if math.Abs(d.RelativeDelta-0.20) > eps {
		t.Fatalf("相对差异 = %.6f，应为 0.20", d.RelativeDelta)
	}
	if d.Verdict != NotSignificant {
		t.Fatalf("差异恰好等于门槛时判定 = %s，应为 NotSignificant", d.Verdict)
	}
}

// 方向性：严格超过门槛后，更快是 Better、更慢是 Worse。
func TestDirectionalVerdicts(t *testing.T) {
	// 噪声 5% → 门槛 10%
	base := SideStats{Name: "baseline", Median: 100 * time.Millisecond, NoiseRatio: 0.05}

	faster := SideStats{Name: "candidate", Median: 60 * time.Millisecond, NoiseRatio: 0.05}
	if got := Decide(base, faster); got.Verdict != Better {
		t.Fatalf("candidate 快 40%%（门槛 10%%），判定 = %s，应为 Better", got.Verdict)
	}

	slower := SideStats{Name: "candidate", Median: 140 * time.Millisecond, NoiseRatio: 0.05}
	if got := Decide(base, slower); got.Verdict != Worse {
		t.Fatalf("candidate 慢 40%%（门槛 10%%），判定 = %s，应为 Worse", got.Verdict)
	}
}

// 空样本不得 panic，也不得给出方向性结论。
func TestEmptySamplesAreNotSignificant(t *testing.T) {
	d := Decide(Summarize("baseline", nil), Summarize("candidate", nil))
	if d.Verdict != NotSignificant {
		t.Fatalf("空样本判定 = %s，应为 NotSignificant", d.Verdict)
	}
}
