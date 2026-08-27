---
level: L1
feature: 002-performance-claim-admission
created: 2026-08-27
---

# 机器可读证据准入

**原始需求**：报告里已有 `verdict=Better`，但本次 candidate 标定噪声 13.02% 超过 10% 准入线，真实结论只能是 L1、不能声称性能收益。这个限定目前只写在 README 和 plan 里，机器可读报告缺失，后续 Agent 可能只读到 Better 就误报收益。

## 目标

让报告自己说清「方向性判定」与「是否够格声称性能收益」是两件事，且后者可被程序直接消费，不依赖人去读 Markdown。

## 非目标

- 不重新运行百万行实验，不修改已有实验数据，不编造新指标
- 不调整噪声准入线与方向判定门槛的数值，只把它们记录进报告
- 不做 LLM、Agent 编排、Webhook、前端、检索、写压测试
- 不触碰任何容器
- 不引入报告版本迁移机制（当前无外部消费者）

## 验收条件

1. **给定** candidate 标定噪声比 13.02%、方向判定为 Better 的既有实验数据，**当** 生成报告，**则** 报告中 `direction_verdict=Better`、`eligible_for_performance_claim=false`、`evidence_level=L1`，且 `rejection_reasons` 含一条表示「candidate 噪声比超出准入线」的机器可读原因码。
2. **给定** 任一侧标定噪声比超过准入线，**当** 生成报告，**则** 无论方向判定是什么，都不得允许声称性能收益。
3. **给定** 两侧噪声比均在准入线内且方向判定为 Better，**当** 生成报告，**则** 允许声称性能收益，`rejection_reasons` 为空。
4. **给定** 方向判定为 NotSignificant 而两侧噪声均合格，**当** 生成报告，**则** 不允许声称性能收益，且原因码指向「方向判定不显著」而非噪声。
5. **给定** 只做了标定、未做正式测量，**当** 生成报告，**则** 不允许声称性能收益，且不得出现方向判定值。
6. **给定** 任一报告，**当** 读取报告，**则** 准入线数值与方向判定门槛倍数都能从报告内读到，无需查阅代码或文档。
7. **给定** 任一报告，**当** 查看终端摘要，**则** 「方向性结果」与「是否允许声称性能收益」分两行明确呈现，不允许收益时同时给出原因。

## 默认假设

- 准入线沿用现有约定：任一侧标定噪声比 > 10% 即不合格；方向判定门槛沿用 2 × 两侧较大噪声比。
- 证据等级取值沿用 README 第 5 节的 L0–L3；本切片只需产出 L1 与 L2 两种：准入通过且方向显著为 L2，否则 L1。
- 原因码用稳定的英文蛇形标识（供程序判等），人类可读说明另附。
- 准入判定基于**标定**阶段的噪声比，与方向判定门槛所用基数保持一致。
- 既有 `validation_result.json` 与 `calibration-1.json` 不回填新字段；重新生成才带新字段，且需说明这一点。
- 消费方约定写入报告与 README：只有 `eligible_for_performance_claim=true` 才可作为性能收益证据。

## 实现范围

准入规则判定、报告字段扩展、终端摘要呈现、上述规则的单元测试。不改测量与统计逻辑本身。

## 任务

- [x] **T001** 准入规则：按噪声准入线与方向判定结果算出 `eligible_for_performance_claim`、`evidence_level`、`rejection_reasons`（稳定原因码），并暴露准入线与门槛倍数常量。完成条件：规则为纯函数，不依赖数据库。验证：`go test ./internal/measure/...`
- [x] **T002** 准入规则单元测试，逐条覆盖验收条件 1–5，含「噪声超标 + Better」这一关键组合。验证：`go test -count=1 ./internal/measure/...`
- [x] **T003** 报告字段扩展与终端摘要：JSON 增加 `direction_verdict`、`eligible_for_performance_claim`、`evidence_level`、`rejection_reasons`、`admission_thresholds`；摘要分行输出方向性结果与是否允许声称收益。完成条件：JSON 与摘要仍来自同一结果对象，不各自重算。验证：`go build ./... && go vet ./... && go test -count=1 ./...`
- [x] **T004** 在 README 消费方约定处写明：只有 `eligible_for_performance_claim=true` 的报告可作为性能收益证据。验证：`go test -count=1 ./...`
