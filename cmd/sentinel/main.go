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
