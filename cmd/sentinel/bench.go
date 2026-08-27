package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"sqlsentinel/internal/dataset"
	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/report"
	"sqlsentinel/internal/snapshot"
)

// dbSnapshotProbe 用真实数据库实现门禁的快照探测。
type dbSnapshotProbe struct {
	dbs   map[string]*sql.DB
	chunk int
}

func (p dbSnapshotProbe) Snapshot(ctx context.Context, side string) (measure.SideSnapshot, error) {
	db, ok := p.dbs[side]
	if !ok {
		return measure.SideSnapshot{}, fmt.Errorf("未知实例: %s", side)
	}
	s, err := snapshot.Compute(ctx, db, side, p.chunk)
	if err != nil {
		return measure.SideSnapshot{}, err
	}
	return measure.SideSnapshot{Name: s.Name, Digest: s.Digest, Rows: s.Rows}, nil
}

// dbIndexProbe 用真实数据库实现候选索引探测。
type dbIndexProbe struct{ db *sql.DB }

func (p dbIndexProbe) HasCandidateIndex(ctx context.Context) (bool, error) {
	return dataset.HasIndex(ctx, p.db, dataset.CandidateIndexName)
}

// dbVersionProbe 现场查询两侧服务端版本。
type dbVersionProbe struct{ dbs map[string]*sql.DB }

func (p dbVersionProbe) ServerVersion(ctx context.Context, side string) (string, error) {
	db, ok := p.dbs[side]
	if !ok {
		return "", fmt.Errorf("未知实例: %s", side)
	}
	var v string
	if err := db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// openBoth 打开两侧连接，任一失败即全部关闭。
func openBoth(ctx context.Context) (map[string]*sql.DB, func(), error) {
	dbs := map[string]*sql.DB{}
	closeAll := func() {
		for _, db := range dbs {
			db.Close()
		}
	}
	for _, name := range []string{"baseline", "candidate"} {
		db, err := open(ctx, name)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		dbs[name] = db
	}
	return dbs, closeAll, nil
}

func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	calibrate := fs.Int("calibrate", 20, "每侧独立标定轮数")
	rounds := fs.Int("rounds", 5, "正式测量轮数（每侧）；0 表示只标定不判定")
	warmup := fs.Int("warmup", 3, "每侧预热轮数，结果丢弃")
	executionsPerRound := fs.Int("executions-per-round", 1, "每轮完整执行 SQL 的次数，计时后折算为单次耗时")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "门禁重算快照时的分块大小")
	seed := fs.Uint64("seed", 42, "数据集种子，仅记入报告的复现信息")
	out := fs.String("out", "validation_result.json", "JSON 报告输出路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *calibrate < 2 {
		return fmt.Errorf("--calibrate 至少 2 轮才能算出离散程度，收到 %d", *calibrate)
	}
	if *rounds < 0 || *warmup < 0 || *executionsPerRound <= 0 {
		return fmt.Errorf("--rounds / --warmup 不能为负，--executions-per-round 必须为正数")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	dbs, closeAll, err := openBoth(ctx)
	if err != nil {
		return err
	}
	defer closeAll()

	// ---- 门禁：版本 → 快照 → 候选索引 ----
	// 版本检查放最前面：它最便宜，而且版本不同的话后面所有比对都失去意义。
	fmt.Println("门禁: 查询两侧 MySQL 版本…")
	mysqlVersion, err := measure.RequireSameServerVersion(ctx, dbVersionProbe{dbs: dbs})
	if err != nil {
		return err
	}
	fmt.Printf("门禁: 两侧版本一致 MySQL %s\n", mysqlVersion)

	fmt.Println("门禁: 现场重算两侧快照…")
	gate, err := measure.RequireIdenticalSnapshots(ctx, dbSnapshotProbe{dbs: dbs, chunk: *chunk})
	if err != nil {
		return err
	}
	fmt.Printf("门禁: 快照一致 rows=%d digest=%s\n", gate.Rows(), gate.Digest()[:16])

	if err := measure.RequireCandidateIndex(ctx, dbIndexProbe{db: dbs["candidate"]}); err != nil {
		return err
	}
	fmt.Printf("门禁: candidate 候选索引 %s 存在\n\n", dataset.CandidateIndexName)

	// ---- 建立两侧会话 ----
	qc := measure.DefaultCase()
	sessions := map[string]*measure.Session{}
	for _, name := range []string{"baseline", "candidate"} {
		s, err := measure.NewSession(ctx, dbs[name], qc)
		if err != nil {
			return fmt.Errorf("[%s] %w", name, err)
		}
		defer s.Close()
		sessions[name] = s

		if err := s.Warmup(ctx, *warmup, *executionsPerRound); err != nil {
			return fmt.Errorf("[%s] %w", name, err)
		}
	}
	fmt.Printf("预热完成: 每侧 %d 轮\n", *warmup)

	// ---- 标定：B→C 逐轮交替采样，两侧统计各自独立计算 ----
	// 交替采样是为了让两侧经历同一段时间窗口：若先连续测完 baseline 再测 candidate，
	// 这段时间里的后台负载、温度、缓存状态变化会整体计到某一侧头上，
	// 算出来的「该侧噪声」其实混进了时间漂移。
	// 但统计仍严格按侧分开：raw samples 分别保存，NoiseRatio 分别计算，绝不混合。
	calRaw := map[string][]time.Duration{
		"baseline":  make([]time.Duration, 0, *calibrate),
		"candidate": make([]time.Duration, 0, *calibrate),
	}
	for i := 0; i < *calibrate; i++ {
		for _, name := range []string{"baseline", "candidate"} {
			d, err := sessions[name].Once(ctx, *executionsPerRound)
			if err != nil {
				return fmt.Errorf("[%s] 标定第 %d 轮失败: %w", name, i+1, err)
			}
			calRaw[name] = append(calRaw[name], d)
		}
	}
	for _, name := range []string{"baseline", "candidate"} {
		fmt.Printf("标定 [%-9s] %d 轮 噪声比 %.2f%%\n",
			name, len(calRaw[name]), measure.NoiseRatio(calRaw[name])*100)
	}

	// ---- 正式测量：B → C → B → C 逐轮交替 ----
	// 交替是为了抵消时间漂移（后台任务、温度、缓存状态随时间变化）。
	// 先测完一侧再测另一侧，会把这段时间里的环境变化整体记到某一侧头上。
	measRaw := map[string][]time.Duration{
		"baseline":  make([]time.Duration, 0, *rounds),
		"candidate": make([]time.Duration, 0, *rounds),
	}
	for i := 0; i < *rounds; i++ {
		for _, name := range []string{"baseline", "candidate"} {
			d, err := sessions[name].Once(ctx, *executionsPerRound)
			if err != nil {
				return fmt.Errorf("[%s] 第 %d 轮测量失败: %w", name, i+1, err)
			}
			measRaw[name] = append(measRaw[name], d)
		}
	}
	if *rounds > 0 {
		fmt.Printf("测量完成: 交替 %d 轮 × 2 侧\n", *rounds)
	}

	// ---- 组装唯一的结果对象 ----
	res := buildResult(*seed, mysqlVersion, gate, qc, calRaw, measRaw, *rounds, *executionsPerRound)

	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("创建报告文件失败: %w", err)
	}
	if err := report.WriteJSON(f, res); err != nil {
		f.Close()
		return fmt.Errorf("写报告失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}

	report.WriteSummary(os.Stdout, res)
	fmt.Printf("\nJSON 报告: %s\n", *out)
	return nil
}

// buildResult 组装报告。所有统计量在此算一次，JSON 与终端摘要都只读这一份结果。
func buildResult(
	seed uint64,
	mysqlVersion string,
	gate measure.SnapshotGate,
	qc measure.QueryCase,
	calRaw, measRaw map[string][]time.Duration,
	rounds int,
	executionsPerRound int,
) report.Result {
	calB := measure.Summarize("baseline", calRaw["baseline"])
	calC := measure.Summarize("candidate", calRaw["candidate"])

	args := make([]string, len(qc.Args))
	for i, a := range qc.Args {
		args[i] = fmt.Sprint(a)
	}

	res := report.Result{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Env: report.EnvInfo{
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			GoVersion: runtime.Version(),
			MySQL:     mysqlVersion,
		},
		Dataset: report.DatasetInfo{
			Seed:                seed,
			Rows:                gate.Rows(),
			DatasetVersion:      dataset.Version(seed, gate.Rows()),
			SchemaVersion:       dataset.SchemaVersion,
			GeneratorVersion:    dataset.GeneratorVersion,
			DistributionVersion: dataset.DistributionVersion,
		},
		Snapshot: report.SnapshotInfo{
			Digest: gate.Digest(),
			Rows:   gate.Rows(),
			Match:  true, // 门禁不通过时不会走到这里
		},
		Case:                report.CaseInfo{Name: qc.Name, SQL: qc.SQL, Args: args},
		CandidateIndex:      dataset.CandidateIndexName,
		MeasurementProtocol: report.MeasurementProtocol{ExecutionsPerRound: executionsPerRound},
		Calibration: report.Phase{
			Mode:      fmt.Sprintf("标定（B→C 交替 %d 轮，统计按侧独立计算）", len(calRaw["baseline"])),
			Baseline:  report.NewSide(calB, calRaw["baseline"]),
			Candidate: report.NewSide(calC, calRaw["candidate"]),
		},
	}

	// rounds 为 0 时只有标定，没有测量就没有结论 ——
	// 不能给一个默认的 NotSignificant 冒充结果。
	if rounds == 0 {
		res.Admission = report.NewAdmission(measure.Admit(calB, calC, nil))
		return res
	}

	mB := measure.Summarize("baseline", measRaw["baseline"])
	mC := measure.Summarize("candidate", measRaw["candidate"])

	// 判定用标定阶段的噪声比作门槛基数：标定轮数更多，噪声估计更稳；
	// 正式测量只有几轮，用它自己算噪声会把门槛算得忽大忽小。
	decision := measure.Decide(
		measure.SideStats{Name: mB.Name, Samples: mB.Samples, Median: mB.Median,
			ScaledMAD: mB.ScaledMAD, NoiseRatio: calB.NoiseRatio},
		measure.SideStats{Name: mC.Name, Samples: mC.Samples, Median: mC.Median,
			ScaledMAD: mC.ScaledMAD, NoiseRatio: calC.NoiseRatio},
	)

	v := report.NewVerdict(decision)
	res.Measurement = &report.Phase{
		Mode:      fmt.Sprintf("正式测量（B→C 交替 %d 轮）", rounds),
		Baseline:  report.NewSide(mB, measRaw["baseline"]),
		Candidate: report.NewSide(mC, measRaw["candidate"]),
	}
	res.Verdict = &v

	// 证据准入与方向判定共用标定噪声作为基数，两个结论才不会各说各话。
	res.Admission = report.NewAdmission(measure.Admit(calB, calC, &decision))
	return res
}
