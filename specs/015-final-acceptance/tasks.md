# 任务

- [x] T001 新建 `examples/evaluation/` 七例 manifest/SQL 与 `internal/evaluation`：严格样例读取、sqladmit 检出率、历史测量摘要及固定 L1 报告模型；为 pipeline 已执行步骤增加 wall-clock 时序。验证：`go test -count=1 ./internal/evaluation/...`、`go test -count=1 ./internal/pipeline/...`
- [x] T002 接 `sentinel evaluate`：固定安全测试包清单、严格输入/输出和稳定豁免字段；测试 CLI 配置拒绝及安全测试结果记录。验证：`go test -count=1 ./cmd/sentinel/...`、`go run ./cmd/sentinel evaluate --help`、`go build ./...`
- [x] T003 运行评测、固定安全测试及一次新的本机 HMAC webhook smoke，写 `evaluation_report.json` 并重写 README §11；复算工件 SHA-256，记录时序、单 Case 局限与每项豁免。验证：评测报告、评论、四项摘要和 HTTP 状态均可检查。（实际 7/7、五组安全测试 passed、HTTP 202、四步骤/五工件。）
- [x] T004 全量格式/build/vet/test、Adaptive Spec 校验、私有最终复盘、本地提交并 fast-forward `master`；不 push/tag。验证：`gofmt -l .`、`go test -count=1 ./...`、`git diff --check`
