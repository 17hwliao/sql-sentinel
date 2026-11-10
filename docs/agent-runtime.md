# SQL 专项 Agent v1：运行契约

2026-11-06。执行循环在 SQL Sentinel 内；模型通过既有 AgentMesh OpenAI 兼容入口调用。AgentMesh 业务代码没有因本轮修改。真实模型、真实编程客户端和数据库的证据需分别看验收记录，不由架构图推定。

## 模式与入口

原固定流程为默认值。旧请求、旧任务、手动 CandidateSpec、SQL 改写和审批后的测量不切换为 Agent。创建自动索引任务时显式指定：

```json
{"delivery_id":"stable_request_001","pr_number":1,"sql":"SELECT id FROM orders WHERE user_id=42 LIMIT 20","execution_mode":"agent.v1"}
```

`POST /jobs` 仍使用受限 writer 身份。省略候选才表示自动生成；Agent 与手工候选不能混用。模式、版本、SQL、授权表及预算创建后绑定到持久任务，改变 Worker 部署配置不会改变旧任务。重复请求沿用原幂等冲突检查，不对旧 mode 自动填入新默认值。

原 `/jobs/{delivery_id}` 继续有效。内部 job_id 使用独立 `/jobs/by-id/{job_id}` 命名空间；两者不能按查询先后次序猜测，否则合法幂等 ID 可能碰撞。各自均支持 `/report`、`/approval`、`/benchmark` 及显式 `POST .../cancel`。读取仍检查 tenant/project/table；取消还要求创建者或同域 admin，reader/reviewer 没有取消权限。

网页选择“Agent”后走同一任务服务。PR Webhook 仍只处理原先支持的 `.sql` 文件，不新增通用代码 SQL 提取。默认保持固定流程；API 服务设置 `SQL_SENTINEL_GITHUB_AGENT_MODE=true` 后，新建的已签名、仓库白名单内任务绑定 `agent.v1` 和服务端表白名单。既有任务不会升级；本轮没有向真实 PR 发评论。

## 决策、工具与证据

1. 当前任务初始取证：基线 SQL、两侧 Schema/已有索引、MySQL 版本、数据快照、基线 EXPLAIN。
2. 模型返回严格 JSON 动作。系统规则与不可信 SQL/证据分开传递，后端同时执行字段、授权、证据和预算检查；安全不依赖模型遵守提示。
3. 后端执行 `inspect_schema`、可选 `search_history`、`evaluate_index` 或 `finish`。只有当前任务的授权表；不接受自由 SQL、连接、Shell 或网络目标。
4. 验证结果转换成有界的结构化观察再交给模型。工具验证失败可成为下一候选的依据；锁、清理、快照、Schema、版本或基础设施失败则停止，不能包装为“无收益”。
5. `finish` 只能选择当前任务／attempt 中真实验证过、候选 hash 和上下文一致的观察；也允许无候选正常结束。

候选规范 hash 使用表和有序列／方向，忽略索引名称；改名不能规避重复限制。实际索引名由服务端产生。`evaluate_index` 复用既有独立连接、命名锁、Gate、快照和清理，返回前完成清理；锁释放失败同样视为基础设施失败，不继续探索。

首版只输出 **L1 计划证据**。选中索引、排序或估算行数变化不是实测性能收益。Agent 不自动创建测量任务；L2/L3 继续走原报告 hash、候选 hash、SQL hash、独立审批及测量流程。无候选、未完成验证或非推荐终态不能审批。

Eino 实际承担现有模型适配和单节点 Compose 调度／callback；观察循环由新增 Go 引擎执行。原 `DraftBounded` 仍是最多三次候选准入修正，没有被重命名为自主循环。

## 持久预算、租约与恢复

- 一个持久 Agent 任务最多 **5 次模型决策、2 次候选验证**，跨 Worker 重试不重置。每次派发前在 MySQL 事务内核对当前状态、owner、attempt、租约、不可变参数及原受限身份 session，并原子预留。
- 畸形输出、模型请求失败和结果未知均消耗模型调用；没有隐式“免费格式修复”。候选验证派发后即消耗一次，即使 SQL 准入／执行失败。调用次数不是 token 或结算金额。
- 决策单次最多 12 秒；Agent 模型输出限制 1024 token。取证／验证各有工具截止时间，任务仍受原约 120 秒工作期限约束；清理最多额外 10 秒，保存部分报告最多额外 5 秒。预算是上限，不能保证在慢依赖下用满所有次数。
- 每个工具前后、影子 SQL 操作和运行 watchdog 持续核对 fence；正在执行的数据库请求也会随取消中断。原身份 session 撤销／过期后不继续计费决策或验证。此方案不承诺分布式撤权零延迟或远程模型 exactly-once。
- 无逐节点 checkpoint。崩溃后从安全起点取证，已有持久预留保留；原默认最多 3 个 durable attempt，因此 Agent 全任务仍不超过 5 次模型／2 次验证，而非每次重试各 5／2。
- 报告保存再次锁定 job 行检查 owner/attempt/租约。旧执行器不能覆盖新报告；选中候选保存前重新核对上下文。报告替换会使旧批准失效。
- 显式取消写入 `CANCELLED`，停止认领／续租、清除所有最终选择并保留历史观察。取消客户端等待、网页离开、MCP 断开均不自动取消持久任务。

默认固定模型路径最多 3 次准入修正／attempt，默认 3 次 durable attempt，和新 Agent 持久总预算是两种不同契约。不要将原路径说成具有新的全任务调用上限。

## 兼容迁移与回滚

新增表 `sql_sentinel_agent_usage(job_id, model_calls, evaluations)`，由现有 `EnsureSchema` 和更新后的部署 schema 创建；没有修改旧任务表字段或重写旧 options。新增 options 字段仅新 Agent 任务使用，计数不塞进幂等参数。该表按 job 主键访问，每任务至多一行；归档时与原 job 的长期幂等保留策略一致，不能在任务仍可重试时删计数以“释放”预算。

升级顺序：先备份控制库并运行 schema，升级 API、Worker、Verifier，再开放显式 Agent 提交。回滚时先关闭新 Agent 提交／Webhook 开关，等待或显式取消未完成 Agent 任务；旧 Worker 不理解新模式，不能让其认领仍待执行的新任务。保留新增表和新报告，旧任务可继续原流程；不要自动删表或退回使用默认配置来改变已创建任务语义。

## 报告、网关与配置

报告追加 `mode/version/outcome/stop_reason/agent/agent_trace/agent_usage`；旧报告仍可读。轨迹只保存动作、结构化事实及短公开结论，不保存内部思维链、完整模型提示或原始数据库／提供商错误。授权报告里原有必要 SQL 的访问边界保持原状。

网页显示完成／部分报告；没有实时节点流。用量仅显示真正取得的本次计数与持久预留数。未取得 token／账单数据时明确未知，不显示虚构 0 元。

模型仍由 Verifier／Runner 使用 `EINO_BASE_URL`、`EINO_MODEL`、专用受限 `OPENAI_API_KEY_FILE` 等既有秘密文件配置调用 AgentMesh；不要把管理 bootstrap Key 放入 MCP。现有 traceparent/tracestate 与 job span 的 job.id/attempt 属性用于关联请求；关联标识不是授权凭证，也不将完整 Agent 轨迹送入网关。

历史验收状态截至 2026-10-23 记录累计估算／占用 8,234,375 nano-CNY（约 0.008234375 元）；更早单个账本快照不覆盖全部后续调用，供应商最终账单未核实。这不是本轮恢复出的实时余额。原 20 元总预算不重置；当前路由／价格／剩余额度未完成新核验前，真实请求硬停止。本轮使用 fixture，不新增付费请求。

可复现的数据库闭环（Docker 已运行并已有官方 `mysql:8.4.11` 镜像）：

```sh
python scripts/verify-agent.py --evidence-dir /absolute/private/agent-evidence
```

脚本仅创建随机命名、loopback 端口和 tmpfs 数据的独立 MySQL，自动制备三个测试 schema，运行实际 SQL 验证／报告／取消反例并清理自己创建的容器；私有密码和 DSN 使用临时文件，不接触现有数据库或模型密钥。输出 `agent-result.json` 和 JSON 测试日志。模型为本地 OpenAI 兼容 fixture，outbox 事件直接交给 Worker.Handle，不把它当成真实模型或 Kafka broker 验收。

本地 stdio 工具配置与协议见 [MCP](mcp.md)，对照评估见 [评估记录](agent-evaluation.md)。真实邮箱、云部署、生产数据库与简历不属于本轮变更。
