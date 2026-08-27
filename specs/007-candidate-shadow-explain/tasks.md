# 任务

- [x] T001 实现纯输入门禁与 L1 报告模型，复用 SQL/CandidateSpec 校验以阻断自由 SQL/DDL。验证：`go test ./internal/shadowgate/...`
- [x] T002 实现 candidate schema、索引定义与快照门禁，保证 baseline 零 DDL。验证：`go test ./internal/shadowgate/...`
- [x] T003 接 `candidate-explain` CLI、真实 JSON 文件和影子库 EXPLAIN 流程。验证：`go run ./cmd/sentinel candidate-explain --help`
- [x] T004 更新 README，运行全量验证、规格校验、私有复盘并提交。验证：`go test -count=1 ./...`
