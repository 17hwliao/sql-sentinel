# Feature Specification: Kafka 异步工作流补充模块

**Feature**: `018-kafka-async-workflow`

## 目标

在既有 HMAC webhook、SQL Admit、shadowgate 和 pipeline 之上增加可恢复的异步执行边界。Webhook/接入层只负责把原始 SQL 与 CandidateSpec 写入 control plane；Kafka 只传递不含敏感业务内容的任务引用。

## 必须行为

1. `delivery_id` 在 control plane 上有唯一约束；重复投递返回同一个 job，不重复创建 outbox 或执行 pipeline。
2. job 状态覆盖 `RECEIVED`、`QUEUED`、`RUNNING`、`COMPLETED`、`REJECTED`、`RETRYING`、`DEAD_LETTER`。worker 使用带 owner/expiry 的 CAS lease，过期任务可被另一个 worker 接管。
3. job 与 outbox 在同一个 MySQL 事务中创建。relay 先 publish 后标记 outbox；崩溃窗口允许重复 publish，不允许静默丢失。
4. Kafka 使用 at-least-once 语义。event_id、job_id、delivery_id 和 DB 状态/lease 共同实现重复消费幂等；pipeline 成功后重复消息不再次执行。
5. worker 错误使用指数退避；达到最大尝试次数写入 `DEAD_LETTER`，明确拒绝写入 `REJECTED`。任何状态更新失败都不会伪装为成功消费。
6. Kafka payload 只允许 schema version、event_id、job_id、delivery_id、attempt；不得出现 secret、raw key、完整 prompt 或 SSE delta。状态查询只序列化摘要、哈希和状态，不返回 SQL/CandidateSpec。
7. 影子 MySQL 的 pipeline runner 仍由调用方注入，并受现有 `max-concurrent=1..4` 约束；本模块不创建无界 goroutine/队列，不改变 SQL Admit、shadowgate 或证据等级裁决。
8. 新增独立 `deployments/kafka-compose.yml`，使用单独 Compose project、control-plane 端口 13308、Kafka 端口 19092 和独立 volume；不得修改 baseline/candidate Compose 的服务或参数。

## 非目标

- 不把 SQL、prompt、模型输出、密钥或 SSE 流复制到 Kafka、日志或状态 API。
- 不把 L1/计划证据升级为性能收益；`eligible_for_performance_claim` 仍由既有 pipeline 语义决定。
- 不在没有明确 Kafka/DB 连接配置时自动启动或改动用户已有容器。

## 验收

- `go test -count=1 ./...`、`go build ./...`、`go vet ./...`、`gofmt` 和 diff 检查通过。
- 离线测试证明唯一 delivery、safe event、relay crash window、lease/CAS、retry/backoff、DLQ、状态脱敏和 at-least-once 重复消费。
- 真实 Kafka/MySQL 只在用户显式启动专用 compose 且提供连接配置后验证；否则报告 controlled refusal，不声称真实集成已验证。
