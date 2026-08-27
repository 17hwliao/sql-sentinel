package dataset

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// BatchRows 是单条 INSERT 携带的行数。批量多值 INSERT 比逐条快一到两个数量级。
const BatchRows = 1000

// Write 按 params 生成并顺序写入 orders。
// 单连接顺序批写：写入次序不影响行内容（行内容只由 (Seed, rowIndex) 决定），
// 但固定次序能让两侧的物理插入顺序也一致，减少后续快照比对的干扰因素。
func Write(ctx context.Context, db *sql.DB, params Params, progress func(done int)) error {
	if params.Rows <= 0 {
		return nil
	}
	g := NewGenerator(params)

	for start := 0; start < params.Rows; start += BatchRows {
		end := min(start+BatchRows, params.Rows)
		if err := writeBatch(ctx, db, g, start, end); err != nil {
			return fmt.Errorf("写入第 %d–%d 行失败: %w", start+1, end, err)
		}
		if progress != nil {
			progress(end)
		}
	}
	return nil
}

func writeBatch(ctx context.Context, db *sql.DB, g *Generator, start, end int) error {
	n := end - start
	var sb strings.Builder
	sb.WriteString(`INSERT INTO orders (id, user_id, status, created_at, amount_cents, note, payload) VALUES `)
	args := make([]any, 0, n*7)

	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte(',')
		}
		sb.WriteString(`(?,?,?,?,?,?,?)`)
		row := g.Row(i)
		args = append(args, row.ID, row.UserID, row.Status, row.CreatedAt, row.AmountCents, row.Note, row.Payload)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
