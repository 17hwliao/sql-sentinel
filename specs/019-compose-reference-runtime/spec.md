# SQL Sentinel 单机参考运行时

## 定位

本规格将已有的 Webhook、MySQL control plane、Kafka Outbox、CAS lease
Worker 与安全 SQL pipeline 装配为可部署、可恢复、可验证的单机 Docker
Compose 参考架构。它不是公网生产级部署声明：真实 GitHub、生产数据库、
TLS、身份提供商和多可用区仍是部署方的责任。

## 启动拓扑

```text
control-plane MySQL + Kafka + topic-init + migration-init
  -> webhook-api + kafka-relay + kafka-worker
```

所有主机端口仅绑定 `127.0.0.1`。Webhook 使用显式配置的测试 HMAC secret；
影子 MySQL runner 是显式选择的 mock/controlled runner，默认不得对外部
数据库执行 SQL。

## 必须行为

1. `docker compose up --build` 可启动 MySQL、Kafka、topic-init、迁移、API、
   Relay、Worker，且依赖按健康状态和迁移成功状态排序。
2. API 验证 HMAC、SQL Admit，并在一个 MySQL 事务中写入 job、history 和
   outbox；Kafka 事件仍只有 ID 引用。
3. Relay 是可取消的长期轮询进程：每次扫描 `available_at` 到期的 Outbox，
   publish 后才标记成功。失败增加 attempts 并退避，Kafka 恢复后自动投递。
4. Worker 是独立 consumer group。它使用现有 MySQL owner/lease CAS，重复
   事件或过期 lease 不得重复执行完成的 job；重试耗尽得到 `DEAD_LETTER`。
5. 提供 loopback health/readiness、受独立管理员凭证保护的安全 job/Outbox
   汇总，以及按 delivery ID 查询的既有脱敏 job 状态。
6. 自动化验收覆盖正常完成、Kafka 不可用后恢复、重复事件幂等、Worker
   重试/DLQ 与安全状态不泄露 SQL/CandidateSpec。

## 非目标与后续边界

- 不把原始 SQL、CandidateSpec、HMAC secret 或模型内容写入 Kafka、日志或
  管理摘要。
- 不让 Compose 默认跑真实影子库的 DDL/测量；这要求明确、隔离的测试数据
  库和操作者批准。
- 多副本 API、Relay、Worker 的连接凭证轮换、TLS、Redis 限流和云端 HA
  是下一阶段；当前 worker 的 job CAS lease 已是多 Worker 的正确基础。

## 验收证据

`go test -count=1 ./...`、`go vet ./...`、`go build -buildvcs=false ./...`、
Compose 配置校验、容器启动、以及一份自动化端到端验证输出均须通过。任一
真实容器验证受本机 Docker 镜像和网络条件阻碍时，必须如实记录，不得以
静态校验替代。
