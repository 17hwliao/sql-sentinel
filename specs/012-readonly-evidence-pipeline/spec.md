---
level: L2
feature: 012-readonly-evidence-pipeline
created: 2026-08-27
---

# 端到端只读证据管线

## 目标

- 新增 `sentinel pipeline`：从一个 SQL 文本文件和 CandidateSpec 串联只读 SQL 准入、candidate shadow EXPLAIN、计划对照和受限诊断。
- 向 `--out-dir` 写入 `sql_admission.json`、`candidate_explain_report.json`、`explain_comparison_report.json`、`diagnostic_hypotheses.json` 与 `pipeline_report.json` 五个机器可读工件。
- 四份步骤 JSON 都携带其直接输入的 SHA-256 溯源；汇总报告记录已完成步骤、停止步骤、拒绝码和每个已写工件的摘要。
- 管线汇总及所有计划/诊断结论固定为 L1，`eligible_for_performance_claim=false`。

## 非目标

- 不运行受控 A/B、系列准入或 evidence binding；不读取 measurement 报告，也不升级性能收益资格。
- 不修改 Docker 环境、不创建 baseline DDL、不接入 LLM；仅复用既有影子库门禁，candidate 的受限索引行为维持现有范围。
- 不放宽 SQL/CandidateSpec/快照/MySQL 门禁，也不把 SQL 拒绝或候选索引门禁失败伪装成成功。

## 验收

1. 一条 `pipeline --sql ... --candidate ... --out-dir ...` 命令在健康影子库上产出四份步骤 JSON 和一份汇总，汇总列出顺序完成的四个步骤。
2. 每份步骤 JSON 都包含直接输入 SHA-256；诊断的比较输入摘要仍由严格 JSON 字节流计算，后续消费者可溯源。
3. `SELECT ... FOR UPDATE` 只写准入工件和汇总，第一步以 `locking_read` 停止；无 EXPLAIN、对照或诊断工件，CLI 打印汇总路径/拒绝码后以非零码退出。
4. 任一步拒绝或门禁失败后不执行后续步骤；汇总保持 L1、资格为 false，并使用稳定的停止/拒绝码。

## 默认假设

- `--out-dir` 必须是新建或空目录，以免覆盖未关联的历史证据；输出文件名固定，便于后续自动消费。
- SQL 拒绝属于可预期证据状态，但为便于脚本中止会在写完汇总后返回非零码；文件/JSON/数据库操作错误同样尽力写汇总后返回错误。
