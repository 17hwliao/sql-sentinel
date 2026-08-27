package dataset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CandidateIndexName 是本切片唯一的候选索引。命名带 cand_ 前缀，
// 便于在两侧索引清单里一眼分辨它是候选物而非基线结构。
const CandidateIndexName = "idx_cand_user_status_created"

// 列顺序对应目标 SQL 的 WHERE user_id = ? AND status = ? AND created_at >= ?：
// 两个等值列在前、范围列在后，才能让范围条件也走索引。
const candidateIndexDDL = `CREATE INDEX ` + CandidateIndexName +
	` ON orders (user_id, status, created_at)`

// ErrBaselineImmutable 表示试图在 baseline 上做结构变更。
// baseline 初始化后只保留主键，任何新增索引都会让它不再是「基线」，A/B 随之失去意义。
var ErrBaselineImmutable = errors.New("baseline 初始化后不得执行任何新增索引或变更 schema 的 DDL")

// CreateCandidateIndex 建候选索引。已存在则跳过，重复调用安全。
func CreateCandidateIndex(ctx context.Context, db *sql.DB) (created bool, err error) {
	exists, err := HasIndex(ctx, db, CandidateIndexName)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if _, err := db.ExecContext(ctx, candidateIndexDDL); err != nil {
		return false, fmt.Errorf("建候选索引失败: %w", err)
	}
	return true, nil
}

// Analyze 重算统计信息。新建索引后不 ANALYZE，优化器可能仍按旧基数选执行计划，
// 让「有索引却没变快」的假阴性出现。
func Analyze(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `ANALYZE TABLE orders`); err != nil {
		return fmt.Errorf("ANALYZE TABLE 失败: %w", err)
	}
	return nil
}

// HasIndex 判断 orders 上是否存在指定索引。
func HasIndex(ctx context.Context, db *sql.DB, name string) (bool, error) {
	const q = `SELECT COUNT(*) FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'orders' AND INDEX_NAME = ?`
	var n int
	if err := db.QueryRowContext(ctx, q, name).Scan(&n); err != nil {
		return false, fmt.Errorf("查询索引失败: %w", err)
	}
	return n > 0, nil
}

// ListIndexes 返回 orders 上的索引名，按名称排序，用于两侧结构对照。
func ListIndexes(ctx context.Context, db *sql.DB) ([]string, error) {
	const q = `SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'orders' ORDER BY INDEX_NAME`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("列出索引失败: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
