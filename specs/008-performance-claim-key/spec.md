---
level: L1
feature: 008-performance-claim-key
created: 2026-08-27
---

# 性能收益资格字段统一

## 目标

将 shadowgate EXPLAIN 报告的 JSON 字段统一为既有稳定键
`eligible_for_performance_claim`，与测量报告、系列准入和 README 消费方约定一致。

## 非目标

不改变 L1 证据等级、资格计算、候选索引行为或 EXPLAIN 内容；不保留同义 JSON 字段。

## 验收

- shadowgate JSON 只输出 `eligible_for_performance_claim`，绝不输出旧键。
- README 和真实示例报告使用同一稳定键。
- 全量 Go 验证与 Adaptive Spec 校验通过。

## 默认假设

这是尚未发布的本地 feature 分支，直接修正键名优于提供兼容别名；下游按唯一键消费。

## 任务

- [x] T001 改 shadowgate 报告字段和序列化测试，防止旧键回归。验证：`go test ./internal/shadowgate/...`
- [x] T002 同步 README 与真实 EXPLAIN 报告，完成全量验证、复盘和本地提交。验证：`go test -count=1 ./...`
