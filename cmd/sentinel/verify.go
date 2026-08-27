package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"sqlsentinel/internal/dataset"
	"sqlsentinel/internal/measure"
	"sqlsentinel/internal/snapshot"
)

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	seed := fs.Uint64("seed", 42, "本次数据所用种子，仅记录在输出中")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "单次查询取回行数")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fmt.Printf("verify seed=%d chunk=%d\n", *seed, *chunk)
	fmt.Printf("版本: schema=%s generator=%s distribution=%s\n\n",
		dataset.SchemaVersion, dataset.GeneratorVersion, dataset.DistributionVersion)

	// 严格串行：两侧同时全表扫描会互相争抢 IO，摘要耗时失去参考价值。
	sides := make([]snapshot.Side, 0, 2)
	for _, name := range []string{"baseline", "candidate"} {
		db, err := open(ctx, name)
		if err != nil {
			return err
		}
		side, err := snapshot.Compute(ctx, db, name, *chunk)
		db.Close()
		if err != nil {
			return err
		}
		fmt.Printf("[%-9s] rows=%-8d sha256=%s  (%s)\n",
			side.Name, side.Rows, side.Digest, side.Elapsed.Round(time.Millisecond))
		sides = append(sides, side)
	}

	res := snapshot.Compare(sides[0], sides[1])
	fmt.Println()
	if !res.Match {
		fmt.Println("判定: 不一致 —— 两侧数据不可比，禁止据此做任何性能结论")
		if res.Baseline.Rows != res.Candidate.Rows {
			fmt.Printf("  行数不同: baseline=%d candidate=%d\n", res.Baseline.Rows, res.Candidate.Rows)
		} else {
			fmt.Println("  行数相同但摘要不同: 存在字段级差异")
		}
		return errors.New("快照不一致")
	}

	fmt.Printf("判定: 一致 —— 两侧 %d 行摘要相同，可进入 index / bench\n", res.Baseline.Rows)
	return nil
}

func runIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	target := fs.String("target", "candidate", "只能是 candidate")
	chunk := fs.Int("chunk", snapshot.DefaultChunk, "门禁重算快照时的分块大小")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 在建连之前就拒绝，确保 baseline 上连一次 DDL 都不会发出。
	if *target != "candidate" {
		return fmt.Errorf("%w（收到 --target %s）", dataset.ErrBaselineImmutable, *target)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	dbs, closeAll, err := openBoth(ctx)
	if err != nil {
		return err
	}
	defer closeAll()

	// 门禁：现场重算两侧快照。建索引本身不改数据，但如果此刻两侧数据已经不同，
	// 建完索引后的测量结论一样无效 —— 与其事后发现，不如在这里就停下。
	fmt.Println("门禁: 现场重算两侧快照…")
	gate, err := measure.RequireIdenticalSnapshots(ctx, dbSnapshotProbe{dbs: dbs, chunk: *chunk})
	if err != nil {
		return err
	}
	fmt.Printf("门禁: 快照一致 rows=%d digest=%s\n", gate.Rows(), gate.Digest()[:16])

	db := dbs["candidate"]

	created, err := dataset.CreateCandidateIndex(ctx, db)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("[candidate] 已创建候选索引 %s (user_id, status, created_at)\n", dataset.CandidateIndexName)
	} else {
		fmt.Printf("[candidate] 候选索引 %s 已存在，跳过创建\n", dataset.CandidateIndexName)
	}

	start := time.Now()
	if err := dataset.Analyze(ctx, db); err != nil {
		return err
	}
	fmt.Printf("[candidate] ANALYZE TABLE orders 完成 (%s)\n", time.Since(start).Round(time.Millisecond))

	names, err := dataset.ListIndexes(ctx, db)
	if err != nil {
		return err
	}
	fmt.Printf("[candidate] 当前索引: %v\n", names)
	return nil
}
