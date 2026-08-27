package measure

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// QueryCase 是被测的固定 SQL 与固定绑定参数。
// SQL 与参数在整场测量中都不变 —— A/B 唯一允许的差异是 candidate 上的候选索引，
// 换 SQL 或换参数都会让延迟差异无法归因。
type QueryCase struct {
	Name string
	SQL  string
	Args []any
}

// defaultSQL 必须与 plan.md 记录的目标 SQL 逐字一致 —— 换成聚合会改变被测行为：
// 聚合不需要真正取回 20 行，也不需要 filesort，正是候选索引收益的主要来源，
// 用 COUNT/SUM 测出来的差异无法归因到该索引。
//
// WHERE 的列顺序对应候选索引 (user_id, status, created_at)：
// user_id / status 是等值条件放前两位，created_at 既是范围条件又是排序键放末位，
// 因此 candidate 可用同一索引完成过滤 + 范围裁剪 + 有序读取，省掉全表扫描与 filesort。
const defaultSQL = `SELECT id, amount_cents, created_at
FROM orders
WHERE user_id = ? AND status = ? AND created_at >= ?
ORDER BY created_at DESC
LIMIT 20`

// caseUserID 取 1：Zipf 分布下最热的用户，样本量足够（10000 行时有 710 条 paid）。
const caseUserID = uint64(1)

// caseStatus 取占比最高的状态（权重 0.55）。
const caseStatus = "paid"

// caseCreatedAtFrom 是 Query Case 的固定时间下界。
//
// 取值可复现：它是硬编码常量，不含 time.Now()，也不从数据集反推，
// 因此任何机器、任何时刻、任何 seed 重跑都得到完全相同的 Query Case。
//
// 不能取数据集最小时间，否则范围条件恒真、退化成不参与过滤。
// 数据集的 created_at 自 dataset 包的固定基准 2024-01-01T00:00:00Z 起递增，
// 10000 行时跨度约 6h31m；取 +2h 实测把 user_id=1 的 paid 行从 710 条筛到 532 条
// （过滤掉约 25%），范围条件真实生效。行数放大后跨度变长、该下界的选择性下降，
// 但仍在数据范围内，不会退化成恒真或恒假。
var caseCreatedAtFrom = time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC)

// DefaultCase 是本切片唯一的 Query Case。
func DefaultCase() QueryCase {
	return QueryCase{
		Name: "hot_user_paid_recent_desc",
		SQL:  defaultSQL,
		Args: []any{caseUserID, caseStatus, caseCreatedAtFrom},
	}
}

// RunOptions 控制单侧测量的预热与采样轮数。
type RunOptions struct {
	Warmup int // 预热轮数，结果全部丢弃
	Rounds int // 计入统计的轮数
}

// Session 持有一条已预处理的语句，支持逐轮触发测量。
// 交替测量（B→C→B→C）必须两侧同时保持会话，否则每轮重新预处理会把
// parse/plan 开销混进延迟，且两侧的预处理时机不对称。
type Session struct {
	db   *sql.DB
	stmt *sql.Stmt
	c    QueryCase
}

// NewSession 预处理语句。预处理一次并复用：否则每轮都含一次 parse/plan 开销，
// 那部分与索引收益无关，只会抬高噪声。
func NewSession(ctx context.Context, db *sql.DB, c QueryCase) (*Session, error) {
	stmt, err := db.PrepareContext(ctx, c.SQL)
	if err != nil {
		return nil, fmt.Errorf("预处理失败: %w", err)
	}
	return &Session{db: db, stmt: stmt, c: c}, nil
}

func (s *Session) Close() error { return s.stmt.Close() }

// Warmup 跑 n 轮并丢弃结果，让 buffer pool 与执行计划进入稳定状态。
func (s *Session) Warmup(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if err := execOnce(ctx, s.stmt, s.c.Args); err != nil {
			return fmt.Errorf("预热第 %d 轮失败: %w", i+1, err)
		}
	}
	return nil
}

// Once 测量一轮，返回本轮延迟。
func (s *Session) Once(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	err := execOnce(ctx, s.stmt, s.c.Args)
	elapsed := time.Since(start)
	if err != nil {
		return 0, err
	}
	return elapsed, nil
}

// Collect 连续测量 n 轮。用于单侧独立标定 —— 标定不需要交替，
// 它衡量的是该侧自身的离散程度。
func (s *Session) Collect(ctx context.Context, n int) ([]time.Duration, error) {
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		d, err := s.Once(ctx)
		if err != nil {
			return nil, fmt.Errorf("第 %d 轮失败: %w", i+1, err)
		}
		out = append(out, d)
	}
	return out, nil
}

// Run 在单个实例上测量一个 Query Case，返回每轮延迟。
// 只做采集，不做统计与判定；单侧可独立调用，便于两侧分别标定。
func Run(ctx context.Context, db *sql.DB, c QueryCase, opt RunOptions) ([]time.Duration, error) {
	if opt.Rounds < 0 || opt.Warmup < 0 {
		return nil, fmt.Errorf("轮数不能为负: warmup=%d rounds=%d", opt.Warmup, opt.Rounds)
	}

	s, err := NewSession(ctx, db, c)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	if err := s.Warmup(ctx, opt.Warmup); err != nil {
		return nil, err
	}
	return s.Collect(ctx, opt.Rounds)
}

// execOnce 执行一次并把结果集完整读到 EOF。
// 必须走 QueryContext 并真正遍历、Scan、检查 rows.Err、关闭 rows：
// 只发出查询而不读取，测到的只是服务端开始响应的时间，
// 而候选索引省掉的 filesort 与随机回表恰恰体现在取回这 20 行的过程中。
func execOnce(ctx context.Context, stmt *sql.Stmt, args []any) error {
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		id        uint64
		amount    int64
		createdAt time.Time
	)
	for rows.Next() {
		if err := rows.Scan(&id, &amount, &createdAt); err != nil {
			return err
		}
	}
	return rows.Err()
}
