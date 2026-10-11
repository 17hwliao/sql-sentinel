> **2026-10-08 扩展版本**：当前运行契约以 [后端扩展说明](backend-expansion.md) 为准。以下早期阶段记录保留历史口径，不能用其未勾选任务或旧能力限制判断本次交付。实际验收与剩余环境条件见联合发布包报告。

# 当前后端契约（2026-10）

联合发布包的 `deploy/README.md` 是上线运行手册。生产入口分别为 api、kafka-relay、kafka-worker、migrate；旧 sentinel CLI、旧本机 webhook 契约和实验代码保留用于演示，不通过生产 HTTP 入口调用。生产 webhook 不拼新增行，而是读取固定 head 的完整 `.sql` 文件。

## 范围与证据

当前限定固定 orders-v1 schema、独立 baseline/candidate 两个影子实例和同一数据快照。baseline 身份只读；candidate 身份只有 SELECT/INSERT/INDEX（INSERT 用于 ANALYZE 所需权限），没有生产 DSN。确定性 Go 代码验证只读输入与 CandidateSpec 后，才在 candidate 执行安全编译出的索引 DDL。用户与模型不能提供任意 DDL。共享影子数据库使用 MySQL 命名锁串行验证；下一任务清理崩溃残留索引，本次结束也清理索引。

支持单条 SELECT/WITH，拒绝写语句、锁定读、输出文件、可执行注释和多语句。原生 PR 自动入口扫描 `.sql` 中受影响语句；不支持 Go Raw SQL、ORM 推导、生产 migration DDL、任意业务 schema 或自动变更生产数据库。其他类型会计数为 ignored。该边界应保留在求职演示中。

任务生成候选使用 Eino Draft Graph 单节点，包含 baseline EXPLAIN 与固定 schema 提示；最多 3 次候选格式/安全验证，输出最多 64 KiB。它不是任意工具选择 Agent。报告包含完整 snapshot gate、candidate EXPLAIN、plan comparison 与 diagnosis 工件，是 L1 证据，`eligible_for_performance_claim:false`。独立 benchmark 工具存在不代表自动后台链路已完成 L2/L3。

## 接口

公网前缀 `/sentinel` 由 Caddy 剥离，服务内接口如下：

| 接口 | 行为 |
|---|---|
| `GET /healthz`、`GET /readyz` | 进程存活；控制数据库故障 readyz=503 |
| `POST /jobs` | Bearer SQL_SENTINEL_API_TOKEN；128 KiB JSON；返回 202 持久入队，相同 delivery_id/PR/SQL/candidate 返回同任务；改变输入复用 ID 返回 409 |
| `GET /jobs/{delivery_id}` | 同 token；安全状态、重试次数等；不返回模型密钥或原始错误 |
| `GET /jobs/{delivery_id}/report` | 同 token；终态时返回 JSON 工件，尚未产生报告返回 404 |
| `POST /webhooks/github` | 原生 X-Hub-Signature-256 验证 raw body，X-GitHub-Event/X-GitHub-Delivery；不使用手工 Bearer |

手工输入：

```json
{"delivery_id":"review-001","pr_number":1,"sql":"SELECT id FROM orders WHERE user_id=42 LIMIT 20"}
```

省略 candidate_spec 由模型提案。显式候选形状为 `{table:"orders",index_name:"idx_cand_manual",columns:[{name:"user_id",direction:"ASC"}]}`，可包含安全现有列与 ASC/DESC。请求 SQL 最多 64 KiB，delivery_id 限制为 1–64 位字母、数字、下划线/短横线。API 全局并发 8、处理上下文 25 秒，后台任务超时 2 分钟，报告最大 2 MiB。

## 可靠性与 GitHub

任务、outbox、历史、源码来源和报告持久保存。每次抢占生成独立 owner；只有 owner 和有效 lease 可以续期、保存报告或终结任务。活跃 lease 抢占失败不会提交 Kafka offset。Worker 对当前消息重试，不会越过失败消息；超期任务及已发布后长期未处理任务由 relay 重新入队，重复事件无副作用。默认 lease 3 分钟、按 lease/3 续期；验收覆盖 SIGKILL 后 lease 过期恢复。毒消息仅保存安全摘要。

GitHub 必须配置仓库白名单、webhook secret、实际 token。官方 API 读取当前 PR/head/repository，然后按固定 SHA 获取完整内容；不跟随事件下载 URL 或 HTTP 重定向。文件列表最多 10×100，文件内容最多 256 KiB（响应 1 MiB），单事件最多 20 个语句任务。受影响修改文件没有 patch 时拒绝为手工审核。允许 opened/synchronize/reopened/ready_for_review；过期 head 事件不入队。

`GITHUB_PUBLISH_COMMENTS` 默认 false。开启后每 15 秒处理终态任务，并重新确认 head；过期报告标记 superseded。评论标记 `sql-sentinel:job_id` 支持发布后数据库确认失败的幂等更新。评论仅包含状态、来源和 L1 边界，完整 SQL/工件需认证查看。本次没有向真实 GitHub 发评论；本机验收使用协议 fixture。

当前服务是单组织、单操作员 API token 授权模型，不是多组织 SaaS。任务和报告包含 SQL 与执行计划，要按私有数据管理备份。数据保留/归档与资源扩容由运行手册说明；迁移不自动删除任务历史。多租户组织权限、通用 schema 与真实工作负载评测需要后续产品开发，不能在演示中宣传为已完成。
