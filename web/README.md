# SQL Sentinel 独立控制台

本目录独立构建、运行和授权，与 AgentMesh 共用自托管 Casdoor 用户池。两个 OIDC 应用各用自己的客户端凭据。使用 QQ 邮箱验证码，旧 Clerk 配置已停止使用。

要求 Node >=22.12。运行 `npm ci`、`npm test`、`npm run build`、`npm start`，默认地址 http://127.0.0.1:4181/ 。`/?preview=1` 为公开样例，不调用模型、不保存业务数据。

使用 `CONSOLE_CONFIG_FILE` 指向私有配置，参考 console.config.example.json。OIDC code、state、nonce、PKCE 和 token 验证在 BFF 完成；浏览器使用 Cookie，会话令牌加密存储于 Redis。写请求另需当前 Origin 与会话 CSRF token。

共享登录账号不自动获得 SQL 权限。成员映射显式指定 issuer、稳定 sub、tenant_id、project_id、role、tables；BFF 按此范围换取后端 scoped token。撤销成员后不能继续使用该产品。退出仅撤销当前控制台会话。

没有真实邮箱配置时保持 `email_delivery=unconfigured`，真实登录及 readiness 阻断。本地 `test_sink` 模式将邮件发往隔离收件箱，不能当作真实 QQ 邮件投递通过。生产拒绝测试模式。

[双平台认证设计](../docs/authentication-redesign.md)。初始化脚本、QQ 授权码保存和完整部署步骤统一维护在 AgentMesh 仓库的 `deployments/backend-stack/CASDOOR-RUNBOOK.md`；将两仓库放在同一部署父目录即可按该手册使用。

开发使用 Vite 时，浏览器实际 origin 与配置和 callback 必须一致，`/api` 与 `/auth` 都代理到本产品 BFF。私有配置、成员权限和凭据不得加入 Git。
