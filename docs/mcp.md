# SQL Sentinel 本地 MCP 工具

2026-11-06。适配基于官方 `github.com/modelcontextprotocol/go-sdk v1.8.0` 的 `mcp.Server`、`StdioTransport` 与工具注册 API。进程只接受本地 stdin/stdout MCP 协议，不开放新的公网 MCP HTTP 服务；诊断、队列、数据库和模型调用仍由现有 SQL Sentinel 任务服务执行。

## 准备与启动

先启动现有 HTTP API、控制数据库、队列、Worker/Verifier 与影子环境。模型仍由 Verifier 通过 AgentMesh 兼容端点及受限服务端 Key 调用。MCP 进程不保存网关 Key、数据库 DSN 或管理 bootstrap。

```sh
go build -o /private/bin/sentinel-mcp ./cmd/sentinel-mcp
```

Windows 使用 `sentinel-mcp.exe`。参考 `cmd/sentinel-mcp/config.example.json`，把实际配置放到仓库外私有目录：

```json
{
  "api_origin": "https://api.example.test",
  "api_base_path": "/sentinel",
  "tenant_id": "your_tenant",
  "project_id": "your_project",
  "token_file": "/private/sql-sentinel-writer.jwt",
  "allow_loopback_http": false
}
```

配置文件与 token_file 均使用绝对路径；Windows JSON 路径可用 `C:/private/...`。`api_origin` 必须为单一 HTTPS origin，不能包含路径、查询参数、用户名或密码。联合部署的 Caddy 将 Sentinel 挂载在 `/sentinel`，因此使用该部署时设置 `api_base_path="/sentinel"`；独立 API origin 或本机直连省略该字段。仅允许空值或固定 `/sentinel`，工具不能修改它。所有任务、报告、取消及预检使用同一前缀；模型网关仍使用原 `/v1`。隔离本机测试可显式设 `allow_loopback_http=true`，此时 HTTP 仍只允许 localhost、127.0.0.1 或 ::1。

由现有受控管理入口 `/auth/tokens` 为指定 tenant/project、具体 tables 签发 **writer** 短期 JWT，将其单独保存到 token_file。MCP 不支持 bootstrap、admin JWT、通配表或运行时切换 tenant/project。无需把凭据写到客户端配置或聊天中。仅执行登录 token 的预检不能证明身份合法，真正的 JWT 验签、撤销状态、授权表和任务归属每次仍由 HTTP API 检查。

设置环境变量 `SQL_SENTINEL_MCP_CONFIG_FILE` 为上述私有配置文件的绝对路径，再启动二进制。stdout 仅输出协议消息；启动错误写入 stderr 且不回显配置、口令或原始 HTTP 错误。每次请求重新读取 token 文件：API 返回 401 或文件已过期时，工具返回 `credential_expired_or_revoked`，替换为同一受限身份的新 JWT 即可；本适配不新增登录／刷新系统。

## 四个工具

| 工具 | 参数 | HTTP 行为 |
| --- | --- | --- |
| `submit_sql_diagnosis` | `request_id`、`sql` | 单次 `POST /jobs`，使用 `execution_mode=agent.v1`、`pr_number=1`、无手动候选；返回 job_id 与 delivery_id |
| `get_diagnosis_status` | 恰选其一：`job_id` 或 `delivery_id` | 单次状态查询，无自动轮询 |
| `get_diagnosis_report` | 同上 | 单次报告查询；报告尚未生成返回明确错误；保留旧报告格式、无候选及部分结果 |
| `cancel_diagnosis` | 同上 | 单次显式 `POST .../cancel`；仍检查身份与任务归属 |

`job_id` 只映射 `/jobs/by-id/{job_id}`，`delivery_id` 只映射 `/jobs/{delivery_id}`。两种标识是不同命名空间，绝不按字符串碰巧相同而切换任务；报告／取消在对应地址后增加 `/report` 或 `/cancel`。不接受模型传入目标 URL、tenant、project、管理操作或任意路径。

提交的 `request_id` 是调用者为一个逻辑提交选择并保存的稳定幂等标识（1–64 位字母、数字、下划线或连字符），同一请求始终复用它。delivery_id 由固定 tenant/project 与 request_id 的 SHA-256 前缀确定。响应丢失／断连或 HTTP 5xx 时，任务可能已经提交，工具会提供 delivery_id 与 `outcome_unknown=true`：先用 **delivery_id 参数**查询，或使用相同 request_id 和完全相同 SQL 重试获取 duplicate/job_id；更换 SQL 必须使用新的逻辑请求，不能期待旧幂等键接受不同输入。

每个工具最多等待一个 20 秒的 HTTP 请求，响应上限 2 MiB，不跟随重定向。取消一次 MCP 等待或关闭编程客户端，只取消 HTTP 等待，**不会默认取消已持久化的后台任务**。只有显式 `cancel_diagnosis` 且后端授权通过才取消任务。客户端可按任务状态有界轮询，但本适配不会创建循环等待、另一个队列或内部 Agent。

## OpenCode 配置示例

在使用者自己的 OpenCode 配置中加入（不要覆盖其他已有配置）：

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "sql_sentinel": {
      "type": "local",
      "command": ["C:/private/bin/sentinel-mcp.exe"],
      "environment": {
        "SQL_SENTINEL_MCP_CONFIG_FILE": "C:/private/sql-sentinel-mcp.json"
      },
      "enabled": true
    }
  }
}
```

可先执行 `opencode mcp list` 检查发现／连接，再在已授权的模型会话中要求使用 sql_sentinel 工具。此命令本身不证明模型已经选择调用并取得诊断报告。[OpenCode 官方本地 MCP 配置](https://opencode.ai/docs/mcp-servers/)。

## Claude Code 配置示例

```sh
claude mcp add --scope local --env SQL_SENTINEL_MCP_CONFIG_FILE=C:/private/sql-sentinel-mcp.json --transport stdio sql_sentinel -- C:/private/bin/sentinel-mcp.exe
claude mcp list
```

`--` 后为二进制命令，环境中只给配置路径。也可在单独测试目录用 `.mcp.json` 的 `mcpServers` 配置；不要把私有配置或 token 文件加入项目。工具参数 schema 保持普通根 object，互斥 job_id/delivery_id 由服务端强制检查，避免依赖客户端对根 union schema 的转换。[Claude Code 官方 stdio 配置](https://code.claude.com/docs/en/mcp)。

## 验证与边界

```sh
go test ./internal/mcptool ./cmd/sentinel-mcp -count=1 -v
go vet ./internal/mcptool ./cmd/sentinel-mcp
```

协议测试使用官方 SDK 客户端启动真实 stdio 子进程，完成握手、工具发现、提交／状态／报告／显式取消，覆盖稳定幂等、响应丢失恢复、任务归属、过期／撤销／更新凭据、job_id/delivery_id 同名碰撞、拒绝任意 URL 与项目参数、HTTP 重定向，以及取消 tool 等待不取消后台任务。

新组合接入与隔离启动见 [外部 Agent 客户端](agent-client.md)。默认离线配置／discovery 不调用模型；可选只读预检复用 AgentMesh 原生成器，并用 `sentinel-mcp --check` 访问受限 `GET /connections`。本轮额外通过 OpenCode 1.19.0、Claude Code 2.1.234 的实际隔离 discovery，均显示 Connected，HTTP 业务请求 0。

报告工具现在显式返回 `report_projection=true` 的白名单投影：最多 16 条观察元数据、选中候选的有界计划摘要及已取得计数；完整 SQL、Schema、原始错误和自由总结不送进客户端模型。`result`、ID、`report_sha256` 等兼容字段保留，完整报告仍在 `full_report_path` 指向的原受限 HTTP 接口；摘要不能代替完整报告参与审批 hash。旧消费者若依赖其余完整字段应切换 HTTP 报告读取。状态结果新增下一步及有界轮询提示，不新增后台轮询或计费。

该测试的 HTTP 任务服务为 `httptest` 受控 fixture，不能据此宣称真实数据库、AgentMesh／供应商模型或编程客户端任务流程已验收。额外覆盖任务已经提交但 HTTP 503 的反例：仍返回结果未知，用 delivery_id 找回并以同一 request_id 重提只得到一个任务。

2026-11-06 在本机实际 **OpenCode 1.19.0** 执行 `opencode mcp list --pure --print-logs --log-level DEBUG`，退出码 0，显示 `✓ sql_sentinel connected`。使用仓库外临时 `OPENCODE_CONFIG`、独立 XDG 配置／数据／缓存目录、测试 HOME／managed 配置目录，并关闭项目配置、更新、模型清单拉取和外部插件；未修改用户原有 OpenCode 配置或账号。凭据是临时生成的受限测试 JWT。此次 CLI 仅证明实际客户端连接到 stdio 服务；没有展示四工具清单，也没有通过模型发起真实诊断，HTTP 业务请求计数为 0。四工具调用与取消的完整结果由前述官方 SDK 协议客户端测试证明，二者不得混写。

实际数据库和模型证据以本轮主验收记录为准；真实调用继续受原累计 20 元模型预算约束。

官方 SDK 的固定版本与传输接口依据：[Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0)。
