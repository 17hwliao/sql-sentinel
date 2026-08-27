# 计划

1. 严格解码四类 JSON，验证诊断 SHA-256 与对照原文、诊断来源环境和对照一致。
2. 纯函数逐项比较对照与 measurement 的快照、MySQL、SQL、candidate 索引，并验证 series 的 measurement 文件身份。
3. 将全部匹配与既有 series 准入分开呈现；只在两者均成立时搬运 L2/资格，否则固定 L1 与原因码。
4. CLI 只读文件；用已提交的阶段 009/010 与 N=10 系列报告得到真实“未绑定”结果并更新 README。
5. 风险：不同报告 schema/身份不完整。降级：拒绝或给出明确未绑定原因，不尝试宽松匹配。

真实实验记录（T003）：2026-08-27 只读运行 `bind-evidence`，输入为 009/010 的
`diagnostic_hypotheses.json` / `explain_comparison_report.json`、历史
`validation_result.json` 和现有 `series_admission.json`。输出
`evidence_binding_report.json` 为 `binding_complete=false`、L1、
`eligible_for_performance_claim=false`；实际拒绝码为
`diagnosis_source_sha_mismatch`、`measurement_sql_mismatch`、
`candidate_index_mismatch`、`series_measurement_file_mismatch`、
`series_not_eligible`、`series_evidence_level_not_l2`。快照摘要和 MySQL
版本实际相同，故不伪造这两项失配。未连接 MySQL、未运行 Docker；章程暂未建立。
