package dataset

import (
	"database/sql"
	"encoding/hex"
	"math"
	"math/rand/v2"
	"sort"
	"time"
)

// GeneratorVersion 随行生成算法或批写顺序规则变化递增。
const GeneratorVersion = "gen-v1"

// DistributionVersion 随任何影响数据分布的参数变化递增。
// 分布参数变了却不改这个值，会让不同分布共用同一个 dataset_version。
const DistributionVersion = "dist-v1"

// baseTime 是所有 created_at 的固定 UTC 基准。绝不使用 time.Now()，
// 否则同一 seed 在两侧、在不同时刻会产出不同数据。
var baseTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// statusWeights 是 status 的低基数偏斜分布，顺序与权重都参与 DistributionVersion。
var statusWeights = []struct {
	Value  string
	Weight float64
}{
	{"paid", 0.55},
	{"pending", 0.20},
	{"shipped", 0.15},
	{"cancelled", 0.07},
	{"refunded", 0.03},
}

// Params 是造数的全部输入。相同 Params 必须产出完全相同的数据。
type Params struct {
	Seed          uint64
	Rows          int
	UserCount     int     // user_id 取值上界，构成高基数列
	ZipfS         float64 // Zipf 倾斜指数，越大热点越集中
	NoteNullRatio float64 // note 为 NULL 的比例
	StepSeconds   int64   // created_at 的基础递增步长
	JitterSeconds int64   // created_at 的抖动上界
}

// DefaultParams 是本切片的默认分布。改动任何一项都必须递增 DistributionVersion。
func DefaultParams() Params {
	return Params{
		UserCount:     50000,
		ZipfS:         1.1,
		NoteNullRatio: 0.10,
		StepSeconds:   2,
		JitterSeconds: 3600,
	}
}

// Row 是 orders 的一行。Note.Valid 为 false 表示 SQL NULL。
// 全部字段都是可比较的值类型，因此可以直接用 == 判断两次生成是否一致 ——
// 用 *string 会让比较退化成比指针，确定性测试会变成假通过。
type Row struct {
	ID          uint64
	UserID      uint64
	Status      string
	CreatedAt   time.Time
	AmountCents int64
	Note        sql.NullString
	Payload     string
}

// Generator 按 (Seed, rowIndex) 确定性地产出行。
// 不持有任何随时间或调用次序变化的状态，因此两侧各自生成即可得到同一份数据，
// 无需 dump/restore 同步快照 —— 这是本切片最关键的简化。
type Generator struct {
	p       Params
	zipfCDF []float64
	statCDF []float64
}

func NewGenerator(p Params) *Generator {
	zipf := make([]float64, p.UserCount)
	sum := 0.0
	for k := 1; k <= p.UserCount; k++ {
		sum += 1.0 / math.Pow(float64(k), p.ZipfS)
		zipf[k-1] = sum
	}
	for i := range zipf {
		zipf[i] /= sum
	}

	stat := make([]float64, len(statusWeights))
	acc := 0.0
	for i, sw := range statusWeights {
		acc += sw.Weight
		stat[i] = acc
	}
	for i := range stat {
		stat[i] /= acc
	}

	return &Generator{p: p, zipfCDF: zipf, statCDF: stat}
}

// Row 生成第 i 行（i 从 0 起）。抽取顺序固定，改动顺序即改变数据，
// 因此调整下面任何一行都必须递增 GeneratorVersion。
func (g *Generator) Row(i int) Row {
	r := rand.New(rand.NewPCG(g.p.Seed, uint64(i)))

	userID := uint64(pick(g.zipfCDF, r.Float64()) + 1)
	status := statusWeights[pick(g.statCDF, r.Float64())].Value
	jitter := time.Duration(r.Int64N(g.p.JitterSeconds)) * time.Second
	createdAt := baseTime.Add(time.Duration(int64(i)*g.p.StepSeconds)*time.Second + jitter)
	amount := 100 + r.Int64N(20_000_000)

	var note sql.NullString
	if r.Float64() >= g.p.NoteNullRatio {
		note = sql.NullString{
			String: "note-" + hex.EncodeToString(u64bytes(r.Uint64())[:4]),
			Valid:  true,
		}
	}

	payload := hex.EncodeToString(append(u64bytes(r.Uint64()), u64bytes(r.Uint64())...))

	return Row{
		ID:          uint64(i) + 1,
		UserID:      userID,
		Status:      status,
		CreatedAt:   createdAt,
		AmountCents: amount,
		Note:        note,
		Payload:     payload,
	}
}

// pick 在归一化 CDF 上做逆变换抽样，返回下标。
func pick(cdf []float64, u float64) int {
	i := sort.SearchFloat64s(cdf, u)
	if i >= len(cdf) {
		i = len(cdf) - 1
	}
	return i
}

func u64bytes(v uint64) []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
	return b
}
