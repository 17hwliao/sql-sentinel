# 任务

- [x] T001 实现 `internal/pipeline` 的顺序编排、稳定汇总和输入/输出 SHA-256，覆盖全成功、SQL `locking_read` 停止及后续步骤未执行；拒绝时已完成的准入工件与其摘要仍会落盘。验证：`go test -count=1 ./internal/pipeline/...`
- [x] T002 接入 `sentinel pipeline` CLI、输出目录门禁与 JSON 写出，复用现有影子库连接和受限 candidate gate。验证：`go run ./cmd/sentinel pipeline --help`、`go build ./...`
- [x] T003 在既有健康影子库真实运行四步骤加汇总，另以 `SELECT ... FOR UPDATE` 复现第一步拒绝并更新 README。验证：成功输出四个 completed steps、五个摘要；拒绝输出仅 sql admission/summary、`locking_read` 与 exit 1（真实记录见 plan）。
- [x] T004 运行全量格式/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`。验证：`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 与 Adaptive Spec 校验均通过。
