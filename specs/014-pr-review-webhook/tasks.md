# 任务

- [x] T001 新建 `internal/webhook` 的签名门禁、严格最小事件解码、`.sql` 新增行提取、逐条 `sqladmit` 与评论/汇总模型；测试无效签名零处理、body 上限、锁定读/多 SQL 零 runner、L1 SHA-256 评论。验证：`go test -count=1 ./internal/webhook/...`
- [x] T002 接仅允许 `127.0.0.1` 的 `sentinel webhook-serve`：本地 CandidateSpec 预检、受限输出目录、可注入 runner 和默认 1 的非阻塞并发 semaphore；测试模拟签名 HTTP delivery、一次 runner 调用、429 `queue_full` 与影子故障降级。验证：`go test -count=1 ./cmd/sentinel/...`、`go run ./cmd/sentinel webhook-serve --help`
- [x] T003 用已有 `examples/` SQL/CandidateSpec 和本地模拟 PR JSON启动服务，签名投递一次；记录真实五工件/评论，或将影子库不可用如实落为 `verification_unavailable`，并更新 README。验证：本地 HTTP POST 后检查评论、汇总、拒绝码与 artifact SHA-256。（实际 HTTP 202，四步骤完成、五项 artifact；前四项 SHA-256 复算匹配。）
- [x] T004 全量格式/build/vet/test、Adaptive Spec 校验、私有阶段复盘、本地提交并 fast-forward `master`。验证：`gofmt -l .`、`go test -count=1 ./...`、`git diff --check`
