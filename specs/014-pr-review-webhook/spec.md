---
level: L3
feature: 014-pr-review-webhook
created: 2026-08-28
---

# 本地 PR 评审 Webhook

## 目标

- 新增本地 HTTP `POST /webhook/pr`：以 HMAC-SHA256 验证 `X-SQL-Sentinel-Signature`（`sha256=<hex>`）；密钥只从 `SQL_SENTINEL_WEBHOOK_SECRET` 读取，并用常量时间比较。签名非法只返回稳定拒绝码，绝不 JSON 解码、提取 SQL、连接影子库或落盘。
- 接收最小模拟 PR JSON（`delivery_id`、`pr_number`、unified `diff`）；只从新增的 `.sql` 文件行提取 SQL 文本。每条提取文本先经 `sqladmit`；拒绝会写入本地评论/汇总，绝不进入影子库。
- 每个有效 delivery 最多接受一条 SQL 和一次 `pipeline.Run`；启动时从 `--candidate` 严格读取 CandidateSpec，PR JSON 不能提供或改变候选索引。输出每个 delivery 的评论文本与机器可读汇总。
- 评论包含验证状态、`evidence_level`、真实拒绝码与管线工件 SHA-256；资格字段只转述真实报告，当前链路固定为 `eligible_for_performance_claim=false`。

## 非目标

- 不接真实 GitHub、OAuth、PR API、远程评论、持久化 delivery 去重、消息队列或 Agent；模拟事件与评论均只在本地文件系统。
- 不执行原 SQL、DDL、测量、系列准入、evidence binding 或自动采纳 CandidateSpec；只复用 012 的只读影子 EXPLAIN 管线。
- 不在影子库不可用、签名/JSON/SQL/CandidateSpec 门禁失败时静默通过或声称性能收益。

## 验收

1. 无效 HMAC、缺失/超限 body 均以稳定码拒绝，且测试证明 JSON 解码、SQL 提取、管线和文件写入都未发生。
2. 模拟 diff 中每条 SQL 都先经 `sqladmit`；`FOR UPDATE`、多语句、零条或多条候选 SQL 均生成“验证不可用”的本地评论/汇总，零次影子库调用。
3. 合法的已签名单 SQL delivery 仅执行一次 `pipeline.Run`，落盘评论和汇总；评论列出 L1、false、已完成步骤/拒绝码及每项 artifact SHA-256。
4. 服务器以有界、非阻塞 semaphore 限制同时验证数（默认 1、范围 1–4）；满载返回 `queue_full`/429，绝不积压无界请求或并发打影子库。
5. 影子库或候选门禁不可用时，评论明确写 `verification_unavailable` 和稳定原因码；不输出通过、L2 或虚假的资格字段。

## 默认假设

- 请求 body 上限 1 MiB；签名在受限读取后、任何语义处理前验证。delivery ID 仅允许安全文件名字符，已存在的 delivery 输出目录拒绝覆盖。
- `webhook-serve` 只允许监听 `127.0.0.1`（默认亦为该地址）；非回环 `--listen` 参数在启动前拒绝，服务绝不暴露到局域网或公网。
- 仅支持 unified diff 的新增 `.sql` 行；这是文本提取而非 SQL 执行或完整 Git diff 解析。SQL、路径和 PR 元数据均是不可信数据，不写入控制指令。
- `--candidate` 是服务器本地、严格验证的只读文件；每个 delivery 独享空的 `artifacts/` 目录，`comment.md` 和 `webhook_report.json` 位于其外层目录。
