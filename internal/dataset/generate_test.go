package dataset

import (
	"testing"
	"time"
)

// 本文件覆盖的风险：造数确定性。
// 本方案不做 dump/restore，两侧各自生成数据后靠 SHA-256 比对；
// 一旦行内容依赖 (seed, rowIndex) 之外的任何东西（时间、时区、调用次序、并发），
// 两侧就会静默产出不同数据，整套 A/B 结论作废。

func testParams(seed uint64) Params {
	p := DefaultParams()
	p.Seed = seed
	return p
}

// 两个独立 Generator 用同一 seed 必须产出逐字段相同的行 —— 这正是两侧各自造数的前提。
func TestRowDependsOnlyOnSeedAndIndex(t *testing.T) {
	a := NewGenerator(testParams(42))
	b := NewGenerator(testParams(42))

	for _, i := range []int{0, 1, 7, 999, 9999} {
		ra, rb := a.Row(i), b.Row(i)
		if ra != rb {
			t.Fatalf("第 %d 行两个 Generator 结果不同:\n  a=%+v\n  b=%+v", i, ra, rb)
		}
	}
}

// 乱序访问不得改变结果：批写顺序、并发切分都不能影响行内容。
func TestRowIsIndependentOfAccessOrder(t *testing.T) {
	g := NewGenerator(testParams(42))

	forward := make([]Row, 100)
	for i := range forward {
		forward[i] = g.Row(i)
	}
	for i := 99; i >= 0; i-- {
		if got := g.Row(i); got != forward[i] {
			t.Fatalf("第 %d 行逆序访问结果不同，Generator 带了跨行状态", i)
		}
	}
}

func TestDifferentSeedProducesDifferentData(t *testing.T) {
	a := NewGenerator(testParams(42))
	b := NewGenerator(testParams(43))

	same := 0
	const n = 200
	for i := 0; i < n; i++ {
		if a.Row(i) == b.Row(i) {
			same++
		}
	}
	if same > n/20 {
		t.Fatalf("seed 42 与 43 有 %d/%d 行完全相同，seed 没有真正参与生成", same, n)
	}
}

// created_at 必须来自固定基准，绝不能取 time.Now()。
func TestCreatedAtDerivesFromFixedBase(t *testing.T) {
	g := NewGenerator(testParams(42))
	p := testParams(42)

	for _, i := range []int{0, 500, 9999} {
		got := g.Row(i).CreatedAt
		lo := baseTime.Add(time.Duration(int64(i)*p.StepSeconds) * time.Second)
		hi := lo.Add(time.Duration(p.JitterSeconds) * time.Second)

		if got.Before(lo) || !got.Before(hi) {
			t.Fatalf("第 %d 行 created_at=%s 落在 [%s, %s) 之外", i, got, lo, hi)
		}
		if got.Location() != time.UTC {
			t.Fatalf("第 %d 行 created_at 时区为 %s，必须是 UTC", i, got.Location())
		}
	}
}

// 分布参数必须真正生效，否则 DistributionVersion 记录的就是假信息。
func TestDistributionMatchesParams(t *testing.T) {
	p := testParams(42)
	p.Rows = 10000
	g := NewGenerator(p)

	nullNotes := 0
	statuses := map[string]int{}
	for i := 0; i < p.Rows; i++ {
		r := g.Row(i)

		if !r.Note.Valid {
			nullNotes++
		}
		statuses[r.Status]++

		if r.UserID < 1 || r.UserID > uint64(p.UserCount) {
			t.Fatalf("第 %d 行 user_id=%d 越界 [1, %d]", i, r.UserID, p.UserCount)
		}
		if r.ID != uint64(i)+1 {
			t.Fatalf("第 %d 行 id=%d，应为 %d", i, r.ID, i+1)
		}
		if r.AmountCents <= 0 {
			t.Fatalf("第 %d 行 amount_cents=%d，应为正整数", i, r.AmountCents)
		}
	}

	ratio := float64(nullNotes) / float64(p.Rows)
	if ratio < p.NoteNullRatio-0.02 || ratio > p.NoteNullRatio+0.02 {
		t.Errorf("note NULL 比例 %.4f 偏离目标 %.2f 超过 2 个百分点", ratio, p.NoteNullRatio)
	}

	if len(statuses) != len(statusWeights) {
		t.Errorf("status 取值数 %d，应为 %d: %v", len(statuses), len(statusWeights), statuses)
	}

	// Zipf 倾斜必须真的产生热点：最热的 user_id 应显著高于均匀分布的期望。
	hottest := 0
	byUser := map[uint64]int{}
	for i := 0; i < p.Rows; i++ {
		u := g.Row(i).UserID
		byUser[u]++
		if byUser[u] > hottest {
			hottest = byUser[u]
		}
	}
	if hottest < 2 {
		t.Errorf("最热 user_id 仅出现 %d 次，未形成热点，Zipf 倾斜没生效", hottest)
	}
}
