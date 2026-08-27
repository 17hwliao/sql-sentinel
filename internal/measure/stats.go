package measure

import (
	"slices"
	"time"
)

// MADScale 让 MAD 成为正态分布下标准差的一致估计量。
// 1 / Φ⁻¹(0.75) ≈ 1.4826。漏掉这个系数会把噪声低估约 33%，
// 于是把纯噪声当成真实收益 —— 这是整套判定里最容易出错也最致命的一处。
const MADScale = 1.4826

// Median 返回中位数。偶数个样本取中间两个的均值。
// 用中位数而不是均值：单次 GC、调度抖动、Docker Desktop 的偶发停顿
// 会把均值拉走，中位数对这类离群点不敏感。
func Median(xs []time.Duration) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)

	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// MAD 返回中位绝对偏差 median(|x - median(x)|)，未缩放。
func MAD(xs []time.Duration) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	med := Median(xs)

	dev := make([]time.Duration, len(xs))
	for i, x := range xs {
		d := x - med
		if d < 0 {
			d = -d
		}
		dev[i] = d
	}
	return Median(dev)
}

// ScaledMAD 返回 MAD × 1.4826。
func ScaledMAD(xs []time.Duration) time.Duration {
	return time.Duration(float64(MAD(xs)) * MADScale)
}

// NoiseRatio 返回 scaled MAD 占中位数的比例，即该侧自身的相对噪声水平。
// 每侧各算一份，不存在跨侧或跨 Case 共用的全局 noise floor。
func NoiseRatio(xs []time.Duration) float64 {
	med := Median(xs)
	if med <= 0 {
		return 0
	}
	return float64(ScaledMAD(xs)) / float64(med)
}
