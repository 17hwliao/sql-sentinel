package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"sqlsentinel/internal/dataset"
)

// 两个影子实例的固定端口。13306/13307 避开本机可能占用的 3306。
var targets = map[string]string{
	"baseline":  "root:sentinel@tcp(127.0.0.1:13306)/sentinel",
	"candidate": "root:sentinel@tcp(127.0.0.1:13307)/sentinel",
}

// dsnOptions 强制会话使用 UTC，否则同一 seed 在不同时区下会写出不同的 created_at。
const dsnOptions = "?parseTime=true&loc=UTC&time_zone=%27%2B00%3A00%27&multiStatements=false"

const usage = `sqlsentinel —— SQL Sentinel 阶段 0：可信 A/B 测量 Spike

用法:
  sentinel <命令> [参数]

命令:
  seed      在指定实例上幂等建表，并按固定 seed 确定性造数
  verify    按主键升序分块比对两侧快照 SHA-256
  index     仅在 candidate 建候选索引并 ANALYZE TABLE（先过快照门禁）
  bench     噪声标定与 A/B 交替测量，输出 JSON 报告（先过快照与索引门禁）
  admit-series  汇总多份标定与正式报告，生成跨运行性能证据结论
  candidate-preview  校验 CandidateSpec JSON 并仅预览候选 DDL，不执行
  sql-admit  校验单条只读 SQL，输出 L0 静态风险信号，不连接数据库
  candidate-explain  仅在 candidate 影子库应用受限索引并输出 L1 EXPLAIN 报告
  explain-compare  对照 baseline/candidate EXPLAIN，输出 L1 结构化计划差异
  diagnose-plan  从 L1 EXPLAIN 对照报告生成待验证诊断假设
  bind-evidence  精确绑定诊断、对照、测量与系列证据
  pipeline  串联只读准入、影子 EXPLAIN、计划对照与诊断并输出溯源工件
  propose-candidate  用受限 Eino 图从管线证据提出并严格校验 CandidateSpec
  webhook-serve  仅监听本机的已签名模拟 PR 评审服务
  evaluate  生成最终验收评测报告并可重跑固定安全测试

seed 参数:
  --rows N        写入行数。0 表示只连接并幂等建表，不写数据（默认 0）
  --seed N        随机种子，决定数据内容（默认 42）
  --target NAME   baseline | candidate | both（默认 both）

verify 参数:
  --seed N        本次数据所用的种子，仅记录在输出中（默认 42）
  --chunk N       单次查询取回行数（默认 5000）

index 参数:
  --target NAME   只能是 candidate；baseline 会被拒绝且不执行任何 DDL
  --chunk N       门禁重算快照时的分块大小（默认 5000）

bench 参数:
  --calibrate N   每侧独立标定轮数（默认 20）
  --rounds N      正式测量轮数，B→C 交替；0 表示只标定不判定（默认 5）
  --warmup N      每侧预热轮数，结果丢弃（默认 3）
  --chunk N       门禁重算快照时的分块大小（默认 5000）
  --seed N        数据集种子，仅记入报告的复现信息（默认 42）
  --out PATH      JSON 报告输出路径（默认 validation_result.json）

sql-admit 参数:
  --in PATH       单条只读 SQL 文件（必填）

candidate-explain 参数:
  --sql PATH        单条已准入 SQL 文件（必填）
  --candidate PATH  CandidateSpec JSON 文件（必填）
  --out PATH        EXPLAIN JSON 报告输出路径（必填）

explain-compare 参数:
  --sql PATH        单条已准入 SQL 文件（必填）
  --candidate PATH  CandidateSpec JSON 文件（必填）
  --out PATH        EXPLAIN 对照 JSON 报告输出路径（必填）

diagnose-plan 参数:
  --in PATH   L1 EXPLAIN 对照 JSON 报告（必填）
  --out PATH  诊断假设 JSON 报告（必填）

bind-evidence 参数:
  --diagnosis PATH   诊断假设 JSON 报告（必填）
  --comparison PATH  EXPLAIN 对照 JSON 报告（必填）
  --measurement PATH bench 正式测量 JSON 报告（必填）
  --series PATH      系列准入 JSON 报告（必填）
  --out PATH         证据绑定 JSON 报告（必填）

pipeline 参数:
  --sql PATH        单条只读 SQL 文件（必填）
  --candidate PATH  CandidateSpec JSON 文件（必填）
  --out-dir PATH    新建或空的工件输出目录（必填）
  --chunk N         快照门禁分块大小（默认 5000）

propose-candidate 参数:
  --provider NAME       仅支持 openai；真实调用时必填
  --evidence-dir PATH   含四份管线证据 JSON 的目录（必填）
  --out PATH            CandidateSpec 提案报告 JSON（必填）
  --max-attempts N      不合格 CandidateSpec 的最大重试次数，1-3（默认 2）

webhook-serve 参数:
  --listen ADDR         仅允许 127.0.0.1 host:port（默认 127.0.0.1:8080）
  --candidate PATH      服务器本地 CandidateSpec JSON（必填）
  --out-dir PATH        delivery 评论与工件根目录（必填）
  --max-concurrent N    同时影子验证数，1-4（默认 1）
  --chunk N             快照门禁分块大小（默认 5000）

evaluate 参数:
  --manifest PATH          评测样例 manifest（默认 examples/evaluation/manifest.json）
  --validation PATH        历史测量 JSON（默认 validation_result.json）
  --pipeline-report PATH   本次 smoke 的 pipeline_report.json（可选）
  --out PATH               评测报告 JSON（默认 evaluation_report.json）
  --run-security-tests     重跑固定安全测试（默认 true）

先启动容器:
  docker compose -f deployments/docker-compose.yml up -d
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
	case "seed":
		if err := runSeed(args); err != nil {
			fatal(err)
		}
	case "verify":
		if err := runVerify(args); err != nil {
			fatal(err)
		}
	case "index":
		if err := runIndex(args); err != nil {
			fatal(err)
		}
	case "bench":
		if err := runBench(args); err != nil {
			fatal(err)
		}
	case "admit-series":
		if err := runAdmitSeries(args); err != nil {
			fatal(err)
		}
	case "candidate-preview":
		if err := runCandidatePreview(args); err != nil {
			fatal(err)
		}
	case "sql-admit":
		if err := runSQLAdmit(args); err != nil {
			fatal(err)
		}
	case "candidate-explain":
		if err := runCandidateExplain(args); err != nil {
			fatal(err)
		}
	case "explain-compare":
		if err := runExplainCompare(args); err != nil {
			fatal(err)
		}
	case "diagnose-plan":
		if err := runDiagnosePlan(args); err != nil {
			fatal(err)
		}
	case "bind-evidence":
		if err := runBindEvidence(args); err != nil {
			fatal(err)
		}
	case "pipeline":
		if err := runPipeline(args); err != nil {
			fatal(err)
		}
	case "propose-candidate":
		if err := runProposeCandidate(args); err != nil {
			fatal(err)
		}
	case "webhook-serve":
		if err := runWebhookServe(args); err != nil {
			fatal(err)
		}
	case "evaluate":
		if err := runEvaluate(args); err != nil {
			fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	os.Exit(1)
}

func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	rows := fs.Int("rows", 0, "写入行数；0 表示只建表不写数据")
	seed := fs.Uint64("seed", 42, "随机种子")
	target := fs.String("target", "both", "baseline | candidate | both")
	if err := fs.Parse(args); err != nil {
		return err
	}

	names, err := resolveTargets(*target)
	if err != nil {
		return err
	}
	if *rows < 0 {
		return errors.New("--rows 不能为负数")
	}

	params := dataset.DefaultParams()
	params.Seed = *seed
	params.Rows = *rows

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fmt.Printf("seed=%d rows=%d target=%v\n", params.Seed, params.Rows, names)
	fmt.Printf("版本: schema=%s generator=%s distribution=%s\n\n",
		dataset.SchemaVersion, dataset.GeneratorVersion, dataset.DistributionVersion)

	// 严格串行处理两侧，避免并发写入争抢 CPU/IO。
	for _, name := range names {
		if err := seedOne(ctx, name, params); err != nil {
			return fmt.Errorf("[%s] %w", name, err)
		}
	}
	return nil
}

func seedOne(ctx context.Context, name string, params dataset.Params) error {
	db, err := open(ctx, name)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := dataset.EnsureSchema(ctx, db); err != nil {
		return err
	}
	fmt.Printf("[%s] 已连接，orders 建表幂等完成\n", name)

	if params.Rows == 0 {
		n, err := dataset.CountRows(ctx, db)
		if err != nil {
			return err
		}
		fmt.Printf("[%s] 未写入数据（--rows 0），当前行数 %d\n", name, n)
		return nil
	}

	if err := dataset.Truncate(ctx, db); err != nil {
		return err
	}

	start := time.Now()
	step := max(params.Rows/10, dataset.BatchRows)
	next := step
	err = dataset.Write(ctx, db, params, func(done int) {
		if done >= next || done == params.Rows {
			fmt.Printf("[%s] 已写入 %d/%d\n", name, done, params.Rows)
			next += step
		}
	})
	if err != nil {
		return err
	}

	n, err := dataset.CountRows(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("[%s] 完成: 行数 %d，耗时 %s\n", name, n, time.Since(start).Round(time.Millisecond))
	if int64(params.Rows) != n {
		return fmt.Errorf("行数不符: 期望 %d 实际 %d", params.Rows, n)
	}
	return nil
}

// open 建连并等待实例就绪。单连接：造数与测量都要求串行，不需要连接池。
func open(ctx context.Context, name string) (*sql.DB, error) {
	db, err := sql.Open("mysql", targets[name]+dsnOptions)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	deadline := time.Now().Add(90 * time.Second)
	for {
		if err = db.PingContext(ctx); err == nil {
			return db, nil
		}
		if time.Now().After(deadline) {
			db.Close()
			return nil, fmt.Errorf("连接超时（容器是否已 healthy？）: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func resolveTargets(target string) ([]string, error) {
	switch target {
	case "both":
		return []string{"baseline", "candidate"}, nil
	case "baseline", "candidate":
		return []string{target}, nil
	default:
		return nil, fmt.Errorf("--target 只能是 baseline | candidate | both，收到 %q", target)
	}
}
