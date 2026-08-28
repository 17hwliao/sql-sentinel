# 计划

1. 新建 `internal/webhook`：受限 body 的 HMAC 验证、最小事件严格解码、统一 diff 文本提取、逐条 `sqladmit`、本地汇总/评论渲染。认证失败在任何 JSON/SQL/文件副作用前返回；提取失败或准入拒绝以 `verification_unavailable` 写出可审计结果。
2. 定义可注入的单次 `PipelineRunner`；只有“签名通过 + 恰一条准入 SQL + 本地 CandidateSpec 已验证”才调用一次。runner 报错或 L1 拒绝均渲染真实状态、拒绝码及 pipeline artifacts 的 SHA-256，绝不自行升级资格。
3. `webhook-serve` CLI 只绑定 `127.0.0.1`，负责密钥/候选/输出根目录参数、`--max-concurrent=1..4` 和无等待 semaphore；生产 runner 复用 012 `pipeline.Run` 的 shadowgate/EXPLAIN 路径，测试注入固定 runner，不连接 MySQL。
4. 任务顺序：纯安全入口与离线测试 → 可启动服务、并发上限和模拟 HTTP 事件 → 运行中影子库的真实（或如实不可用）单 delivery 与 README → 全量验证、复盘、提交和 master fast-forward。
5. 风险与降级：签名、格式、SQL、候选、影子库或输出失败均不代表“通过”；只返回/落盘 `verification_unavailable` 与稳定码。无界队列会使影子库互相干扰，因此满载立即 429。章程：暂未建立。

真实实验记录（T003）：2026-08-28 在既有可达的两侧影子库上，以一次性本地密钥启动
`127.0.0.1:18081`，向 `/webhook/pr` 投递使用已有 candidate EXPLAIN SQL/Spec 的模拟 diff。HTTP 202；delivery
`pr014-real` 的评论为 `verification_completed`、L1/false，顺序完成四个步骤，记录五项 artifacts。前四份文件字节
SHA-256 逐项复算匹配；summary 使用既有的 `pipeline_report_with_own_sha256_blank` canonical 范围。服务随后停止，未操作 Docker 或真实 GitHub。
