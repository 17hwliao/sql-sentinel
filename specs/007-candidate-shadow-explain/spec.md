---
level: L2
feature: 007-candidate-shadow-explain
created: 2026-08-27
---

# Candidate 影子库执行门禁与 EXPLAIN 报告

## 目标

- 新增 `candidate-explain --sql query.sql --candidate spec.json --out report.json`：只接受 `sql-admit` 已允许的 SQL 和严格 CandidateSpec。
- 只连接固定的 candidate 影子库执行受限 `CREATE INDEX`、`ANALYZE TABLE` 与 `EXPLAIN FORMAT=JSON`；baseline 只用于数据快照门禁，绝不写入。
- 在执行前确认 CandidateSpec 的表与列存在；索引同名但定义不符时拒绝，不覆盖未知结构。
- 输出可复核 JSON：SQL 的 L0 信号、候选 DDL/创建状态、数据快照、MySQL 版本、EXPLAIN JSON 与 L1 证据等级。

## 非目标

- 不执行原 SQL、不做性能基准或收益结论、不生成/执行自由 DDL、不修改生产库。
- 不接入 Agent、LLM、Webhook、Redis、前端或写压测；不尝试完整 SQL AST。

## 验收

1. 非只读 SQL、无效 CandidateSpec、缺失表/列和候选索引名称冲突均在执行前拒绝。
2. 仅 candidate 可创建或复用匹配的 `idx_cand_` 索引；baseline 保持无 DDL，且两侧快照不一致时拒绝。
3. 合法输入只产生 `EXPLAIN FORMAT=JSON` 报告，报告明确为 L1 计划级证据而非性能结论。
4. 单元测试覆盖输入/索引定义门禁；真实 CLI 在现有影子库上可复现，不操作 Docker 生命周期。

## 默认假设

- 当前 `orders-v1` 的受支持表仅位于当前数据库；CandidateSpec 使用普通二级索引，MySQL 返回 `A`/`D` 排序方向。
- 影子库中已通过现有 `seed` 和 `verify` 建好同版本数据；该命令不负责造数或修复快照。
- SQL 文件必须是自包含语句，不支持参数占位符；报告只覆盖 `orders` 数据快照。
