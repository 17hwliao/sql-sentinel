# 任务

- [x] T001 实现严格 JSON 解码、来源哈希和精确绑定纯函数，覆盖匹配、不匹配与系列未准入。验证：`go test ./internal/evidencebind/...`
- [x] T002 接 `bind-evidence` 本地文件 CLI，只输出机器可读绑定报告。验证：`go run ./cmd/sentinel bind-evidence --help`
- [x] T003 用阶段 009/010 与既有系列报告生成真实未绑定报告，更新 README。验证：`go run ./cmd/sentinel bind-evidence --diagnosis diagnostic_hypotheses.json --comparison explain_comparison_report.json --measurement validation_result.json --series series_admission.json --out evidence_binding_report.json`（实际为 L1 未绑定；拒绝码如实记录于 plan。）
- [x] T004 运行全量验证、规格校验、私有复盘并提交。验证：`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive Spec 校验均通过。
