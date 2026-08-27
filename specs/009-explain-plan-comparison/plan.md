# 计划

1. 在纯 Go 包中递归提取已知 EXPLAIN JSON 字段；保留原始 JSON，未知节点只跳过，不作推断。
2. 定义同一报告中的 baseline/candidate 摘要与差异字段，固定 L1 和 `eligible_for_performance_claim=false`。
3. CLI 复用 shadowgate 完成 SQL、CandidateSpec、版本、快照与 candidate index 门禁；仅额外在 baseline 执行 EXPLAIN。
4. 在已有 healthy 影子库运行真实示例，报告计划差异而非性能结论；不操作 Docker 生命周期。
5. 风险：MySQL 计划形状随查询变化。降级：输出原始 JSON，摘要仅记录可靠字段；解析失败则拒绝生成成功报告。

真实实验记录：2026-08-27 在已有 healthy 影子库上生成对照报告：1,000,000 行、快照 `5f2ed9465c96…`、MySQL 8.4.11；
baseline 为 `ALL`/filesort/预计 992595 行，candidate 为 `ref`、`idx_cand_status_amount`、无 filesort/预计 496809 行。报告为 L1，
`eligible_for_performance_claim=false`；未操作 Docker 生命周期。章程暂未建立。
