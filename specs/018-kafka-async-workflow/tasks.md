# Tasks

- [x] T001 固定安全事件模型、状态集合、Store/Publisher/Consumer 接口和指数退避。
- [x] T002 实现 MySQL durable store：delivery/event 唯一约束、job+outbox 事务、CAS lease 和状态更新。
- [x] T003 实现离线 MemoryStore、relay、worker、Kafka writer/reader 适配器和安全状态 handler。
- [x] T004 增加独立 Kafka/KRaft + control-plane Compose 与 schema.sql，避免干扰 baseline/candidate。
- [x] T005 增加重复 delivery、payload 脱敏、重复消费、lease、retry/backoff、DLQ 和状态查询测试。
- [x] T006 执行 `gofmt`、`go test -count=1 ./...`、`go build ./...`、`go vet ./...` 与工作树 diff 检查；记录真实 Kafka/DB 或 controlled refusal 边界。
