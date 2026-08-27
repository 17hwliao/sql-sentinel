---
level: L2
feature: 004-series-evidence-admission
created: 2026-08-27
---

# 跨运行系列证据准入

## 原始需求

单份 `bench` 报告只能判断自身的一次标定。阶段 003 中最终 N=10 报告为 L2，
但另一份独立标定 candidate 为 10.19%，按预先协议整个实验仍应为 L1。
需要一个机器可读的系列准入命令，消除“人工挑选单份好报告”的空间。

## 目标

- 新增 `sentinel admit-series`：至少接收两份 `--calibration` 和一份 `--measurement` JSON，输出单独的系列结论 JSON 与终端摘要。
- 仅当所有输入报告的 dataset version、snapshot digest、MySQL version、Query Case（名称/SQL/参数）、候选索引和 executions-per-round 完全一致时，才允许汇总。
- 每份 calibration 必须是 `rounds=0` 报告，且 baseline/candidate 噪声都 ≤10%；measurement 必须已有 `direction_verdict=Better` 且其单份 `eligible_for_performance_claim=true`。
- 系列结论只在全部条件满足时给 `eligible_for_performance_claim=true`、`evidence_level=L2`；任一条件不满足则 L1，并输出稳定原因码与关联文件。
- 用阶段 003 的三份 N=10 报告实际运行，必须输出 L1 并指出第二次 calibration candidate 噪声超标。

## 非目标

- 不修改单份 `bench` 的 admission 规则、噪声线、测量内核或历史报告内容。
- 不引入数据库、Docker 操作、持久化任务、LLM/Agent、Webhook 或前端。
- 不把 L2 自动升级为 L3，不把多次失败报告用平均值“洗掉”。

## 默认假设

- 输入是本项目 `bench` 生成的可信本地 JSON；命令仍逐字段验证，不信任文件名。
- 使用重复 `--calibration path` flag；至少两份，顺序不影响准入原因码的稳定顺序。
- 输出默认 `series_admission.json`，是派生结论而非原始测量记录，可安全重新生成。
- 任何读取、JSON 解析或字段不一致错误都使命令退出非零，不生成成功结论。

## 验收条件

1. 两份 quiet calibration 与一份 eligible Better measurement 可生成 L2 系列结论。
2. 任一 calibration 噪声 >10% 时，输出 L1 和 `calibration_noise_exceeds_limit`，并包含关联文件。
3. dataset/snapshot/MySQL/case/index/N 任一不一致时命令失败，不能误汇总。
4. 少于两份 calibration、measurement 未测量、NotSignificant 或单份不合格时均不允许系列收益。
5. 实际阶段 003 三份 N=10 报告生成 L1；不运行 Docker、不修改输入 JSON。

## 任务将在 tasks.md 中拆分

计划会先建立纯验证/汇总内核和测试，再接 CLI、JSON 与真实已有报告验证。
