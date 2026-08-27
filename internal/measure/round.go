package measure

import (
	"context"
	"fmt"
	"time"
)

// executeRound 在一个计时窗口内完整执行 N 次查询，并把总耗时折算为单次样本。
// N 次执行缺一不可：任一次失败或上下文取消时，部分执行不能伪装成一个有效样本。
func executeRound(
	ctx context.Context,
	executions int,
	now func() time.Time,
	execute func(context.Context) error,
) (time.Duration, error) {
	if executions <= 0 {
		return 0, fmt.Errorf("每轮执行次数必须为正数，收到 %d", executions)
	}

	start := now()
	for i := 0; i < executions; i++ {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("第 %d/%d 次执行前上下文已取消: %w", i+1, executions, err)
		}
		if err := execute(ctx); err != nil {
			return 0, fmt.Errorf("第 %d/%d 次执行失败: %w", i+1, executions, err)
		}
	}
	return now().Sub(start) / time.Duration(executions), nil
}
