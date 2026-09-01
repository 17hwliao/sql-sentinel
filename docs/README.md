# SQL Sentinel 文档导航

本目录只提供入口与阅读顺序；不移动、不删除、不重写既有规格、实验报告或私有阶段记录。需要判断一个结论是否可信时，优先回到对应的原始 JSON、规格和代码。

## 推荐阅读路径

1. [项目 README](../README.md)：目标、边界、可运行命令与当前事实。
2. [实施步骤](../实施步骤.md)：最初路线图与后置扩展；它是规划来源，不等于所有条目都已实现。
3. `specs/`：每个 feature 的原始需求、计划和任务，是实现范围的权威记录。
4. `.private/stage-records/`：本机阶段复盘与真实命令输出，仅供本地复盘，Git 不跟踪。

## 规格索引

| 范围 | Feature | 建议先看 |
| --- | --- | --- |
| 可信 A/B 测量与资格 | 001–004 | [001](../specs/001-trusted-ab-measurement/spec.md)、[004](../specs/004-series-evidence-admission/spec.md) |
| 受限输入与影子 EXPLAIN | 005–008 | [CandidateSpec](../specs/005-candidate-spec-compiler/spec.md)、[SQL 准入](../specs/006-readonly-sql-admission/spec.md)、[影子门禁](../specs/007-candidate-shadow-explain/spec.md) |
| 只读证据链 | 009–012 | [计划对照](../specs/009-explain-plan-comparison/spec.md)、[诊断](../specs/010-plan-diagnostic-hypotheses/spec.md)、[管线](../specs/012-readonly-evidence-pipeline/spec.md) |
| LLM 与本机 PR 入口 | 013–014 | [受限 Eino 提案](../specs/013-eino-candidate-proposal/spec.md)、[Webhook](../specs/014-pr-review-webhook/spec.md) |
| 验收与跨项目复用 | 015–017 | [最终验收](../specs/015-final-acceptance/spec.md)、[AgentMesh 联桥](../specs/016-agentmesh-bridge-test/spec.md)、本导航整理 |

## 证据与边界

- 根目录的 `*_report.json`、`validation_result.json`、`series_admission.json` 是机器可读原始产物；其中 `eligible_for_performance_claim` 才是性能声称的唯一资格字段。
- 当前 100 万行影子库实验的方向性结果为 Better，但 candidate 噪声 13.02% 超过约定阈值。因此结论保持 L1/false，不能改写为已验证性能收益。
- `.private/` 包含阶段记录、交接资料、简历要点与真实联桥报告。它被 Git 忽略，不能作为公开仓库的可复现替代品。
- 016 的联桥报告只证明 SQL Sentinel 与 AgentMesh 的本机 SSE 传输互操作；不传 SQL、CandidateSpec 或模型 delta，固定为 L1/false。

## 代码入口

| 目的 | 代码 |
| --- | --- |
| CLI | [`cmd/sentinel/`](../cmd/sentinel/) |
| 数据、快照、测量 | [`internal/dataset/`](../internal/dataset/)、[`internal/snapshot/`](../internal/snapshot/)、[`internal/measure/`](../internal/measure/) |
| SQL/CandidateSpec/影子门禁 | [`internal/sqladmit/`](../internal/sqladmit/)、[`internal/candidate/`](../internal/candidate/)、[`internal/shadowgate/`](../internal/shadowgate/) |
| 计划、诊断、证据绑定 | [`internal/plancompare/`](../internal/plancompare/)、[`internal/diagnosis/`](../internal/diagnosis/)、[`internal/evidencebind/`](../internal/evidencebind/) |
| 受限 LLM、Webhook、联桥 | [`internal/agentgraph/`](../internal/agentgraph/)、[`internal/webhook/`](../internal/webhook/)、[`internal/agentmeshbridge/`](../internal/agentmeshbridge/) |

## 面试材料

本地可查看 `.private/SQL-Sentinel-面试全流程报告.md` 与 `.private/resume-bullets.md`。其中只列已经发生的实验和明确的适用范围；公开 README 是对外项目说明的首选入口。
