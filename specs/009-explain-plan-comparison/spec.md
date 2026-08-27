---
level: L2
feature: 009-explain-plan-comparison
created: 2026-08-27
---

# Baseline/Candidate EXPLAIN 结构化对照报告

## 目标

- 新增 `explain-compare --sql query.sql --candidate spec.json --out report.json`，复用既有 SQL/CandidateSpec/快照门禁后，分别获取 baseline 与 candidate 的 `EXPLAIN FORMAT=JSON`。
- 将两个原始计划和稳定的结构化摘要写入同一份 L1 报告：表访问方式、索引、预计扫描行数、覆盖索引与 filesort；只陈述计划差异。
- CandidateSpec 仍只可作用于 candidate；baseline 只执行版本、快照与 EXPLAIN，绝不接收 DDL。

## 非目标

- 不执行原 SQL，不计时、不计算性能收益，不从 EXPLAIN 自动生成索引建议。
- 不尝试完整 MySQL 计划 AST 或跨版本成本比较，不接入 Agent、Webhook、Redis、前端或生产库。

## 验收

1. 相同已准入 SQL 在版本和快照一致的 baseline/candidate 上生成一份同时含两侧原始计划与摘要的 JSON 报告。
2. 摘要只从 JSON 中实际出现的字段提取；多表或未知计划节点不得 panic、不得虚构差异。
3. 报告固定为 `evidence_level=L1`、`eligible_for_performance_claim=false`，并显式声明计划差异不是实测收益。
4. 真实示例报告可显示 candidate 索引与 filesort 状态的对照；不启停 Docker，不修改 baseline。

## 默认假设

- 当前 MySQL 8.4 的 `EXPLAIN FORMAT=JSON` 可被解析为 JSON；跨版本成本数值不可直接比较。
- 当前比较只支持 `orders-v1` 及既有 candidate shadow gate 的自包含 SQL/CandidateSpec 输入范围。
