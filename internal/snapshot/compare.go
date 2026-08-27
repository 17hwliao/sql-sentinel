package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DefaultChunk 是单次查询取回的行数。分块是为了避免一次把整表读进内存，
// 用主键 keyset 分页而不是 OFFSET —— OFFSET 在大表上是 O(n²)。
const DefaultChunk = 5000

const selectChunk = `SELECT id, user_id, status, created_at, amount_cents, note, payload
FROM orders WHERE id > ? ORDER BY id LIMIT ?`

// Side 是单个实例的摘要结果。
type Side struct {
	Name    string
	Digest  string
	Rows    int64
	Elapsed time.Duration
}

// Result 是两侧的比对结论。
type Result struct {
	Baseline  Side
	Candidate Side
	Match     bool
}

// Compute 按主键升序分块读取 orders，逐行规范化后增量 SHA-256。
func Compute(ctx context.Context, db *sql.DB, name string, chunk int) (Side, error) {
	if chunk <= 0 {
		chunk = DefaultChunk
	}
	start := time.Now()
	h := NewHasher()

	var lastID uint64
	for {
		n, next, err := scanChunk(ctx, db, h, lastID, chunk)
		if err != nil {
			return Side{}, fmt.Errorf("[%s] 读取快照失败: %w", name, err)
		}
		lastID = next
		if n < chunk {
			break
		}
	}

	digest, rows := h.Sum()
	return Side{Name: name, Digest: digest, Rows: rows, Elapsed: time.Since(start)}, nil
}

// scanChunk 读一块并喂进 hasher，返回本块行数与新的游标。
// 单独成函数是为了让 rows.Close 在每块结束时确定发生 —— 连接池只有一条连接，
// 未关闭的 rows 会让下一次查询直接阻塞。
func scanChunk(ctx context.Context, db *sql.DB, h *Hasher, lastID uint64, chunk int) (int, uint64, error) {
	rows, err := db.QueryContext(ctx, selectChunk, lastID, chunk)
	if err != nil {
		return 0, lastID, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.UserID, &r.Status, &r.CreatedAt,
			&r.AmountCents, &r.Note, &r.Payload); err != nil {
			return n, lastID, err
		}
		h.Add(r)
		lastID = r.ID
		n++
	}
	if err := rows.Err(); err != nil {
		return n, lastID, err
	}
	return n, lastID, nil
}

// Compare 比对两侧摘要。摘要相同且行数相同才算一致。
func Compare(baseline, candidate Side) Result {
	match := baseline.Digest == candidate.Digest && baseline.Rows == candidate.Rows
	return Result{Baseline: baseline, Candidate: candidate, Match: match}
}
