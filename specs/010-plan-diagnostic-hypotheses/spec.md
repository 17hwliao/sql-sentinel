---
level: L2
feature: 010-plan-diagnostic-hypotheses
created: 2026-08-27
---

# EXPLAIN 差异的受限诊断假设

## 目标

- 新增 `diagnose-plan --in explain_comparison_report.json --out diagnosis.json`，严格读取阶段 009 的 L1 对照报告，输出稳定、机器可读的待验证诊断假设。
- 仅从已有事实生成三类保守假设：candidate 索引消除 filesort、改变访问路径、降低预计扫描行数；每条附来源事实与后续验证要求。
- 输出固定为 L1 和 `eligible_for_performance_claim=false`，不生成索引建议、不声称性能收益。

## 非目标

- 不连接 MySQL、不执行 SQL/DDL、不调用 Agent/LLM，不自动选择或应用 CandidateSpec。
- 不从成本数值、未知计划节点或缺失字段推断，不把预计扫描行数当作实际耗时。

## 验收

1. 有 filesort 消失、candidate-only 索引或预计扫描下降的对照报告，分别产生对应稳定原因码的假设。
2. 无差异、缺失预计行数或不合格输入不产生虚假假设；未知 JSON 字段和 trailing JSON 被拒绝。
3. 每条假设显式要求受控 A/B 测量和系列准入，整份报告不允许性能收益资格。
4. 真实阶段 009 报告可在纯本地文件路径上生成诊断 JSON，不操作 Docker。

## 默认假设

- 输入只接受本项目当前 `explain-compare` 的 L1 报告；诊断不负责迁移旧 schema。
- 稳定假设码供后续规则或 Agent 消费，人类描述可改变但不能替代证据字段。
