# 批量轮次测量任务

## 任务

- [x] **T001** 在 `internal/measure` 实现“执行 N 次完整查询后折算单次耗时”的唯一循环内核；拒绝 N≤0，任一次执行错误或 `ctx.Err()` 后立刻停止。新增纯逻辑测试覆盖 N=1、N=3、失败短路、取消短路与非法 N。验证：`go test -count=1 ./internal/measure/...`
- [x] **T002** 将 `Session.Warmup`、`Session.Once` 与 `RunOptions` 接入 executions-per-round，保持 prepared statement 复用且每次完整消费结果集。验证：`go build ./... && go vet ./... && go test -count=1 ./...`
- [x] **T003** 为 `bench` 增加 `--executions-per-round`（默认 1）并让预热、标定、正式 B→C 测量均使用同一个值；非法值明确报错。验证：`go run ./cmd/sentinel bench --help` 与 `go run ./cmd/sentinel bench --executions-per-round 0`。
- [x] **T004** 在 `report.Result` 增加 `measurement_protocol.executions_per_round`，JSON 与终端摘要均输出；新增报告测试，且确认 `rounds=0` 仍不输出方向 verdict。验证：`go test -count=1 ./internal/report/...`
- [x] **T005** 完成代码级回归：`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、Adaptive Spec 校验。验证：上述命令均通过。
- [x] **T006** 真实实验前记录运行容器，并仅临时停止已获许可的 `astrbot`、`napcat`、`mysql`；确认 100 万行双库快照与候选索引仍在。先跑 N=1 一次对照，再连续跑两次 N=10、20 轮、只标定的报告。验证：所有门禁通过，容器数据不删除。
- [x] **T007** 在相同 N=10 协议下跑一次正式 5 轮 A/B 测量；恢复 T006 停止的容器，读取三份新报告并按既有 admission 规则记录 L1/L2 结论。验证：恢复结果与报告字段可复核。
- [x] **T008** 将真实命令、协议含义和实验结论写回 README 与 `plan.md`；不改历史 JSON。验证：全量 Go 检查与 Adaptive Spec 校验通过。

## 测试克制说明

只新增三类测试：批量循环的次数/短路边界（防止把部分样本当成功）、报告协议字段（防止消费者不知道 N）、`rounds=0` 的无方向结论（防止伪造事实）。不为 CLI flag 解析或普通 getter 堆参数化测试。
