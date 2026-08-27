package measure

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedClock(values ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		v := values[i]
		i++
		return v
	}
}

// 风险：N=1 或 N>1 被错误地计时/折算，会让整套统计口径失真。
func TestExecuteRoundCountsAndNormalizesEachExecution(t *testing.T) {
	base := time.Unix(0, 0)
	for _, tc := range []struct {
		name       string
		executions int
		elapsed    time.Duration
		want       time.Duration
	}{
		{name: "single keeps elapsed", executions: 1, elapsed: 25 * time.Nanosecond, want: 25 * time.Nanosecond},
		{name: "three normalizes batch", executions: 3, elapsed: 30 * time.Nanosecond, want: 10 * time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := executeRound(context.Background(), tc.executions,
				fixedClock(base, base.Add(tc.elapsed)), func(context.Context) error {
					calls++
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
			if calls != tc.executions || got != tc.want {
				t.Fatalf("calls=%d elapsed=%s; want calls=%d elapsed=%s", calls, got, tc.executions, tc.want)
			}
		})
	}
}

// 风险：批量中途失败仍继续执行，会污染下一次操作并产出部分样本。
func TestExecuteRoundStopsAfterFailure(t *testing.T) {
	calls := 0
	_, err := executeRound(context.Background(), 3,
		fixedClock(time.Unix(0, 0), time.Unix(0, 1)), func(context.Context) error {
			calls++
			if calls == 2 {
				return errors.New("database failed")
			}
			return nil
		})
	if err == nil || calls != 2 {
		t.Fatalf("err=%v calls=%d; want failure after exactly 2 calls", err, calls)
	}
}

// 风险：请求取消后继续向数据库发查询，既浪费资源也会造成 goroutine/连接问题。
func TestExecuteRoundStopsBeforeNextCallAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := executeRound(ctx, 3,
		fixedClock(time.Unix(0, 0), time.Unix(0, 1)), func(context.Context) error {
			calls++
			cancel()
			return nil
		})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d; want context.Canceled after 1 call", err, calls)
	}
}

func TestExecuteRoundRejectsNonPositiveExecutionCount(t *testing.T) {
	for _, n := range []int{0, -1} {
		if _, err := executeRound(context.Background(), n, time.Now, func(context.Context) error { return nil }); err == nil {
			t.Fatalf("executions=%d should fail", n)
		}
	}
}
