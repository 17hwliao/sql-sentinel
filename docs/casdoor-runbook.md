# Casdoor + QQ 邮箱验证码

2026-10-23：替代原 Clerk 配置入口。两个产品使用一个 Casdoor 用户池、两个 OIDC 应用；业务权限仍分别管理。邮箱未配置时不会开放真实登录。

## QQ 授权码如何取得和保存

1. 自行登录 [QQ 邮箱网页版](https://mail.qq.com/)，进入 **设置 → 账号与安全 → 安全设置**。旧版界面可能显示 **设置 → 账户**。
2. 找到 **IMAP/SMTP 服务**，启用并按页面提示完成安全验证，然后选择 **生成授权码**。
3. 将生成的 16 位字母授权码保存为 UTF-8 纯文本，文件中只放授权码。例如 Windows 私有路径：`C:\Users\17hwliao\.config\agentmesh-private\qq-smtp-authorization.txt`。不要放在桌面截图、源码目录、命令参数或聊天中。可在 PowerShell 执行 `notepad "$env:USERPROFILE\.config\agentmesh-private\qq-smtp-authorization.txt"`，在记事本中粘贴并保存，避免凭据进入终端历史。
4. 后续只提供 **发件 QQ 邮箱地址、授权码文件的绝对路径、明确指定的测试收件邮箱**。无需提供 QQ 登录密码、身份证、手机号；邮箱安全验证由持有人完成。

授权码是发信凭证，不是邮件收到的六位验证码。丢失或怀疑泄露时在邮箱设置中撤销／重新生成。SMTP 参数为 `smtp.qq.com:465`、隐式 TLS；账号与 From 地址均使用完整 QQ 邮箱。[官方帮助](https://help.mail.qq.com/detail/106/985)，[当前设置路径说明](https://help.aliyun.com/zh/model-studio/connector/qq-mail)。

## 无真实邮箱时先做什么

使用隔离 Mailpit 收件箱，不连接真实 SMTP。以下操作在**全新隔离的部署目录**执行，所需模板来自 AgentMesh 仓库的 deployments/backend-stack 目录；与现有部署同级放置两个源码仓库。不要向既有生产目录重新运行初始化。

```sh
python3 identity-setup.py --deploy-dir . --local --test-mail \
  --issuer http://127.0.0.1:8000 \
  --mesh-origin http://127.0.0.1:4180 \
  --sentinel-origin http://127.0.0.1:4181
```

生成 `identity-private/`：随机管理员密码、数据库密码、Redis 密码、独立客户端密钥、Casdoor 配置与初始应用数据。目录 0700、文件 0600；Linux 使用 root 或 UID 1000 初始化。内置管理员密码保存在 `identity-private/admin_password`，不打印到终端。

`identity.yml` 与现有 `compose.yml` 一起使用。只启动指定身份服务，不启动收费模型工作负载：

```sh
docker compose -f compose.yml -f identity.yml -f identity-local.yml \
  --profile identity-test up -d identity-db console-sessions mailpit casdoor
```

访问 `http://127.0.0.1:8000/` 和隔离收件箱 `http://127.0.0.1:8025/`。测试邮件仅在此收件箱出现；不是向真实 QQ 邮箱投递的证据。身份服务默认生成独立 JWT 签名证书；不导入公开仓库中的示例私钥。`initDataNewOnly=true` 保留后续创建的用户与管理修改，不在每次启动时重置应用。

## 两个控制台配置

先让测试用户完成邮箱验证，从 Casdoor 用户详情取得稳定的 **ID（sub）**。按原有后端手册准备网关租户、专用应用 Key 和 SQL 项目。注册本身不自动获得业务权限、管理员或余额。

```sh
python3 console-setup.py --deploy-dir . --api-origin http://127.0.0.1:18080 \
  --user-id '<Casdoor稳定ID>' --tenant-id '<已有租户>' --project-id '<SQL项目>' \
  --gateway-key-file /private/gateway-key \
  --gateway-admin-file /private/gateway-admin \
  --sentinel-bootstrap-file /private/sentinel-bootstrap \
  --tables orders --sql-role writer
```

生成各自的 `console-private/mesh/`、`console-private/sentinel/`。每个目录只挂给自己的控制台。成员绑定必须包含 issuer 和 sub，不能靠邮箱自动继承旧 Clerk 成员；管理员权限需要显式 `--platform-admin`。

容器使用 `backend_url=http://gateway:18080` 或 `http://sentinel-api:8080`、`session_redis_url=redis://console-sessions:6379/0`。若直接在宿主运行 Node，修改私有配置为实际可达的本机业务服务与 Redis 地址。不要为了方便暴露生产数据库或 Redis 公网端口。

在各自 `web/` 执行 `npm ci && npm test && npm run build`，设置 `CONSOLE_CONFIG_FILE` 后运行 `npm start`。开发使用 Vite 时，其实际浏览器 origin 必须与配置、Casdoor callback 一致，`/api` 和 `/auth` 都代理到 BFF；不要混用 localhost 和 127.0.0.1。

## 生产初始化与实际发信

在全新目标目录运行 `identity-setup.py`，去掉 `--local --test-mail`，为 issuer 和两个 origin 指定真实 HTTPS 地址；如已取得授权码，再增加 `--smtp-from <发件QQ邮箱> --smtp-secret-file /private/qq-smtp-authorization.txt`。脚本只读取文件并生成配置，不发送邮件。

没有授权码时也能准备配置，但 `email_delivery=unconfigured`：登录入口及 readiness 明确拒绝，不冒充可上线。后续已存在的实例应在 Casdoor 管理界面配置邮件 Provider，测试投递后显式更新两个控制台私有配置的 `email_delivery`；不要靠重跑初始化覆盖数据。

将 `console-setup.py --api-origin` 改为真实 API HTTPS origin。它会保留 `.env` 其他值，写入 API、两个业务和身份域名。生产组合在原后端覆盖文件基础上增加 `identity.yml:consoles.yml`；Caddy 读取 `AUTH_DOMAIN` 并代理 Casdoor。数据库和 Redis 没有公网端口；身份服务管理员通过受控运维入口管理。

```sh
docker compose config --quiet
docker compose build mesh-console sentinel-console
docker compose run --rm --no-deps mesh-console node check-config.mjs --probe
docker compose run --rm --no-deps sentinel-console node check-config.mjs --probe
```

无邮件阶段可增加 `--allow-unconfigured-email` 检查文件／基础设施，此时返回 `deployment_ready=false`，不能作为生产验收通过。`--probe` 检查身份元数据、私有会话存储和业务 readiness；不证明真实收件成功。

## 行为与验收边界

- 使用标准 OIDC 客户端验证 code、state、nonce、PKCE、签名、发行者和受众。两个业务的 Cookie 限定各自 host，生产加 Secure、HttpOnly、SameSite=Lax；写操作还要求精确 Origin 与会话 CSRF token。
- 令牌和 PKCE verifier 加密后存入 Redis；成员授权每次读取，令牌状态和邮箱验证每次向身份中心核实。身份服务／Redis 不可用时拒绝业务请求，不把失效凭证当匿名重试。
- 空闲 30 分钟、绝对 8 小时；也受上游 token 生命周期约束。退出当前控制台会撤销其服务端会话；不宣称同时退出另一个产品或清除 Casdoor SSO 会话。“重新验证身份”会显式要求重新认证。
- 两个 Casdoor 应用显式启用 Signin session 和 Auto signin；目标控制台仍须完成自己的 code/PKCE 回调及成员检查。身份中心重启后可能需要重新验证邮箱；业务权限与数据不会因此改变。
- Redis 断连期间拒绝请求，连接恢复后会重新建连；并发退出不会被延迟的会话续期重新激活。多标签页改变账号时，旧页面拒绝与原会话不一致的业务响应。
- 测试收件箱仅用于本机隔离环境。生产预置内置图片验证码、错误尝试限制；具体短信／邮箱发送频率由固定版本实现与服务商共同约束，不宣称无限量发信或跨实例共享验证码状态。
- 在公开放量前核实真实验证码送达、错误／过期／重放、Casdoor 账号停用、两产品独立权限、实际服务器资源及第三方镜像安全状态。QQ 个人邮箱不提供本项目所需的业务送达 SLA。
- 原模型 20 元预算不重置；本轮无真实模型调用。真实登录后再做网页图片到模型的最少必要联调。

`identity-private/`、`console-private/` 与数据库备份均不得提交 Git。新增身份数据库、私有初始化数据及会话密钥需单独纳入加密备份；更换会话加密密钥会使已有控制台会话失效，不能当作无影响配置更新。
