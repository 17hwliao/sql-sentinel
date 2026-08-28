# 任务

- [x] T001 实现证据包严格读取、SHA-256、固定 L1 报告、CandidateSpec 严格校验和有上限重试；用假节点覆盖合格、未知字段/DDL、尾随 JSON、耗尽重试与含提示注入文本的证据包，且注入文本绝不落盘。验证：`go test -count=1 ./internal/agentgraph/...`
- [x] T002 用 Eino Compose Graph 适配可注入节点并接 `propose-candidate` CLI；精确锁定 Eino/OpenAI adapter 依赖，不发起真实模型调用。验证：`go test -count=1 ./internal/agentgraph/...`、`go run ./cmd/sentinel propose-candidate --help`、`go build ./...`
- [x] T003 仅在环境凭据齐全时以真实 OpenAI 兼容模型运行一份 012 证据包，记录模型名、尝试数、严格验证结果并更新 README；无凭据则如实记录受控拒绝。验证：`go run ./cmd/sentinel propose-candidate --provider openai --evidence-dir PATH --out candidate_proposal_report.json`（本机环境变量缺失，受控拒绝 `runtime_model_configuration_missing`，未发起网络调用。）
- [x] T004 运行全量格式/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`。验证：`gofmt -l .`、`go test -count=1 ./...`
