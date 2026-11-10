# 网站外层 Agent 与客户端接入：本地验收

日期：2026-11-06。此次交付在现有工作区实现网页外层 SQL Agent，并补齐“模型经 AgentMesh、工具经 Sentinel”的客户端接入；用户随后要求两项目全量 Review。Review 的问题与修复见 [全量记录](full-review-2026-11-06.md)，不沿用旧阶段的绿灯证明新代码。

## 已实现

- 外部路径：OpenCode／Claude Code → AgentMesh 模型服务；客户端执行 Sentinel stdio MCP 四工具，经受限 HTTP 任务服务取得诊断证据。新隔离配置生成器不修改用户全局配置，默认仅配置／discovery，不自动启动付费会话。
- 独立网站：新增局部“SQL Agent”页；持久 run 的创建、按 ID／稳定 request_id 恢复、事实进度、有界等待、配置选择、取消和原报告跳转。原固定 SQL 诊断、报告、审批及 RAG 保留。
- 后端：新 `internal/agentrun/`，由现有 MySQL 保存 run、原身份、不可变输入与配置、唯一子任务、模型预留、租约及有界事件。复用 `service.RunTasks` 与原 `/jobs` 的共享业务入口，不调用自己的 HTTP handler 假造身份。
- 上限：外层每 run 4 次模型派发、1 个诊断 job、180 秒工作期限，单次模型 12 秒／768 token；内层仍是每持久 job 5 次决策／2 次验证。工具等待不调用模型，重启不清预算，未知模型派发不自动重放。
- 证据回答：模型只选择受限动作，最终采用可信报告的确定性摘要，`model_summary=false`。只提供 L1，不自动审批、测量或生产 DDL；缺少 usage／账单时费用为未知。
- 部署：更新迁移命令与 schema，API 只读检查表；联合模板新增可选 `sentinel-agent.yml`。网页不接收模型 Key、bootstrap 或数据库 DSN。

运行和回滚见 [网站手册](agent-website.md)；外部客户端私有配置和预检见 [组合接入](agent-client.md)、[MCP](mcp.md)。

## 分版本测试证据

数字统计测试与子测试，范围相互重叠，不能相加当成独立覆盖数。日志保留在仓库外 `../validation/sentinel-outer-2026-11-06/`；私有密码、DSN、JWT、客户端运行目录不在公开源码中。

| 检查 | 实际结果／范围 | 证据 |
| --- | --- | --- |
| 开发收拢时普通全量 Go | 453 PASS，0 FAIL，0 SKIP；运行后又补总期限边界，已另做真实 MySQL 定向及最终全量 race | `go-full-final.jsonl` |
| Review 前最终全量 Linux race | 454 PASS，0 FAIL，0 SKIP，Go 1.26.10＋GCC；测试拷贝哈希一致。后续 F3/F4 修复由关联测试覆盖 | `go-linux-race-final.jsonl`、`linux-race-result.json` |
| Review 后 Sentinel 关联普通测试 | Schema／内层 Agent／service／MCP 共 175 PASS，0 FAIL，0 SKIP，使用真实隔离 MySQL | `review-sentinel-affected-final.jsonl` |
| Review 后 Mesh 关联普通测试 | gateway／兼容 provider／router 共 65 PASS，0 FAIL，0 SKIP，新锁定 compress 1.20.2 | `mesh-review-affected-final.jsonl` |
| 修复后关联 Linux race | Sentinel 四模块 175 PASS；Mesh gateway／全部 provider／router 85 PASS；均 0 FAIL、0 SKIP，最终 Go 源码哈希匹配 | `sentinel-review-race-final.jsonl`、`mesh-review-race-final.jsonl` 及两个 `*-review-race-result.json` |
| 最终 Web 自动测试／构建 | 100 PASS，0 FAIL，0 SKIP；Vite 构建成功 | `review-web-tests-final.log`、`review-web-build-final.log` |
| 真实 Redis | 10 PASS，0 FAIL，0 SKIP：加密会话、产品隔离、双副本、退出／签发交错、重新认证、401 恢复、迟到 CAS 等。OIDC 和 SQL HTTP 是受控服务 | `web-redis-final.log`、`web-redis-result.json` |
| 客户端配置生成器 | Python 6 项通过；配置仅离线生成或可选 GET 预检，不生成真实可用预算证明 | `python-client-final.log` |
| OpenAPI／静态检查 | 两项目 OpenAPI 校验通过；全量 Sentinel vet 和修复关联 vet 通过，Mesh 关联 vet 通过 | 命令回执、`go-vet-final.log`、`review-sentinel-vet-final.log` |
| 依赖安全门禁 | 两项目 Windows／Linux 目标扫描均为代码可达漏洞 0；仍有 2 条未被代码调用的模块级提示 | `govulncheck-*-final.log`、`mesh-govulncheck*-final.log` |

修复后的竞态验证按受影响模块执行，未重新开展全量初评，也不把前一版全量 race 冒充修复后结果。代码／构建、自动测试、源码 Review 与生产验收是不同结论。

## 真实数据库与协议闭环

网站路径实际运行：HTTP 创建 run → MySQL 持久状态 → 共享受限任务服务 → 原 outbox／Worker → 内层 Agent → 两个真实影子 MySQL → 报告 → 外层 L1 证据回答。首候选实际触发 MySQL 1071，观察反馈后改选第二候选，内层 3 次模型 fixture／2 次真实验证；外层 2 次模型 fixture。没有生产索引或付费模型。

还运行了官方 Go SDK 客户端启动真实 stdio 子进程，经真实 Sentinel HTTP 服务创建任务、查询状态、内层实际 MySQL 验证、取回有界报告并显式取消另一个任务；与此前 HTTP 协议 fixture 分开记录。稳定幂等提交只创建一个任务，真正数据库报告由原 Runner 生成。

`scripts/verify-agent.py --outer` 已在随机新建的隔离 MySQL 上实际执行，当次 43 项 PASS、无跳过；产物为 `reproducible/agent-result.json`。该次包含外层状态、网站闭环、SDK stdio 到 MySQL 及旧内层矩阵，早于最后总期限/F3/F4修复；最后这些变化有独立定向证据，不把旧运行冒充新版本重跑。任务 outbox 由测试直接交给 `Worker.Handle`，本轮没有真实 Kafka broker 验收。

永久外层数据库反例覆盖：稳定输入与精确恢复、新 session 同 scope 读取、提交回执丢失／重启、模型 in-flight 不重放、跨 attempt 预算与旧 owner 拒写、排队及子任务取消、提交／取消竞态、撤权零派发、校验依赖故障、模型格式／超时、大上下文零派发、公平调度及期限绑定。剩余 500ms 的模型确实收到相同持久 deadline，0.60 秒停止；没有为了通过放宽限额。

## 实际客户端与部署

OpenCode **1.19.0**、Claude Code **2.1.234** 均用新生成的隔离配置实际执行 MCP list，退出 0，显示 `sql_sentinel Connected`；业务 HTTP 请求 0、付费 0，用户全局配置没有修改。这证明实际客户端 discovery，不是模型主动调用 MCP；四工具执行由 SDK 协议及数据库链路证明，不能合并成一次实际客户端模型验收。原始记录为 `client-bridge/discovery-result.json`。

生产 Caddy 的 SQL API 挂载在 `/sentinel`，新 MCP 配置可显式指定固定 `api_base_path`；根 origin 保持模型 `/v1`，不允许模型选择 URL。原前缀缺失问题已有失败反例，新预检、四工具和原独立 origin 配置均复核。

联合源模板 `compose.yml + sentinel-agent.yml` 的配置合并及迁移依赖通过；单独 Linux Compose 启动也通过：非 root、只读 API、私有 secret 文件、迁移先执行，然后 API 使用仅 DML 的 MySQL 用户 ready；同用户 CREATE 被拒绝。模型地址是不会调用的 fixture，无模型请求。该启动验证在 Review 前，F3/F4/F5/F6 属关联逻辑修复，不将启动记录说成全栈上线。旧 smoke 脚本曾因 Docker internal-only 网络没有发布端口而失败，修正测试网络后通过；生产模板已有 egress，未归因生产 API 未就绪。

证据：`compose-template-result.json`、`compose-smoke-result.json`。只创建并清理本轮随机资源，不停止用户已有 mysql；未向真实云部署。

## 不能由本轮推定的条件

新增真实模型请求 **0**、新增费用 **0**。原累计 20 元上限没有重置，当前权威账本、占用、有效价格、模型工具能力和实时硬限额未重新核验，因此付费步骤停止。已有历史千问接入证据不能证明新外层／客户端模型会话已经通过。

真实 QQ 邮件、真实多用户 Casdoor 登录、浏览器画面／交互、云域名与 HTTPS、生产数据库、真实 Kafka broker、新模型计费及真实 GitHub 评论没有在本轮执行。本机浏览器操作被设置禁用，未绕过。最终交付为已实现并按上述范围验证的本地源码，不宣称生产已上线或所有自然语言回答质量已通过。

## 2026-11-07 补充验收

- OpenCode 1.19.0 与 Claude Code 2.1.234 均实际发起 MCP 提交、查询和读报告。链路经 AgentMesh、Sentinel HTTP、MySQL outbox、Kafka 4.3.1 和独立 Worker，在 MySQL 8.4.11 影子库验证候选，再把报告交回客户端。外部客户端与 Worker 的模型响应均由本机 SSE 测试服务提供。
- 网页外层 Agent 的 HTTP 入口在同一套服务中完成提交、等待及证据返回。外层两次决策完成，子任务状态为 `COMPLETED`，返回 L1，费用字段为空。三次诊断均先遇到索引键过长，再验证替代候选。最终残留候选索引数为 0。
- 在实际浏览器打开隔离示例，提交一条 SELECT，确认运行结果显示 L1，再通过关联任务跳转到报告页。登录后的浏览器业务流另见下方。
- 首次测试将 OpenCode 的标题生成请求计入诊断决策，导致测试服务返回错误。区分标题请求后，使用原 `--pure` 参数复测通过。一次诊断产生 4 次业务模型请求与 1 次标题请求。付费预算核对应纳入客户端辅助请求。

验收脚本和结果保存在仓库外的 `work/validation/sentinel-continuation-2026-11-07/`。最终记录为 `run-d1527d923c/result.json`。本次新增真实供应商请求 0，新增费用 0 元。真实模型诊断、QQ SMTP 与云端部署仍待相应环境和验收条件。

### 登录后的浏览器自主验收

- 实际 Casdoor、Redis 会话和隔离 Mailpit 完成邮箱验证码注册；两个产品只收到一封测试验证码。未授权账号被拒绝，仅授权 AgentMesh 时 SQL Sentinel 仍被拒绝，单独授权 SQL 项目的 `writer` 后可提交 `orders` 诊断。
- AgentMesh 网页经 MySQL 持久化网关访问真实模型目录，向本机测试模型完成一轮对话，页面显示 100/30 token 和 Trace ID。网关的离线引导模式没有持久化账户接口，本轮以部署使用的 MySQL 模式验证。
- SQL Sentinel 从真实浏览器提交两次外层 Agent 运行。两次都经过 MySQL、Kafka、Worker 和真实影子库；索引键过长后均验证替代候选，任务最终为 `COMPLETED`，残留候选索引数为 0。修复后重测无需手动刷新，详情和最近运行同时显示「已完成」，从该运行能打开原诊断报告，确认 L1 和报告 SHA256。
- 修复最近运行在详情更新后仍显示旧状态的问题，并补了「已完成的详情轮询不能被随后返回的旧列表覆盖」回归用例。SQL Sentinel 前端 101 项通过，生产构建通过。

本地证据：`run-774e1c4e97/browser-ui-result.json`、`browser-backend-result.json` 和 `browser-report.jpg`。本轮模型响应均为本机测试数据，新增付费请求和真实邮件投递均为 0；未验收新版网页上传图片到真实模型、QQ SMTP 或云端 HTTPS。
