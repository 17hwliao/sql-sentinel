package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"sqlsentinel/internal/measure"
)

func noisySide(name string, ratio float64) measure.SideStats {
	return measure.SideStats{Name: name, Samples: 20, NoiseRatio: ratio}
}

// build 用给定噪声与判定构造完整报告，模拟「噪声超标 + Better」的关键组合。
func build(baseNoise, candNoise float64, verdict measure.Verdict) Result {
	d := measure.Decision{Verdict: verdict}
	res := Result{GeneratedAt: "t", MeasurementProtocol: MeasurementProtocol{ExecutionsPerRound: 1}}
	res.Admission = NewAdmission(measure.Admit(
		noisySide("baseline", baseNoise),
		noisySide("candidate", candNoise), &d))
	return res
}

// 风险：报告只保留折算后的 raw_ms，却不记录每轮执行次数，消费者会误读时间口径。
func TestResultJSON_RecordsExecutionsPerRound(t *testing.T) {
	var b bytes.Buffer
	res := Result{GeneratedAt: "t", MeasurementProtocol: MeasurementProtocol{ExecutionsPerRound: 10}}
	if err := WriteJSON(&b, res); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Protocol struct {
			ExecutionsPerRound int `json:"executions_per_round"`
		} `json:"measurement_protocol"`
	}
	if err := json.Unmarshal(b.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Protocol.ExecutionsPerRound != 10 {
		t.Fatalf("executions_per_round=%d, want 10", raw.Protocol.ExecutionsPerRound)
	}
}

// 验收条件 1：JSON 必须同时表达 Better 与不可声称收益 —— 两个字段独立。
// direction_verdict 缺失（未测量）与 NotSignificant 是不同状态，
// 所以用 omitempty + 指针，序列化后必须能区分。
func TestResultJSON_AdmissionFields(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, build(0.0421, 0.1302, measure.Better)); err != nil {
		t.Fatal(err)
	}

	var raw struct {
		Admission struct {
			DirectionVerdict            *string `json:"direction_verdict"`
			EligibleForPerformanceClaim bool    `json:"eligible_for_performance_claim"`
			EvidenceLevel               string  `json:"evidence_level"`
			RejectionReasons            []struct {
				Code string `json:"code"`
			} `json:"rejection_reasons"`
			Thresholds struct {
				NoiseRatioLimit        float64 `json:"noise_ratio_limit"`
				SignificanceMultiplier float64 `json:"significance_multiplier"`
			} `json:"admission_thresholds"`
		} `json:"admission"`
	}
	if err := json.Unmarshal(b.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}

	a := raw.Admission
	if a.DirectionVerdict == nil || *a.DirectionVerdict != "Better" {
		t.Errorf("direction_verdict 应为 Better，实际 %v", a.DirectionVerdict)
	}
	if a.EligibleForPerformanceClaim {
		t.Errorf("eligible_for_performance_claim 应为 false")
	}
	if a.EvidenceLevel != "L1" {
		t.Errorf("evidence_level = %s，期望 L1", a.EvidenceLevel)
	}
	if len(a.RejectionReasons) == 0 || a.RejectionReasons[0].Code != "candidate_noise_ratio_exceeds_limit" {
		t.Errorf("应含 candidate 噪声原因码，实际 %+v", a.RejectionReasons)
	}
	if a.Thresholds.NoiseRatioLimit != 0.10 || a.Thresholds.SignificanceMultiplier != 2.0 {
		t.Errorf("门槛数值应随报告携带，实际 %+v", a.Thresholds)
	}
}

// 验收条件 5：只标定未测量时 direction_verdict 必须缺失而非 null 值语义混淆。
func TestResultJSON_NoMeasurement_OmitsDirection(t *testing.T) {
	var b bytes.Buffer
	res := Result{GeneratedAt: "t"}
	res.Admission = NewAdmission(measure.Admit(
		noisySide("baseline", 0.04), noisySide("candidate", 0.13), nil))
	if err := WriteJSON(&b, res); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `"direction_verdict"`) {
		t.Errorf("未测量时不得出现 direction_verdict 字段")
	}
}

// 验收条件 7：终端摘要里方向性结果与收益资格分两行，不允许收益时给出原因。
func TestSummary_SeparatesDirectionAndEligibility(t *testing.T) {
	var b bytes.Buffer
	WriteSummary(&b, build(0.0421, 0.1302, measure.Better))
	out := b.String()

	for _, want := range []string{"方向性结果", "允许声称性能收益", "candidate_noise_ratio_exceeds_limit", "每轮完整执行 1 次"} {
		if !strings.Contains(out, want) {
			t.Errorf("摘要缺少 %q\n输出:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "否") && !strings.Contains(out, "false") {
		t.Errorf("摘要应明确表示不可声称收益\n%s", out)
	}
}
