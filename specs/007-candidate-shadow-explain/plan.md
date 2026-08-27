# 计划

1. 复用 sqladmit 与 CandidateSpec，先在无数据库句柄的输入门禁中固定拒绝原因和候选 DDL。
2. 连接两侧仅重算数据快照；随后在 candidate 的 information_schema 校验表、列和索引定义，baseline 不接收 DDL。
3. 仅在 candidate 创建匹配的索引、ANALYZE 后执行 `EXPLAIN FORMAT=JSON`，将事实写入独立 JSON 报告并标为 L1。
4. CLI 读取 SQL/CandidateSpec 文件；真实实验使用已有健康影子库，不启停或删除 Docker 容器/卷。
5. 风险：文本准入不是 AST、DDL 会改 candidate schema。降级：复用保守准入，拒绝可执行注释/索引冲突，失败不声称报告或性能收益。

真实实验记录：2026-08-27 在既有 healthy 影子库上运行示例，快照为 1,000,000 行 / `5f2ed9465c96…`，创建
`idx_cand_status_amount`，MySQL 8.4.11 的 EXPLAIN 使用该索引且不 filesort；报告仍为 L1、无性能收益结论。未操作 Docker 生命周期；章程暂未建立。
