# 外层 SQL Agent：实施契约

2026-11-06。本轮实施保持两个独立产品及共享身份。AgentMesh 处理模型请求；SQL Sentinel 处理对话编排、受限诊断任务和影子验证。没有任意生产 SQL、自动审批、自动测量或公网 MCP。

## 两条路径

1. 外部客户端的模型经 AgentMesh，工具在客户端通过 Sentinel stdio MCP 调用现有 HTTP 任务服务。
2. 网站的 SQL Agent 调用 Sentinel `/agent/runs`，持久外层编排通过共享的受限任务接口调用同一业务服务。网页不启动 MCP 子进程，也不接收服务端 Key。

## 冻结的公共 HTTP 契约

- `GET /agent/config`：部署限定的模型别名、工具开关、有效配置版本、外层及内层上限、当前成员项目／表范围。只读；每次运行的选择持久绑定，重置默认是重新选择部署默认配置，不在线修改管理员配置。
- `POST /agent/runs`：`request_id`、`sql`、`message`（可选）、`model_alias`（可选）、`tools_enabled`（可选）。需要 writer/admin；无 SQL 返回 `sql_required`，不生成业务查询。相同 request_id 与相同输入返回原 run，改变输入返回 409。
- `GET /agent/runs`：当前成员自己创建的最近运行，最多 30。
- `GET /agent/runs/by-request/{request_id}`：按原稳定请求标识精确恢复自己创建的 run，不受最近列表长度限制，不创建或重放模型请求。
- `GET /agent/runs/{id}`：状态、事实事件、关联 job、证据摘要、停止原因、持久模型预留次数；缺少真实 usage／价格时费用未知。
- `POST /agent/runs/{id}/cancel`：显式取消自己运行及该 run 创建的子任务；关闭页面或等待取消不等于持久取消。

所有资源绑定 tenant/project/subject，读取需当前表权限；模型／工具派发及持久写入核对原会话、owner/lease。事件采用有界 JSON 随 run 读取，不新增 SSE 或无界消息历史。BFF 仅开放上述精确路由，保留现有 Cookie、会话绑定与 CSRF。

## 持久执行

外层状态为 queued/running/waiting_tool/completed/failed/cancelled/outcome_unknown。使用现有 MySQL 的独立 run 表与有界后台轮询，不新增 broker。保存输入、配置版本、原身份会话、稳定子任务 delivery、关联 job、事件、结果和模型派发预留。每次模型派发前原子预留；最多 4 次，包含总结；每 run 最多 1 个诊断任务，总期限 180 秒。工具等待不调用模型。内层仍保持每 durable task 5 次决策／2 次验证。

实际 v1 模型仅选择受限动作；最终回答采用可信报告的确定性摘要，`model_summary=false`，没有生成或保存模型自由文本解释。模型调用仍计入外层 4 次上限。180 秒是工作期限，最后保存最多额外 3 秒，显式取消确认最多独立 5 秒，不在停止后派发模型。等待任务按更新时间公平轮换，不能让最早一项占满本机执行器。

模型动作仅 submit/status/report/finish，参数不能覆盖 scope、SQL、目标或审批；SQL 来自绑定输入。首次提交前持久保存稳定 delivery，提交结果未知先按原 delivery 恢复；重启不会生成新子任务 ID。失去 lease 的执行器不得落盘。模型派发中崩溃不得自动重放结果未知调用。总结只描述有界可信证据，失败仍可返回确定性报告并明确总结未完成；不保存思维链或原始依赖错误。

## 文件所有权

- Agent A：新 `internal/agentrun/`（状态机、MySQL 存储、预算、恢复、取消及测试），不修改既有 service/identity/inner Agent。
- Agent B：`internal/mcptool/`、`cmd/sentinel-mcp/`、新隔离客户端准备脚本及对应测试／接入文档，不修改用户全局配置。
- Agent C：新 `web/src/agentrun.js`、对应新测试／局部样式；负责 `web/src/product.js` 的最小导航与挂载接入，其他前端公共文件由主控协调。
- 主控：`internal/service/` 共享任务适配及 HTTP、`cmd/api/`、模型适配、鉴权/BFF/OpenAPI、部署与跨模块验收。

公共 Go 接口由 Agent A 在开始实现时冻结并通知主控，任务服务通过 typed 接口共享，不向自己的 HTTP handler 发伪造请求。各自完成后收拢写入再做整合测试。初始实施不自动触发 Review；用户随后明确要求全量 Review，已按同一问题清单执行初评、最小修复及定向复核，见 [全量记录](full-review-2026-11-06.md)。

## 验收与限制

覆盖持久闭环、状态／预算／撤权／取消／响应丢失及幂等；真实隔离 MySQL 与模型 fixture、协议 fixture、实际客户端 discovery 和模型驱动调用分别标注。运行相关普通、MySQL、Linux race、Web/build、契约及安全检查。保留 fixed/manual/rewrite/审批/RAG/Casdoor 边界。原 20 元累计预算不重置，未核对当前权威账本、价格及硬限额前不发付费请求。仅操作本轮隔离容器，不发布 GitHub 或真实云端。
