# 任务

- [x] T001 实现严格输入解码、来源摘要和受限诊断纯函数，覆盖三类事实与缺失边界。验证：`go test ./internal/diagnosis/...`
- [x] T002 接 `diagnose-plan` 文件 CLI，拒绝无效输入且只输出 JSON。验证：`go run ./cmd/sentinel diagnose-plan --help`
- [x] T003 用真实 EXPLAIN 对照报告生成诊断 JSON，更新 README。验证：`go run ./cmd/sentinel diagnose-plan --in explain_comparison_report.json --out diagnostic_hypotheses.json`
- [x] T004 运行全量验证、规格校验、私有复盘并提交。验证：`go test -count=1 ./...`
