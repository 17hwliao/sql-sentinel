# 两项目全量 Review：2026-11-06

用户在外层 SQL Agent 开发过程中明确要求全量 Review。本轮从主要入口及跨模块调用出发，不限于 diff；完成一次初评、问题真实性二次核实，再按同一清单修复和定向复核，不逐次重开全量审查。

基线：SQL Sentinel `master / 941ecb8ef629bdc102f9d5c2156600fd47c53422`；AgentMesh `master / 313db3c8ae1aa17314b330b2694a9dba61b1274b`。包含当前未提交及新增文件，保留此前 Casdoor／前端成果。不创建分支、提交、推送、PR、标签或发行。

最终结论：**本次范围内确认的 8 项缺陷均已修复并定向复核通过**。Sentinel 修复关联普通／Linux race 均 175 项通过；Mesh 关联普通 65 项、覆盖全部 provider 的 Linux race 85 项通过；最终 Web 100 项、真实 Redis 10 项通过，均无失败或跳过，构建及契约检查通过。Linux race 的拷贝 Go 源码哈希与最终代码一致。没有未收拢的已确认缺陷；下文外部环境和模型质量条件仍未验证，不是生产上线认证。

## 调用链与实际覆盖

| 主要路径 | 状态归属与核对结论 |
| --- | --- |
| Mesh API → 鉴权／租户路由 → 限流／余额预留 → provider／ModelRunner → usage／结算 | 静态追踪启动、私有配置、API Key、租户 enabled、能力路由、SSE、tool_calls／vision、mTLS、整数人民币、未知费用、取消和恢复；对新确认 F5/F6 执行受控反例。不是新一轮真实提供商账单验收。 |
| Mesh 管理／用户／Key／图片／Embedding／语义缓存／用量 outbox | 核对授权边界、私有图片引用、向量维度、工具／图片绕过文本缓存、消息重复与投影幂等。账本和长期幂等记录不能用随意 TTL 清理。未新跑所有真实 Redis／Kafka／模型路径。 |
| Sentinel API／Webhook／MCP → 任务／options／outbox → Worker／Verifier | 核对签名、稳定 ID、角色／表／项目授权、固定与 Agent 模式、重试计数、租约所有权、取消与评论 pending；公有模型网关不执行 SQL 工具。 |
| Sentinel fixed／manual／rewrite／inner Agent → Schema／快照／命名锁／影子验证 | 核对 AST、限定候选、相同上下文、清理失败停止、规范 hash 与跨 attempt 5／2 预算。F4 是合法 MySQL 元数据兼容缺陷；修复不授予任意表达式候选。 |
| 报告 → 审批 → benchmark／L2-L3／写成本 | 核对报告／SQL／候选 hash、独立角色及测量资格；L1 不升级成实测收益。聊天不自动审批、生成 benchmark 或执行生产 DDL。 |
| RAG／历史先验 | 核对范围／全部关联表／TTL／可信标志、批次与维度、向量存储替换、引用及可选依赖故障。检索结果与格式校验不证明模型每句解释真实。 |
| 新网站 run → 共享受限任务接口 → inner Agent → 有界证据回答 | 主控独立检查 run 持久化、原 session、租约 fencing、4／1／180 上限、确定性等待、重启未知请求不重放、单子任务恢复与取消、配置绑定、事件和费用未知。最终回答是确定性证据摘要，不保存思维链。 |
| 两套前端／BFF → Casdoor／Redis → 各自业务身份 | 核对共享账号与独立成员、Cookie／CSRF／会话绑定、跨身份迟到响应、局部失败、旧／新报告、审批及配置。新网页主写部分标注自检；公共身份与旧页面由另一审查者独立追踪。F1/F2 是跨副本凭据生命周期问题。 |
| 私有客户端配置 → stdio MCP → HTTPS／Caddy → Sentinel | 核对只读预检、稳定 ID、响应丢失、取消等待与持久取消、错误及报告投影；F3 是真实部署代理路径不一致。stdio 运行在客户端，不新增公网 MCP 端口。 |
| 迁移／Compose／控制台部署／备份与回滚 | 核对迁移账号与 DML 业务账号、只读容器、secret 文件、服务顺序、源模板导出清单及模型路由。历史 stage0 Compose 仅本地实验。真实云、QQ 投递、生产数据和所有运维故障组合未执行。 |

全量指主要功能及接缝的源码审查，不是逐行穷举所有依赖、SQL 方言、设备和故障交错。分工中避免审查者把自己主写的模块称为独立审查；外层由主控检查，两个产品后端和公共 Web 由其他审查者分别检查。

## 同一问题清单

| 编号／级别 | 触发 → 后果 | 根因与最小修复 | 复核状态 |
| --- | --- | --- | --- |
| F1 / P1 | `/auth/tokens` 在途时另一副本退出；旧请求随后发布 SQL JWT。或者重新认证只替换 Cookie。会话已退出但 SQL 执行身份仍存活。 | `web/auth.mjs`／`server.mjs`：退出、失效及重新认证先持久关闭旧 binding；Redis 原子核对关闭标志及原会话后 NX 发布，未采用 JWT 必须撤销且不转发业务。 | 旧受控反例已复现；保留正向断言旧失败／新通过，真实 Redis 退出／发布交错、重新认证及底层原子边界通过。 |
| F2 / P2 | 后端撤销单个 SQL JWT 后返回 401；进程只删 Map，跨副本继续重用 Redis 中同一无效 JWT，业务持续 401。 | 以本次实际使用的凭据版本 CAS 删除共享绑定及相应 Map；不自动重放原请求，下次明确操作才重新签发。迟到的旧 401 不能删除新凭据。 | 旧受控反例已复现；真实 Redis 拒绝原 POST 后不重放、下次换发、迟到旧 401 不删除新绑定及角色变更反例通过。 |
| F3 / P2 | 联合 Caddy 只在 `/sentinel/*` 转发 SQL API，而 MCP 仅支持 origin 后追加 `/jobs`，远程客户端根路径得到 404。 | MCP 增加管理员限定的可选 `api_base_path`，仅空值或 `/sentinel`；所有请求、预检及报告路径一致使用前缀，旧直连配置保持有效。同步手册和样例。 | 旧配置回归失败；新预检／四工具路径及非法前缀／旧配置反例通过。 |
| F4 / P2 | 合法函数索引的 `COLUMN_NAME` 为 NULL；Describe 扫描进 string，固定自动提案及内层 Agent 初始取证失败／重试。 | `shadowdata/schema.go` nullable 读取普通列及 EXPRESSION，保留可省略表达式事实和 Schema hash；表达式不冒充物理候选列。无数据库迁移。 | 真实 MySQL 原失败用例修复后通过；普通／函数／混合 key 及候选/hash 边界通过。 |
| F5 / P2 | Messages 每个请求重新构造 Authenticate 中间件，重建 8 槽鉴权门，未认证并发可绕过该入口上限。 | `AgentMesh/internal/gateway/anthropic.go` 在 handler 构造时创建一次受保护 handler，仍保留两种认证头及冲突检查。 | 旧第 9 个查询进入 DB 的反例已复现；新第 9 个 429、首 8 个及释放后重入通过。 |
| F6 / P2 | 兼容上游返回 `finish_reason=tool_calls`，但没有任何工具调用；网关当成功完成，客户端没有合法工具可执行。 | `AgentMesh/internal/provider/ark/ark.go` 工具终态要求至少一个完整调用，DONE 双向核对；普通文本 stop／完整分片保持原语义。 | 旧 nil error／空工具完成已复现；新空调用拒绝、正常工具、普通 stop、usage／断流与禁止 fallback 反例通过。 |
| F7 / P2 | 手工使用 Sentinel 控制台样例，实际连到 Mesh 4180／gateway backend，登录回调和业务请求不匹配。 | `web/console.config.example.json` 改为 Sentinel 4181、peer 4180、sentinel-api:8080；现有生成器原本正确，不重写它。 | 旧样例不变量失败；修复后配置／端口断言通过。 |
| F8 / P2 | AgentMesh 锁定 `compress v1.19.1`，govulncheck 报 GO-2026-6865 可达提示；Sentinel 此前已升级。 | Mesh 最小升级至扫描所列修复版本 `v1.20.2`，保持锁定依赖。没有声称实际生产利用已复现。 | 旧扫描命中 1 条；新 Windows／Linux 扫描代码可达 0，另有 2 条未调用模块级提示。 |

本轮 8 条中，F1 为 P1，其余为 P2。F3 是对固定部署前缀的兼容补全，未允许用户传任意 URL；F4 仅正确表示已有元数据；没有借 Review 实施路线图新功能、数据迁移或重构。

原始旧行为证据、独立覆盖报告、修复后日志均在仓库外 `../validation/sentinel-outer-2026-11-06/`，其中包含测试私有目录，不能整体上传或加入发布包。最终测试版本及结果见 [本轮实现与验收](agent-outer-verification.md)。本页初评和修复复核只有一轮，后续按原编号闭环。

## 仍需外部条件

本轮没有真实模型请求、费用、SMTP、云端或 GitHub 写入。实际 OpenCode／Claude Code 的 discovery、官方 MCP 协议、真实隔离 MySQL、真实 Redis 和 Linux Compose 是不同证据，不能相互替代。当前真实路由、有效价格、累计账单／占用及原 20 元硬预算尚未重新核验；没有据历史快照恢复付费。浏览器操作被本机设置禁用，未绕过。源码全量审查和测试均不能证明真实生产上线健康。
