# 计划

1. 严格解码对照报告，校验 L1、未获性能收益资格和唯一 JSON 值；计算输入 SHA-256 作为可追溯来源。
2. 使用纯函数只从 Difference 与摘要生成固定诊断码，不比较 MySQL cost、不修改候选或环境。
3. 输出每条假设的事实依据和必经的 `controlled_ab_measurement`、`series_admission` 验证步骤。
4. CLI 只读本地 JSON 文件；以阶段 009 真实报告验证并更新 README，不连接 Docker/MySQL。
5. 风险：计划字段缺失或结构演进。降级：缺失事实就不生成相应假设，未知字段/旧 schema 明确失败。

真实实验记录：2026-08-27 只读 `explain_comparison_report.json` 生成 3 条 L1 假设，来源 SHA-256 为
`1192a7e2bdb1…`，快照 `5f2ed9465c96…`、MySQL 8.4.11；分别是 filesort 消失、candidate-only 索引和预计扫描下降。
所有假设要求受控 A/B 测量与系列准入，未操作 Docker/MySQL。章程暂未建立。
