package measure

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrSnapshotMismatch 表示两侧数据不同，任何 A/B 结论都无意义。
	ErrSnapshotMismatch = errors.New("两侧快照不一致，数据不可比")
	// ErrCandidateIndexMissing 表示 candidate 上没有候选索引，测出来的差异与索引无关。
	ErrCandidateIndexMissing = errors.New("candidate 上不存在候选索引")
	// ErrServerVersionMismatch 表示两侧 MySQL 版本不同。
	// 不同版本的优化器、执行器、默认参数都可能不同，延迟差异无法归因到候选索引。
	ErrServerVersionMismatch = errors.New("两侧 MySQL 版本不一致")
)

// SideSnapshot 是门禁现场重算得到的单侧摘要。
type SideSnapshot struct {
	Name   string
	Digest string
	Rows   int64
}

// SnapshotProbe 现场重算单侧快照摘要。
//
// 门禁**不缓存、不读取上一次 verify 的结果**：每个 CLI 命令都是独立进程，
// 进程之间没有可靠的共享状态，「上一条命令 verify 过了」不能证明「现在还一致」——
// 中间可能有人手改了数据、重建了容器、跑了另一个 seed。
// 把状态写文件同样脆弱：文件会过期、会被复制到别的机器。所以每次现场重算。
type SnapshotProbe interface {
	Snapshot(ctx context.Context, side string) (SideSnapshot, error)
}

// IndexProbe 判断 candidate 上候选索引是否存在。
type IndexProbe interface {
	HasCandidateIndex(ctx context.Context) (bool, error)
}

// VersionProbe 查询单侧服务端版本。
// 版本从服务端现场查出，不写死在代码或配置里：镜像 tag 是 8.4，
// 实际跑的可能是 8.4.x 的任意一个补丁版本，硬编码会让报告里的环境信息失真。
type VersionProbe interface {
	ServerVersion(ctx context.Context, side string) (string, error)
}

// RequireSameServerVersion 查询两侧版本并要求完全一致，返回该版本。
func RequireSameServerVersion(ctx context.Context, p VersionProbe) (string, error) {
	b, err := p.ServerVersion(ctx, "baseline")
	if err != nil {
		return "", fmt.Errorf("查询 baseline 版本失败: %w", err)
	}
	c, err := p.ServerVersion(ctx, "candidate")
	if err != nil {
		return "", fmt.Errorf("查询 candidate 版本失败: %w", err)
	}
	if b != c {
		return "", fmt.Errorf("%w: baseline=%s candidate=%s", ErrServerVersionMismatch, b, c)
	}
	return b, nil
}

// SnapshotGate 是通过一致性检查后的两侧摘要。
type SnapshotGate struct {
	Baseline  SideSnapshot
	Candidate SideSnapshot
}

// Digest 返回两侧共同的摘要。仅在门禁通过后有意义。
func (g SnapshotGate) Digest() string { return g.Baseline.Digest }

// Rows 返回两侧共同的行数。仅在门禁通过后有意义。
func (g SnapshotGate) Rows() int64 { return g.Baseline.Rows }

// RequireIdenticalSnapshots 现场重算两侧摘要，不一致即拒绝。
// index 与 bench 在动手之前都必须过这道门 —— 快照不一致却出报告，
// 比不出报告危险得多：读者会把噪声或数据差异当成索引收益。
func RequireIdenticalSnapshots(ctx context.Context, p SnapshotProbe) (SnapshotGate, error) {
	b, err := p.Snapshot(ctx, "baseline")
	if err != nil {
		return SnapshotGate{}, fmt.Errorf("重算 baseline 快照失败: %w", err)
	}
	c, err := p.Snapshot(ctx, "candidate")
	if err != nil {
		return SnapshotGate{}, fmt.Errorf("重算 candidate 快照失败: %w", err)
	}

	g := SnapshotGate{Baseline: b, Candidate: c}

	if b.Rows != c.Rows {
		return g, fmt.Errorf("%w: 行数 baseline=%d candidate=%d", ErrSnapshotMismatch, b.Rows, c.Rows)
	}
	if b.Digest != c.Digest {
		return g, fmt.Errorf("%w: 行数同为 %d 但摘要不同 (baseline=%s candidate=%s)",
			ErrSnapshotMismatch, b.Rows, shortDigest(b.Digest), shortDigest(c.Digest))
	}
	return g, nil
}

// RequireCandidateIndex 要求 candidate 上已存在候选索引。
// 缺索引时两侧执行计划相同，测出来的只会是噪声，却很容易被当成「索引没用」。
func RequireCandidateIndex(ctx context.Context, p IndexProbe) error {
	ok, err := p.HasCandidateIndex(ctx)
	if err != nil {
		return fmt.Errorf("查询候选索引失败: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w，请先运行 index --target candidate", ErrCandidateIndexMissing)
	}
	return nil
}

func shortDigest(d string) string {
	if len(d) <= 12 {
		return d
	}
	return d[:12]
}
