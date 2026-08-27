# 任务

- [x] T001 实现 EXPLAIN JSON 摘要与计划差异纯函数，覆盖未知节点、filesort 和多表安全边界。验证：`go test ./internal/plancompare/...`
- [x] T002 接 `explain-compare` CLI，复用 shadowgate 门禁并在 baseline/candidate 仅执行 EXPLAIN。验证：`go run ./cmd/sentinel explain-compare --help`
- [x] T003 新增示例并在现有影子库生成真实对照报告，更新 README。验证：`go run ./cmd/sentinel explain-compare --sql examples/candidate-explain-query.sql --candidate examples/candidate-explain-index.json --out explain_comparison_report.json`
- [x] T004 运行全量验证、规格校验、私有复盘并提交。验证：`go test -count=1 ./...`
