package measure

import (
	"context"
	"errors"
	"testing"
)

// 本文件覆盖的风险：状态门禁失效。
// 快照不一致却出报告，比不出报告危险得多 —— 读者会把数据差异当成索引收益。
// 另一半风险是门禁「看起来在」但实际读的是缓存/上一次结果，那等于没有门禁。

type fakeSnapshotProbe struct {
	sides map[string]SideSnapshot
	errs  map[string]error
	calls []string // 记录调用顺序与次数，用于证明门禁真的现场重算了
}

func (p *fakeSnapshotProbe) Snapshot(_ context.Context, side string) (SideSnapshot, error) {
	p.calls = append(p.calls, side)
	if err := p.errs[side]; err != nil {
		return SideSnapshot{}, err
	}
	return p.sides[side], nil
}

func probe(bDigest string, bRows int64, cDigest string, cRows int64) *fakeSnapshotProbe {
	return &fakeSnapshotProbe{
		sides: map[string]SideSnapshot{
			"baseline":  {Name: "baseline", Digest: bDigest, Rows: bRows},
			"candidate": {Name: "candidate", Digest: cDigest, Rows: cRows},
		},
		errs: map[string]error{},
	}
}

func TestGateAcceptsIdenticalSnapshots(t *testing.T) {
	p := probe("abc123def456aa", 10000, "abc123def456aa", 10000)

	g, err := RequireIdenticalSnapshots(context.Background(), p)
	if err != nil {
		t.Fatalf("两侧一致却被拒绝: %v", err)
	}
	if g.Rows() != 10000 || g.Digest() != "abc123def456aa" {
		t.Fatalf("门禁返回的摘要不对: rows=%d digest=%s", g.Rows(), g.Digest())
	}
}

// 门禁必须每次都真正查询两侧，不能复用上一次结果。
func TestGateRecomputesBothSidesEveryCall(t *testing.T) {
	p := probe("same", 10, "same", 10)

	for i := 1; i <= 3; i++ {
		if _, err := RequireIdenticalSnapshots(context.Background(), p); err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i, err)
		}
	}

	if len(p.calls) != 6 {
		t.Fatalf("3 次调用共探测 %d 次，应为 6 次（每次都要现场重算两侧）: %v", len(p.calls), p.calls)
	}
	for i := 0; i < len(p.calls); i += 2 {
		if p.calls[i] != "baseline" || p.calls[i+1] != "candidate" {
			t.Fatalf("第 %d 次调用的探测顺序为 %v，应为 baseline 后 candidate", i/2+1, p.calls[i:i+2])
		}
	}
}

// 摘要不同必须拒绝，且错误可用 errors.Is 识别。
func TestGateRejectsDifferentDigest(t *testing.T) {
	p := probe("aaaaaaaaaaaaaa", 10000, "bbbbbbbbbbbbbb", 10000)

	_, err := RequireIdenticalSnapshots(context.Background(), p)
	if err == nil {
		t.Fatal("摘要不同却通过了门禁")
	}
	if !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("错误未包装 ErrSnapshotMismatch: %v", err)
	}
}

// 行数不同必须拒绝，并且报出两侧行数便于定位。
func TestGateRejectsDifferentRowCount(t *testing.T) {
	p := probe("same", 10000, "same", 9999)

	_, err := RequireIdenticalSnapshots(context.Background(), p)
	if err == nil {
		t.Fatal("行数不同却通过了门禁")
	}
	if !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("错误未包装 ErrSnapshotMismatch: %v", err)
	}
}

// 探测本身失败时必须拒绝，不能当成「一致」放过去。
func TestGateRejectsWhenProbeFails(t *testing.T) {
	boom := errors.New("连接被拒绝")

	for _, side := range []string{"baseline", "candidate"} {
		t.Run(side, func(t *testing.T) {
			p := probe("same", 10, "same", 10)
			p.errs[side] = boom

			_, err := RequireIdenticalSnapshots(context.Background(), p)
			if err == nil {
				t.Fatalf("%s 探测失败却通过了门禁", side)
			}
			if !errors.Is(err, boom) {
				t.Fatalf("错误未保留底层原因: %v", err)
			}
			// 探测失败不是「不一致」，不应误报为 ErrSnapshotMismatch
			if errors.Is(err, ErrSnapshotMismatch) {
				t.Fatalf("探测失败被误报为快照不一致: %v", err)
			}
		})
	}
}

type fakeIndexProbe struct {
	has   bool
	err   error
	calls int
}

func (p *fakeIndexProbe) HasCandidateIndex(_ context.Context) (bool, error) {
	p.calls++
	return p.has, p.err
}

func TestRequireCandidateIndexAcceptsWhenPresent(t *testing.T) {
	p := &fakeIndexProbe{has: true}
	if err := RequireCandidateIndex(context.Background(), p); err != nil {
		t.Fatalf("索引存在却被拒绝: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("探测次数 = %d，应为 1", p.calls)
	}
}

func TestRequireCandidateIndexRejectsWhenMissing(t *testing.T) {
	err := RequireCandidateIndex(context.Background(), &fakeIndexProbe{has: false})
	if err == nil {
		t.Fatal("索引缺失却通过了门禁")
	}
	if !errors.Is(err, ErrCandidateIndexMissing) {
		t.Fatalf("错误未包装 ErrCandidateIndexMissing: %v", err)
	}
}

// 查询索引本身失败时必须拒绝，不能默认「有索引」。
func TestRequireCandidateIndexRejectsOnProbeError(t *testing.T) {
	boom := errors.New("表不存在")

	err := RequireCandidateIndex(context.Background(), &fakeIndexProbe{has: true, err: boom})
	if err == nil {
		t.Fatal("探测失败却通过了门禁")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("错误未保留底层原因: %v", err)
	}
}

type fakeVersionProbe struct {
	versions map[string]string
	errs     map[string]error
	calls    []string
}

func (p *fakeVersionProbe) ServerVersion(_ context.Context, side string) (string, error) {
	p.calls = append(p.calls, side)
	if err := p.errs[side]; err != nil {
		return "", err
	}
	return p.versions[side], nil
}

func versionProbe(b, c string) *fakeVersionProbe {
	return &fakeVersionProbe{
		versions: map[string]string{"baseline": b, "candidate": c},
		errs:     map[string]error{},
	}
}

// 版本必须现场查两侧，一致时返回完整版本串（含补丁号，不是镜像 tag）。
func TestRequireSameServerVersionAcceptsMatching(t *testing.T) {
	p := versionProbe("8.4.6", "8.4.6")

	got, err := RequireSameServerVersion(context.Background(), p)
	if err != nil {
		t.Fatalf("版本一致却被拒绝: %v", err)
	}
	if got != "8.4.6" {
		t.Fatalf("返回版本 = %q，应为 8.4.6", got)
	}
	if len(p.calls) != 2 || p.calls[0] != "baseline" || p.calls[1] != "candidate" {
		t.Fatalf("探测记录 = %v，应为两侧各查一次", p.calls)
	}
}

// 补丁版本不同也必须拒绝：优化器行为可能随补丁变化，差异无法归因到候选索引。
func TestRequireSameServerVersionRejectsMismatch(t *testing.T) {
	_, err := RequireSameServerVersion(context.Background(), versionProbe("8.4.6", "8.4.11"))
	if err == nil {
		t.Fatal("版本不同却通过了门禁")
	}
	if !errors.Is(err, ErrServerVersionMismatch) {
		t.Fatalf("错误未包装 ErrServerVersionMismatch: %v", err)
	}
}

// 查询版本失败时必须拒绝，不能猜一个版本继续。
func TestRequireSameServerVersionRejectsOnProbeError(t *testing.T) {
	boom := errors.New("连接中断")

	for _, side := range []string{"baseline", "candidate"} {
		t.Run(side, func(t *testing.T) {
			p := versionProbe("8.4.6", "8.4.6")
			p.errs[side] = boom

			got, err := RequireSameServerVersion(context.Background(), p)
			if err == nil {
				t.Fatalf("%s 查询失败却通过了门禁", side)
			}
			if got != "" {
				t.Fatalf("失败时仍返回了版本 %q", got)
			}
			if !errors.Is(err, boom) {
				t.Fatalf("错误未保留底层原因: %v", err)
			}
			if errors.Is(err, ErrServerVersionMismatch) {
				t.Fatalf("查询失败被误报为版本不一致: %v", err)
			}
		})
	}
}
