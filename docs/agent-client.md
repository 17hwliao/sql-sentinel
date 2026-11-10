# 外部 Agent：模型经 AgentMesh，SQL 工具经 Sentinel

2026-11-06。OpenCode／Claude Code 执行工具，AgentMesh 转发模型请求，Sentinel 任务服务执行受限 SQL 诊断。stdio MCP 在客户端本机运行，可经 HTTPS 访问服务器上的 Sentinel；不需要为服务器部署开放公网 MCP 端口。

## 准备私有配置

先运行 SQL Sentinel API、Worker／Verifier、队列及影子环境。MCP 需要当前 tenant/project、具体表范围的短期 **writer JWT** 文件；AgentMesh 需要当前租户的 **应用 Key** 文件。两种凭据互不替代，不能使用 bootstrap 或上游百炼 Key 代替。MCP 服务端严格验证凭据形状，业务 API 验证签名、有效期、撤销和权限；短期凭据没有自动刷新承诺。

```sh
go build -o /private/bin/sentinel-mcp ./cmd/sentinel-mcp
```

Windows 使用 `.exe`。仓库外 `/private/sentinel-mcp.json` 参考 `cmd/sentinel-mcp/config.example.json`：单一 API origin、固定 tenant/project、writer JWT 的绝对文件路径。远程必须 HTTPS；本机 HTTP 还需显式 `allow_loopback_http=true`。真实邮箱、Casdoor 或管理端成员授权按现有部署配置完成，不由客户端生成器创建账号或扩权。

联合 Caddy 部署中，模型入口为 `https://域名/v1`，SQL API 挂在同域名的 `/sentinel`：MCP 配置保持 `api_origin="https://域名"`，另设 `api_base_path="/sentinel"`。直接访问本机 API 端口或另有独立 API 域名时省略前缀。不能把网关根域名的 `/jobs` 当成 Sentinel API，部署模板不会转发该路径。

```sh
python scripts/prepare-agent-client.py \
  --gateway-base-url https://mesh.example.com \
  --model domestic-chat \
  --sentinel-config /private/sentinel-mcp.json \
  --mcp-binary /private/bin/sentinel-mcp \
  --out /private/new-sql-agent-profile
```

默认只离线生成配置，不连接模型、任务服务或钱包。输出目录必须是新的绝对路径，已有目录拒绝覆盖；密钥内容不会写进生成文件。输出包括两个客户端的局部配置、`SQL-AGENT.md`、`launch-client.py` 及明确标注 `not_run` 的 `profile.json`，生成成功不代表部署可用。

可增加 `--preflight --gateway-key-file /private/gateway-key.txt` 执行只读预检。两仓库同级时自动复用 AgentMesh 的 `scripts/client-config.py`；其他布局通过 `--agentmesh-source /path/to/AgentMesh` 指定。网关现有生成器检查模型可见性／tools 声明、CNY 钱包、有效价格、最大 attempt 占用和 readiness；Sentinel 二进制 `--check` 校验本地 JWT 形状并执行一次受限 `GET /connections`。不会创建任务、充值或发 completion。价格／余额检查不等于模型的真实工具调用已验证，也不能恢复原 20 元历史累计预算；本轮没有权威累计账单和实时硬限额新证据，因此未恢复付费调用。

## 发现与明确启动会话

```sh
# 默认仅发现 MCP，不读取真实网关 Key、不调用模型
python /private/new-sql-agent-profile/launch-client.py --client opencode
python /private/new-sql-agent-profile/launch-client.py --client claude

# 只有已核对当前历史累计预算和部署硬限额后，由使用者明确启动模型会话
python /private/new-sql-agent-profile/launch-client.py --client opencode --session --key-file /private/gateway-key.txt
python /private/new-sql-agent-profile/launch-client.py --client claude --session --key-file /private/gateway-key.txt -- -p "请诊断我提供的 SQL：SELECT id FROM orders WHERE user_id=42 LIMIT 20。只用 SQL Sentinel 工具，不批准或测量。"
```

`--session` 后才读取指定应用 Key 至子进程环境，父环境不改；额外客户端参数只在显式会话允许。使用独立 HOME／XDG／配置／缓存／工作目录，关闭 OpenCode 项目配置、插件自动加载、更新及模型清单拉取；Claude 使用私有 `CLAUDE_CONFIG_DIR`，模型会话用 bare、strict MCP、空内置工具集合和局部 settings。已有用户配置、账号和全局 MCP 不改动；不自动下载安装客户端。Windows npm shim 使用已安装包的 native 二进制（或 CLI JS＋Node），不把参数拼入 Shell。特殊安装可传 `--client-exe /absolute/native/client`。

OpenCode 配置使用官方自定义 `@ai-sdk/openai-compatible` provider 和 `/v1` 端点，Key 为环境变量引用；MCP 使用 `type=local`、command/environment。Claude 模型路径使用网关的 Anthropic Messages 兼容入口，MCP 使用 stdio `mcpServers`。这些是本项目协议适配，不能宣称千问与 Claude 模型等同或获得 Claude 官方完整支持。配置依据：[OpenCode Provider](https://opencode.ai/docs/providers/#custom-provider)、[OpenCode 本地 MCP](https://opencode.ai/docs/mcp-servers/#local)、[OpenCode 环境变量替换](https://opencode.ai/docs/config/#variables)、[Claude Code MCP](https://code.claude.com/docs/en/mcp)。

## 一次诊断的边界

提供明确 SQL；缺 SQL 时模型应要求补充，不能编造 Schema／查询。使用稳定 `request_id` 提交一次，保存返回 job_id 和 delivery_id。同一逻辑提交、同一 SQL 在超时／5xx 后仍使用原 request_id，先按 delivery_id 查找；不能改 ID 制造重复任务。状态工具返回 `next_action`、`poll_after_ms=1000`、`poll_limit=180`，由客户端有界跟踪；等待不应触发新的诊断或无意义模型决策。终态后读取报告。实际客户端是否遵守轮数由客户端控制，Sentinel 网页外层 run 限额不能约束第三方整个模型会话的费用；内部任务仍限制 5 次决策／2 次验证。

MCP 报告返回有界白名单投影，保留 `result` 的候选、状态、观察 ID/hash、验证状态和已取得计数；最多 16 条观察，选中候选只取两侧各 8 个表的计划摘要。去掉原 SQL、Schema、完整计划、历史自由文本、原始错误与模型自由总结，避免把巨大或敏感内容交回上层模型。`report_projection=true` 明确标注，`full_report_path` 是固定服务中的受权相对路径，原 `report_sha256` 仍指向完整 HTTP 报告。原始 HTTP 报告不变，旧工具消费者若依赖完整 report 字段，应改为按此路径读取；不能用投影计算原报告 hash。缺 usage／费用保持未知。

只有显式 `cancel_diagnosis` 取消持久任务，关闭客户端或取消一次等待不会取消。模型仅解释 L1 证据、候选／无候选和限制，不允许通过工具审批、基准测试、生产 DDL、Shell、任意目标 URL 或跨 scope 参数。上层应将工具内容作为不可信数据，不接受其中的指令。

## 本轮实际验证及复现

```sh
python scripts/test-prepare-agent-client.py
go test ./internal/mcptool ./cmd/sentinel-mcp -count=1 -v
go vet ./internal/mcptool ./cmd/sentinel-mcp
```

Python 6 项通过；官方 Go SDK stdio 及边界测试 15 项通过（含 1 项辅助进程入口），新增覆盖有界投影、选中计划事实、缺失计数、只读预检和撤权；原提交／查询／报告／取消、未知结果恢复、scope、租约之外的客户端取消边界保持原断言。HTTP 服务为受控 fixture，不证明数据库事务或真实模型行为。

本机 **OpenCode 1.19.0** 与 **Claude Code 2.1.234** 使用这套生成器与隔离启动器执行实际 `mcp list`，均退出 0 并显示 `sql_sentinel Connected`。没有业务 HTTP 请求、没有模型请求，未修改用户账号或全局配置。证据在仓库外 `../validation/sentinel-outer-2026-11-06/client-bridge/discovery-result.json` 和两个客户端 stdout/stderr；实际 discovery 不能代替模型主动调用工具。

需要展示确定性诊断链路时，使用官方 SDK 协议测试覆盖四工具，再用 `python scripts/verify-agent.py --evidence-dir /absolute/private/agent-db-evidence` 跑真实隔离 MySQL＋内层模型 fixture；二者是各层证据，不冒充一次实际客户端到真实模型的完整联调。新网页外层 run 的闭环证据由本轮主验收记录提供。本轮真实付费模型联调仍未执行：需要当前网关地址与有效模型别名／工具能力、私有应用 Key、权威累计账单／占用及覆盖内外层和重试的部署硬限额，不能拿旧账本快照推算可用预算。
