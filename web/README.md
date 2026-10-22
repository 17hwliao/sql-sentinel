# SQL Sentinel 独立控制台

本目录为 SQL Sentinel 前端与服务端身份适配，与 AgentMesh/web 分别构建、运行和授权；共用一个 Clerk 应用。界面包含 SQL 诊断、任务查询、报告、审批、基准、RAG 及项目连接。

## 本机运行

要求 Node >=22.12：

```sh
cd sql-sentinel/web
npm ci
npm test
npm run build
npm start
```

打开 http://127.0.0.1:4181/ 。缺少配置时业务 API 返回503；/?preview=1 是标明的界面示例，不调用模型、不保存业务数据。独立的 AgentMesh 控制台默认4180，可通过侧栏互相跳转。

## 获取共享身份配置

用户自行登录 https://dashboard.clerk.com/，创建一个应用并启用邮箱验证。在 API Keys 页取得 Publishable Key，将 Secret Key 保存到私有文件；同页核对 Frontend API 地址作为 Issuer。在 Users 页取得已验证用户的稳定 user_... ID。无需提供身份证、个人密码、邮箱密码或验证码。

复制 console.config.example.json 到私有目录，使用 CONSOLE_CONFIG_FILE 环境变量指定该文件：

| 配置 | 本产品取值 |
|---|---|
| origin | 本控制台外部 origin，默认 http://127.0.0.1:4181 |
| peer_url | AgentMesh 控制台 origin |
| clerk_publishable_key / clerk_issuer / clerk_secret_file | 与 AgentMesh 控制台使用同一个 Clerk 应用 |
| members_file | 本产品成员 JSON，结构见 members.example.json |
| backend_url | 内部 http://sentinel-api:8080 |
| backend_admin_file | SQL bootstrap token 文件，浏览器不可访问 |

成员根据 user_... 分配 enabled、tenant_id、project_id、role 和具体 tables。注册不自动开通业务权限。成员文件每次请求重读，使用原子替换避免半写入；enabled:false 拒绝本产品新请求。部署时文件放在 /run/console-private 下，不放入 Git 或静态目录。

服务端校验 RS256、Issuer、exp/iat、sid/sub/azp 及允许来源，向 Clerk 核实会话、用户和主邮箱状态，然后按可信成员映射获取 scoped SQL JWT。该业务 JWT 仅留在服务端内存、缓存最多55分钟；映射变更使缓存失效，SQL 后端持久会话与 Casbin 仍会复核。浏览器不能选择租户、项目、角色或授权表。Clerk 不可用时拒绝业务，身份 API 额外请求量适用于当前小规模演示。

生产建议两个控制台使用同一根域的不同子域，按 Clerk Domains 指引配置生产实例与 DNS。不同根域需另行配置 satellite，不能仅复制 Key 就声称跨域单点登录。完整的 Clerk 获取步骤与官方链接见同级 AgentMesh/web/README.md。

## 已实现的操作边界

- 提交 SQL 返回202后显示已提交，按同一 delivery_id 查询。轮询有期限，刷新后可按ID恢复；结果不明不自动重建任务。
- 最近任务仅当前浏览器、当前账号，最多30条任务ID，不等同于服务端全量列表。SQL和报告不写入浏览器持久存储。
- 报告展示 candidate_spec、artifacts、performance，计划与性能证据分开；缺失字段不冒充零值，不伪造L2/L3或生产收益。支持原始证据与JSON导出。
- 审批绑定报告SHA；普通reviewer不能审批自己任务。基准是独立新任务，创建结果不明需先查状态。
- RAG上传明示embedding用量，回答保留引用或拒答。知识写入需要writer/admin角色。
- 示例表结构明确为样例，实际表授权仍以后端为准；不提供生产DDL执行入口。
- SQL服务使用专用网关凭据调用 AgentMesh，不转发网站会话或浏览器提供的模型平台Key。

## 验证与部署

npm test 使用隔离 RSA/JWKS 和受控身份、localhost HTTP 业务夹具，不读取真实凭据、不产生模型费用。npm run build 构建前端。Dockerfile 独立构建；联合模板见同级 AgentMesh/deployments/backend-stack/CONSOLES.md、consoles.yml、Caddyfile.consoles，保留部署原有Compose组合。

真实 Clerk 邮箱收信、跨控制台会话、与当前Go服务的完整浏览器联调和云端部署尚待配置。受控测试与界面示例不替代这些证据。本次没有GitHub推送或远程发布。


## Review 后补齐（2026-10-22）

登录组件与业务 hash 路由已隔离；身份变化取消旧请求，并用身份代次保护成功和失败响应。已登录但没有产品授权时，可重新检查权限或退出换号。构建/示例不证明真实 Clerk 登录、邮件或跨子域 SSO 已验收。

页面直接适配真实 Go 报告与 RAG JSON 契约。SQL 任务从后端按项目及表授权分页读取，支持跨浏览器查询；知识文档支持当前版本列表、更新、删除，审计与历史搜索有独立入口。网关增加价格、图片资产、trace 和平台管理；`platform_admin` 必须在服务端成员文件显式设置，普通 Key 管理不等于平台管理。

客户端配置从真实目录选择模型，并检查工具能力、有效 CNY 价格、余额冻结上限和网关就绪。检查使用控制台专用凭据，不冒充新 Key 或全部 harness 功能已实测；新应用 Key 继续使用原 `scripts/client-config.py` 单独检查。所有写操作不自动重试，结果不明时先核实。

生产配置增加 `production:true`，拒绝开发 Clerk Key、非 HTTPS origin、不同父域和占位公网地址。使用 `CONSOLE_CONFIG_FILE=... npm run check:config` 校验私有配置与内部 readyz；输出不打印凭据。完整流程见 AgentMesh `deployments/backend-stack/WEB-PRODUCTION.md`；Linux 生成器 `console-setup.py` 保留原后端环境，不覆盖已有成员目录。

本轮修复与验证范围：[web-console-verification](../docs/web-console-verification.md)。
