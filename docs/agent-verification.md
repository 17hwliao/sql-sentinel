# Agent v1 本地验收记录

日期：2026-11-06。基线提交 `941ecb8ef629bdc102f9d5c2156600fd47c53422`；继续使用原 `master` 工作区，没有提交、推送、PR、标签或 Release。此前未提交的 Casdoor／前端工作保留；本轮没有修改 AgentMesh 业务代码，也没有恢复邮件、云部署或简历任务。

## 交付范围

显式 `execution_mode=agent.v1` 的受限 SQL Agent 已接入现有任务服务：初始取证 → 模型动作 → 准入与授权／租约／预算检查 → 工具执行 → 结构化观察 → 再决策或结束。可根据验证失败调整第二个候选，也可以合法地不给建议。默认固定流程、手动候选、SQL 改写及审批后测量仍使用原路径。

| 入口或能力 | 源码及使用说明 |
| --- | --- |
| 严格动作协议、循环、候选规范 hash | `internal/agentgraph/loop_contract.go`、`loop_engine.go` |
| 实际 Schema／EXPLAIN／候选验证 | `internal/service/agent_tools.go` |
| 执行、身份撤销、部分报告及最终审批绑定 | `internal/service/agent_runner.go`、`agent_report.go` |
| 跨重试预算、取消及持久状态 | `internal/kafka/agent.go`、`runtime.go`；新增表见 `deployments/kafka/schema.sql` |
| API／可选 Webhook | `internal/service/server.go`、`agent_webhook.go`；API 契约见 `docs/openapi.json` |
| 本地 MCP 四工具 | `cmd/sentinel-mcp/`、`internal/mcptool/`；配置见 [MCP 手册](mcp.md) |
| 前端显式模式和可折叠报告 | `web/src/product.js`、`agentreport.js`；旧格式、空候选、部分结果和身份切换兼容 |

每个持久任务上限为 5 次模型决策、2 次候选验证，跨 attempt 保留预留。只提供 L1 计划证据，不能自动跳过审批生成 L2/L3 性能结论。完整配置、取消语义、迁移和回滚见 [运行契约](agent-runtime.md)。

## 实际验证结果

以下数字统计测试项与子测试，不代表独立业务场景数。全量与后续定向测试重叠，不能相加为总覆盖数。

| 检查 | 结果与适用范围 | 本地原始证据文件 |
| --- | --- | --- |
| 全量 Go 测试 | 401 PASS，0 FAIL，0 SKIP；包含隔离真实 MySQL。运行后又修复 R-B8，随后按关联模块定向复核 | `go-tests-final.jsonl` |
| 全量 Linux `-race` | 同版本 401 PASS，0 FAIL，0 SKIP；源码拷贝哈希匹配 | `go-linux-race-final.jsonl`、`linux-race-result.json` |
| 最终关联模块普通测试 | R-B8 修复后 `internal/agentgraph`、`internal/service` 共 148 PASS，0 FAIL，0 SKIP | `affected-context-budget-final.jsonl` |
| 最终关联模块 Linux `-race` | 最终代码同两模块 148 PASS，0 FAIL，0 SKIP；源码在运行期间未变化 | `go-linux-race-affected-final.jsonl`、`linux-race-affected-result.json` |
| 静态检查 | 全量 `go vet ./...` 通过；最终 R-B8 后对上述两模块再次通过 | `go-vet.log` 与执行回执 |
| 前端自动测试／构建 | 68 PASS，0 FAIL，0 SKIP；Vite 构建成功，含新增 BFF 路由及 CSRF 检查 | `web-tests-final.log`、`web-build-final.log` |
| MCP 协议 | 官方 Go SDK 启动真实 stdio 子进程，完成握手、工具发现和四工具调用；8 项业务测试及 1 项子进程辅助测试通过。HTTP 业务服务为 fixture | `mcp-protocol.log` |
| 实际 OpenCode 1.19.0 | 隔离配置执行 `opencode mcp list --pure --print-logs --log-level DEBUG`，退出 0，显示 `sql_sentinel connected`；业务 HTTP 请求 0 | `opencode-client-result.json` 及对应日志 |
| 依赖检查 | `compress` 升至 `v1.20.2` 后，Windows／Linux `govulncheck` 代码可达漏洞 0；另有 2 条未被代码调用的模块级提示 | `govulncheck-final.log`、`govulncheck-linux-final.log` |

原始日志保留在仓库外本地验收目录 `../validation/sentinel-agent-2026-11-06/`，其中 `private/` 是测试专用密码／DSN，不应上传或打包。此处列出的日志名用于定位本地证据，不宣称公开仓库包含这些私有文件。

本机 Windows race 编译遇到 cgo 工具链问题；最终使用 Linux Go 1.26.10＋GCC、当前锁定依赖的离线 vendor 和仅连接测试 MySQL 的内部网络完成 race。未把 Windows 编译失败改写为通过，也未为获得绿灯放宽断言。

## 真实数据库闭环及反例

使用独立 MySQL 8.4.11 容器、tmpfs 和随机测试密码，控制库、基线库、候选库分离。没有连接生产数据库。模型是本地 OpenAI 兼容 SSE fixture；任务经过 HTTP API 和持久 outbox，再由测试直接交给 `Worker.Handle`，没有本轮真实 Kafka broker 验收。

- 首个候选触发真实 MySQL 1071 验证失败；工具观察返回后，下一决策选择另一个候选，最后明确结束：3 次模型 fixture 决策、2 次真实候选验证。报告可读，并经原审批接口返回 201，原测量任务创建返回 202；这不等于测量已完成或证明性能收益。
- 取消／超时后检查影子锁可立即重新取得，没有遗留候选索引；不通过额外等待掩盖锁泄漏。
- 覆盖 job_id／delivery_id 碰撞与旧 `by-id` 幂等键、排队及部分结果取消、评论 pending 终结、撤权、旧 owner／attempt／租约、跨重试持久计数，以及历史检索失败分类。
- R-B7 的旧代码真实表现为撤权任务进入死信且报告 404；修复后为 REJECTED、授权失败报告 200，模型调用 0。
- R-B8 使用合法的 3000 个不同 ENUM 值制造大 Schema：旧代码误判为基础设施失败／死信；修复后是预算停止、模型派发和持久预留均 0。恢复普通 Schema 后固定流程对照正常。Schema 在运行中变化仍须停止，不能借容量分类绕过一致性检查。

原失败和修复后日志分别保留，例如 `mysql-schema-context-old-failure.jsonl`、`mysql-schema-context-fixed.jsonl`。8 项确认问题及修复依据见 [Review 记录](agent-review.md)；本轮只有一次聚焦 Review 和原问题清单的修复复核，没有重新开启全项目审查。

## 复现入口

要求本机 Go、Python、Docker 正常，且已缓存官方 `mysql:8.4.11` 镜像；脚本不会自动拉取镜像，也不需要真实模型或邮箱密钥：

```sh
python scripts/verify-agent.py --evidence-dir /absolute/private/agent-evidence
go test ./internal/agentgraph -run TestLoopScenario -count=1 -v
go test ./internal/mcptool ./cmd/sentinel-mcp -count=1 -v
go vet ./...
```

数据库脚本已实际跑通，当次 20 项测试／子测试通过、无跳过，产物为 `reproducible-script/agent-result.json` 与 `agent-mysql.jsonl`；当次运行早于 R-B8，最终变更由上表关联模块及独立真实 ENUM 回归验证，不将旧脚本运行冒充最终源码全量复跑。脚本创建随机容器、loopback 端口和三个测试 schema，仅清理自己创建的容器；不修改已有数据库或客户端账号。没有 DSN 环境配置时，直接运行 `go test ./...` 的集成测试可能跳过，应查看日志，不能只看退出码。

前端在 `web/` 目录执行 `npm test`、`npm run build`。MCP 构建、私有配置、受限 writer 凭据及 OpenCode／Claude Code 示例见 [MCP 手册](mcp.md)。实际客户端连接命令与完整模型会话是不同验收项。

## 尚未验证的条件

本轮新增付费模型请求 **0**、新增费用 **0**，未重置原累计 20 元预算。当前真实路由、模型价格和账单余额没有重新核验，因此没有恢复付费调用。fixture 的表现不能证明真实模型的动作遵循、优化质量或实际 token 费用。

OpenCode 本轮只验证 stdio 连接，没有通过客户端模型发起完整诊断；Claude Code 本轮没有实际运行。四工具的提交／查询／报告／取消由官方 SDK 协议测试覆盖，真实数据库闭环由独立集成测试覆盖，尚未合并成一次真实客户端到模型再到数据库的端到端验收。

浏览器工具返回“Local browser use is disabled in Work settings”，未修改该设置，未执行真实浏览器交互或截图验收。前端 68 项自动测试与构建通过不能替代该项。

真实邮箱投递、云域名／HTTPS、生产数据库、真实 Kafka broker、新一轮 AgentMesh／模型计费联调及真实 GitHub 评论均没有在本轮执行。本交付是已完成本地验证的增量代码，不是这些外部环境已上线健康的证明。
