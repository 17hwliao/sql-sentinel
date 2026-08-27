package measure

import (
	"testing"
	"time"
)

// side 构造一个标定统计摘要。中位数本身不参与准入判定，噪声比才是。
func side(name string, noiseRatio float64) SideStats {
	return SideStats{
		Name:       name,
		Samples:    20,
		Median:     12 * time.Millisecond,
		ScaledMAD:  time.Duration(noiseRatio * float64(12*time.Millisecond)),
		NoiseRatio: noiseRatio,
	}
}

func reasonsContain(t *testing.T, a Admission, want RejectionReason) {
	t.Helper()
	for _, r := range a.RejectionReasons {
		if r == want {
			return
		}
	}
	t.Fatalf("rejection_reasons 中找不到 %s，实际: %v", want, a.RejectionReasons)
}

func assertNotEligible(t *testing.T, a Admission, level EvidenceLevel) {
	t.Helper()
	if a.EligibleForPerformanceClaim {
		t.Errorf("eligible_for_performance_claim 应为 false")
	}
	if a.EvidenceLevel != level {
		t.Errorf("evidence_level = %s, 期望 %s", a.EvidenceLevel, level)
	}
	if len(a.RejectionReasons) == 0 {
		t.Errorf("不允许声称收益时 rejection_reasons 不应为空")
	}
}

// 验收条件 1：既有实验数据的复现 —— candidate 噪声 13.02% 超线 + 方向 Better。
// direction 与 eligible 必须是两个独立字段：方向仍是 Better，但不得声称收益。
func TestAdmit_CandidateNoiseExceeds_BetterDirection(t *testing.T) {
	baseline := side("baseline", 0.0421)
	candidate := side("candidate", 0.1302)
	decision := Decide(baseline, candidate)

	if decision.Verdict != NotSignificant {
		t.Fatalf("前置校验：13.02%% 噪声下 delta 未超门槛应判 NotSignificant，得到 %s；测试需要构造真实的 Better", decision.Verdict)
	}

	// 直接构造 Better 判定（更快），复现 validation_result.json 的关键组合
	better := Decision{Verdict: Better}
	a := Admit(baseline, candidate, &better)

	assertNotEligible(t, a, EvidenceL1)
	reasonsContain(t, a, ReasonCandidateNoiseExceedsLimit)

	if a.DirectionVerdict == nil || *a.DirectionVerdict != Better {
		t.Errorf("direction_verdict 应为 Better（与准入独立），实际 %v", a.DirectionVerdict)
	}
}

// 验收条件 2：任一侧超标即不合格，无论方向判定是什么。
func TestAdmit_AnySideNoiseExceeds_Disqualifies(t *testing.T) {
	cases := []struct {
		name      string
		baseNoise float64
		candNoise float64
		want      RejectionReason
	}{
		{"candidate超标", 0.05, 0.13, ReasonCandidateNoiseExceedsLimit},
		{"baseline超标", 0.11, 0.05, ReasonBaselineNoiseExceedsLimit},
		{"两侧都超标", 0.20, 0.30, ReasonCandidateNoiseExceedsLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decision{Verdict: Better}
			a := Admit(side("baseline", tc.baseNoise), side("candidate", tc.candNoise), &d)
			assertNotEligible(t, a, EvidenceL1)
			reasonsContain(t, a, tc.want)
		})
	}
}

// 验收条件 3：两侧噪声均合格 + Better → 允许声称收益，L2，原因码为空。
func TestAdmit_AllPass_Better_IsEligible(t *testing.T) {
	a := Admit(side("baseline", 0.03), side("candidate", 0.06),
		&Decision{Verdict: Better})

	if !a.EligibleForPerformanceClaim {
		t.Fatalf("合格 + Better 应允许声称收益")
	}
	if a.EvidenceLevel != EvidenceL2 {
		t.Errorf("evidence_level = %s, 期望 L2", a.EvidenceLevel)
	}
	if len(a.RejectionReasons) != 0 {
		t.Errorf("rejection_reasons 应为空，实际 %v", a.RejectionReasons)
	}
}

// 验收条件 4：NotSignificant 且噪声合格 → 原因指向方向判定而非噪声。
func TestAdmit_NotSignificant_QuietNoise_PointsToDirection(t *testing.T) {
	a := Admit(side("baseline", 0.04), side("candidate", 0.05),
		&Decision{Verdict: NotSignificant})

	assertNotEligible(t, a, EvidenceL1)
	reasonsContain(t, a, ReasonDirectionNotSignificant)

	if len(a.RejectionReasons) != 1 {
		t.Errorf("噪声合格时不应出现噪声类原因码，实际 %v", a.RejectionReasons)
	}
}

// Worse 与 NotSignificant 同理：不合格且原因是方向。
func TestAdmit_Worse_PointsToDirection(t *testing.T) {
	a := Admit(side("baseline", 0.04), side("candidate", 0.05),
		&Decision{Verdict: Worse})

	assertNotEligible(t, a, EvidenceL1)
	reasonsContain(t, a, ReasonDirectionWorse)
	if len(a.RejectionReasons) != 1 {
		t.Errorf("实际 %v", a.RejectionReasons)
	}
}

// 验收条件 5：只标定未测量 → decision 为 nil，不得出现方向判定值，
// 且原因码是「未测量」而不是「不显著」—— 这两种证据状态不能混淆。
func TestAdmit_NoMeasurement_NoDirectionVerdict(t *testing.T) {
	a := Admit(side("baseline", 0.04), side("candidate", 0.13), nil)

	assertNotEligible(t, a, EvidenceL1)
	if a.DirectionVerdict != nil {
		t.Errorf("未测量时 direction_verdict 必须缺失，实际 %s", *a.DirectionVerdict)
	}
	reasonsContain(t, a, ReasonNoMeasurement)

	for _, r := range a.RejectionReasons {
		if r == ReasonDirectionNotSignificant {
			t.Errorf("未测量不是 NotSignificant，不得出现该原因码")
		}
	}
}

// 验收条件 6：门槛数值随报告携带，读者无需翻代码。
func TestAdmit_ThresholdsRecorded(t *testing.T) {
	a := Admit(side("baseline", 0.04), side("candidate", 0.05), nil)

	th := a.Thresholds
	if th.NoiseRatioLimit != NoiseRatioLimit {
		t.Errorf("准入线应记录 %.2f，实际 %v", NoiseRatioLimit, th.NoiseRatioLimit)
	}
	if th.SignificanceMultiplier != SignificanceMultiplier {
		t.Errorf("门槛倍数应记录 %.1f，实际 %v", SignificanceMultiplier, th.SignificanceMultiplier)
	}
}

// 多个原因码的输出顺序必须固定 —— 顺序不稳定会让下游对报告的 diff 失真。
func TestAdmit_RejectionReasons_OrderIsStable(t *testing.T) {
	a := Admit(side("baseline", 0.50), side("candidate", 0.60), nil)

	want := []RejectionReason{
		ReasonNoMeasurement,
		ReasonBaselineNoiseExceedsLimit,
		ReasonCandidateNoiseExceedsLimit,
	}
	if len(a.RejectionReasons) != len(want) {
		t.Fatalf("原因码数量 = %d，期望 %d: %v", len(a.RejectionReasons), len(want), a.RejectionReasons)
	}
	for i := range want {
		if a.RejectionReasons[i] != want[i] {
			t.Errorf("顺序第 %d 位 = %s，期望 %s", i, a.RejectionReasons[i], want[i])
		}
	}
}

// 临界值语义：恰好等于准入线不算超（规格写的是「超过」）。
func TestAdmit_AtExactLimit_IsQualified(t *testing.T) {
	a := Admit(side("baseline", NoiseRatioLimit), side("candidate", NoiseRatioLimit/2),
		&Decision{Verdict: Better})

	if !a.EligibleForPerformanceClaim {
		t.Errorf("噪声比恰好等于准入线（%.2f）不应算超出", NoiseRatioLimit)
	}
}
