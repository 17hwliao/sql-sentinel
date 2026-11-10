# 独立网站 SQL Agent

2026-11-06。网站外层编排与已有内层诊断 Agent 分开：外层理解用户给出的 SQL 并选择诊断工具；内层负责真实 Schema、影子验证和证据绑定。AgentMesh 只做模型网关。原任务、报告、审批、RAG 与两个独立控制台继续保留。

## 启用

默认关闭，不因为打开网页自动调用模型。先用迁移账号执行更新后的 `migrate`，再配置 API：

```dotenv
SQL_SENTINEL_OUTER_AGENT_ENABLED=true
SQL_SENTINEL_OUTER_MODELS=sql-review
SQL_SENTINEL_OUTER_CONFIG_VERSION=outer.v1
SQL_SENTINEL_OUTER_TOOLS_ENABLED=true
EINO_BASE_URL=http://gateway:18080/v1
EINO_MODEL=sql-review
OPENAI_API_KEY_FILE=/run/secrets/sentinel_model_key
```

`OPENAI_API_KEY_FILE` 是网关颁发的应用 Key，不是模型供应商 Key或 Sentinel 管理 bootstrap。推荐为本部署单独配置受限网关应用、有效价格、余额／额度及路由。当前同一外层部署的模型请求由该应用账户结算，不能宣称按网站成员自动建立独立钱包。网关 Key 不授予 SQL 权限；任务始终绑定当前 Sentinel writer/admin 的 tenant/project/tables 与 session。

模型别名是管理员在部署中限定的最多 8 项清单，第一项为默认；浏览器只能选择该清单。它不能改 Base URL、Key、数据库连接或工具目标。版本、别名、工具选择随 run 保存，部署变更不重写旧输入。旧别名移除后，未完成运行会明确停止，不偷偷切换模型。配置界面的“恢复默认”仅重置当前表单选择，不修改管理员部署配置。

API 启动只检查新增表，不用业务账号执行 DDL。生产业务账号继续仅有 SELECT/INSERT/UPDATE/DELETE；迁移账号负责创建 `sql_sentinel_agent_runs`。运行表保存私有原身份 session 标识及有界消息／事实事件，不保存 Key、原始提供商错误或思维链；备份按既有控制库流程处理。

## 网页使用

1. 完成共享 Casdoor 登录并取得本产品 writer/admin 权限，打开独立 Sentinel 控制台的“SQL Agent”。
2. 填写已有 SQL 和可选说明，选择已授权的模型与工具开关。没有 SQL 时服务会要求补充，不自行生成业务查询。
3. 提交保存为一个持久 run。外层进行受限动作决策；创建唯一内层任务后由服务端确定性等待，轮询不调用模型。
4. 读取事实进度、L1 证据卡及原报告。最终回答采用服务端确定性证据摘要，`model_summary=false`；模型决定工具动作，未生成自由文本总结，不伪称已经完成模型总结。
5. 断网或刷新后恢复同一个 run。提交回执丢失使用原 request_id 的只读查询；不会自动新建。点击“取消运行”才取消其创建的子任务。取消尚未确认时显示结果未知。

权限不同的成员不能读取他人 run。浏览器存储仅保存按 subject/tenant/project 隔离的 run/request ID，不保存 SQL、输入内容或凭据。切换身份后旧请求不能更新新页面；关闭页面只取消等待，后台任务继续。

控制台为每个浏览器会话保存独立的短期 SQL 身份，经认证加密后放入既有 Redis，会话副本共享同一绑定，竞争签发的未采用 JWT 会被撤销。退出当前控制台或检测到身份／成员授权失效时撤销对应 SQL session；退出只依赖本地会话和 CSRF，不要求当时 Casdoor 在线。本机成员文件后台检查间隔 5 秒，依赖正常可达的 SQL API，不能承诺零延迟。原 SQL session 每次模型／工具执行点重新核对，撤销／过期会停止后续执行。Casdoor 管理端的远程账号变化仍通过已有身份校验传播；没有新增 Casdoor Webhook 或无限后台身份轮询，不承诺网页关闭后可立即感知所有上游账号变化。紧急撤权可使用现有 Sentinel admin 的 session 撤销入口。

## 限额与恢复

外层每 run 最多 4 次模型派发、1 个诊断任务、180 秒工作期限，单次模型 12 秒并受剩余 run 期限约束、输出最多 768 token。停止后的持久保存最多额外 3 秒，显式取消确认最多使用独立 5 秒；这两个窗口不派发新的模型。内层仍为每 durable job 最多 5 次决策／2 次候选验证；外层 4 次不含内层最多 5 次，不是整个系统只会调用 4 次。外部第三方 Agent 的模型轮数由客户端管理，本站 run 上限不控制第三方会话。

派发前在 MySQL 原子预留；进程重启不会清零。模型派发过程中崩溃或超时保留预留并标记结果／费用未知，不自动重试。工具提交先绑定稳定 delivery，响应丢失只查找或按完全相同输入幂等恢复，最多一个 job。运行租约防止旧执行器覆盖新状态；没有 exactly-once 承诺。

真实 token／账单没有取得时，网页显示费用未知；派发次数不能兑换成虚构人民币费用。最终费用以 AgentMesh 账务及供应商账单为准。用户 SQL 和工具观察作为不可信数据；未知动作、额外参数、跨域目标、审批、自动测量及任意 Shell 均不在动作协议中。

## HTTP 与外部路径

接口详见 [外层契约](agent-outer-design.md) 与 `docs/openapi.json`。创建／读取／精确 request 恢复／取消均经过原身份鉴权及 BFF 路由白名单、会话绑定与 CSRF。工具调用不向自己的 HTTP handler 伪造成员请求，而经 `service.RunTasks` 和原 `/jobs` 共用的受限业务入口。

外部客户端不经过网站 run；它使用 [MCP](mcp.md) 四工具调用相同诊断服务。stdio 进程运行在客户端机器，通过 HTTPS 访问服务器 Sentinel API，不需要公网 MCP 端口。模型走 AgentMesh、工具走 Sentinel 的组合材料见 [客户端接入](agent-client.md)。

## Linux Compose 升级与回滚

两个源码仓库同级，沿用现有导出流程。联合模板新增 `sentinel-agent.yml`，只覆盖 Sentinel API 的受限开关，不改变外部身份、Key 或网关账务。与既有 `compose.yml`、所需模型覆盖及身份／控制台覆盖一起使用；所有命令必须使用同一套 `-f` 列表与 project 名。

升级顺序：备份控制库 → 构建本版本 → 用迁移服务执行 `migrate` 创建新表 → 更新 API／Worker／Verifier 与前端 → 核对 `/agent/config` 配置 → 开放入口。模板中的 `ss-migrate` 自动先于 API；不要给业务账号加 CREATE 权限绕过迁移。

回滚顺序：先停止新 run 提交（关闭入口／维护访问）→ 等待原 run 终结或显式取消并确认子 job → 停止外层执行器 → 关闭 `SQL_SENTINEL_OUTER_AGENT_ENABLED` 或移除本覆盖 → 回滚应用。不能先回滚到不认识新状态的执行器再让它认领旧 run。保留新表、计数、稳定幂等标识及报告；不删表“释放”预算，也不用 `down -v`。

尚需的外部条件：真实网关应用及权威余额／占用账本、当前有效价格／模型能力；真实网站还需 Casdoor／QQ SMTP、HTTPS 域名和云服务器。原累计 20 元预算不重置，未核验前不运行付费示例。本地 fixture／实际数据库／真实客户端的证据分别记录于本轮验收文档，配置或构建通过不等于生产已上线。

本地复现（缓存官方 MySQL 8.4.11 镜像、Docker／Go／Python 就绪）：

```sh
python scripts/verify-agent.py --outer --evidence-dir /absolute/private/outer-agent-evidence
```

此命令验证持久外层状态、网站外层到真实内层 MySQL，以及官方 SDK stdio MCP 到真实任务服务／数据库的链路；模型是 fixture，outbox 直接交给 Worker，不冒充真实模型、实际 OpenCode 模型会话或 Kafka broker 验收。结果详见 [本轮验收](agent-outer-verification.md)。
