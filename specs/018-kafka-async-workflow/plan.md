# Implementation Plan

1. `internal/kafka/model.go` 固定状态、引用事件、Store/Publisher/Consumer 接口和退避算法；事件采用严格 JSON 解码，防止字段漂移和敏感字段进入协议。
2. `internal/kafka/mysql.go` 实现 InnoDB control plane、delivery/event 唯一约束、事务性 job+outbox、CAS lease、完成/拒绝/重试/DLQ 更新和 ready outbox 查询。
3. `internal/kafka/memory.go` 提供同样规则的确定性离线 store；`runtime.go` 提供 relay、worker、状态 handler、MemoryBroker 与 `segmentio/kafka-go` 适配器。
4. 用独立 compose 启动 Kafka KRaft、control-plane MySQL 和 topic init；baseline/candidate 测量 compose 保持不变。真实验证缺少容器/凭据时只记录受控拒绝。
5. 测试从 delivery 去重开始，覆盖 relay publish-after-mark、lease 接管/重复消费、retry 到 DLQ、拒绝和状态响应脱敏；最后执行全量 Go 验证与 diff 检查。

## 数据流

`HMAC webhook -> MySQL(job + outbox, one tx) -> relay -> Kafka(reference event) -> worker(CAS lease) -> injected pipeline -> MySQL status`

Kafka 重复或 relay 在 publish 后崩溃时，DB lease/status 阻止重复 pipeline；worker commit 发生在状态处理成功之后。
