# 系列证据准入任务

## 任务

- [x] **T001** 创建 `internal/series` 的输入、结论、稳定原因码与纯 `Evaluate()`；测试 quiet calibration + eligible Better measurement 得 L2，以及 calibration 超噪声得 L1。验证：`go test -count=1 ./internal/series/...`
- [x] **T002** 增加报告角色与环境/协议指纹校验；测试 dataset、snapshot、MySQL、case、索引、N 任一不一致时拒绝汇总。验证：`go test -count=1 ./internal/series/...`
- [x] **T003** 增加 `admit-series` CLI：可重复 `--calibration`、单个 `--measurement`、`--out`，JSON/摘要来自同一结论对象。验证：`go run ./cmd/sentinel admit-series --help` 与坏参数退出非零。
- [x] **T004** 用三个阶段 003 N=10 报告真实运行；确认 L1、`calibration_noise_exceeds_limit` 与第二份文件名，不修改任何输入报告或 Docker。验证：PowerShell 读取输出 JSON 字段。
- [x] **T005** 将实际命令和结论写回 README/plan；运行全量 Go 检查与 Adaptive Spec 校验。验证：所有命令通过。

## 测试克制说明

测试只覆盖“错误汇总会造成虚假性能声明”的边界：噪声、角色、关键指纹和单份 admission。JSON 文件读写与 flag 框架只做一条端到端验证，不为普通字段访问器堆测试。
