package dataset

import (
	"context"
	"database/sql"
	"fmt"
)

// SchemaVersion 随建表 DDL 变化递增。参与 dataset_version 计算。
const SchemaVersion = "orders-v1"

// ordersDDL 是两侧执行的**完全相同**的建表语句。
// baseline 初始化后不再执行任何 schema 变更或新增二级索引的 DDL；
// candidate 仅在 verify 通过后新增候选二级索引。
const ordersDDL = `
CREATE TABLE IF NOT EXISTS orders (
  id           BIGINT UNSIGNED NOT NULL,
  user_id      BIGINT UNSIGNED NOT NULL,
  status       VARCHAR(16)     NOT NULL,
  created_at   DATETIME(6)     NOT NULL,
  amount_cents BIGINT          NOT NULL,
  note         VARCHAR(64)         NULL,
  payload      VARCHAR(255)    NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`

// EnsureSchema 幂等建表，重复调用不报错。
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, ordersDDL); err != nil {
		return fmt.Errorf("建表失败: %w", err)
	}
	return nil
}

// Truncate 清空 orders，使同一 seed 的重复造数得到一致结果。
func Truncate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE orders`); err != nil {
		return fmt.Errorf("清表失败: %w", err)
	}
	return nil
}

// CountRows 返回 orders 当前行数。
func CountRows(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计行数失败: %w", err)
	}
	return n, nil
}

// ShowCreateTable 返回 orders 的建表语句，用于确认两侧结构一致。
func ShowCreateTable(ctx context.Context, db *sql.DB) (string, error) {
	var name, ddl string
	err := db.QueryRowContext(ctx, `SHOW CREATE TABLE orders`).Scan(&name, &ddl)
	if err != nil {
		return "", fmt.Errorf("读取建表语句失败: %w", err)
	}
	return ddl, nil
}
