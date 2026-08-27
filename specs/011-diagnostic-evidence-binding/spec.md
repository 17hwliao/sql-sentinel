---
level: L2
feature: 011-diagnostic-evidence-binding
created: 2026-08-27
---

# 诊断假设与测量证据绑定

## 目标

- 新增 `bind-evidence`，严格关联诊断报告、其 EXPLAIN 对照来源、正式 measurement 报告和系列准入报告。
- 验证诊断来源 SHA-256、快照、MySQL 版本、SQL 文本、candidate 索引和系列 measurement 文件身份；不匹配时输出稳定拒绝原因而非强行关联。
- 只有绑定全部一致且已有系列准入允许时，才继承既有 L2 收益资格；该命令不重新计算性能结论。

## 非目标

- 不重新运行测量、不连接 MySQL、不修改任何输入报告或生成新的统计数据。
- 不以 SQL 语义相似性、索引前缀相似性或人工判断绕过精确身份匹配；不接入 Agent/LLM。

## 验收

1. 来源哈希或任一环境/SQL/索引身份不一致时，输出 L1、`eligible_for_performance_claim=false` 和稳定原因码。
2. 完全一致且系列报告已准入时，绑定结果才能继承 L2 与收益资格；绑定器不自行把 L1 升级。
3. 阶段 009/010 对照与诊断同源，但与现有 N=10 measurement 的 SQL/索引不一致，必须产生未绑定结果。
4. 全流程只读取本地 JSON；未知字段、trailing JSON 或损坏报告明确失败。

## 默认假设

- measurement 报告来自本项目 `bench`，series 报告来自 `admit-series`；路径身份按文件 basename 比较。
- SQL 身份采用精确文本比较，避免在没有 AST/参数绑定协议时把不同查询误认等价。
