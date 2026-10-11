# 2026-10 后端扩展契约

SQL改写、ES、PostgreSQL、独立Verifier和并发写入实验的新增契约见 [backend-roadmap.md](backend-roadmap.md)。下文保留核心扩展说明。

本文件覆盖 API/worker 的当前能力；早期实施步骤与 Spike CLI 是历史计划/实验入口。生产运行入口为 Docker 中的 api、kafka-relay、kafka-worker，部署配置位于联合包 deploy/。无需 UI。

## 通用 SQL 与影子边界

只支持 MySQL 单条 SELECT/WITH/UNION，只操作操作者导入的隔离影子数据。服务启动时 `SQL_SENTINEL_SHADOW_TABLES=orders,customers,invoices` 明确授权最多 16 个小写表名；JWT 还可进一步限制 `tables`。表必须是 BASE TABLE、有主键，支持字符串与复合主键。表名/列名为安全 ASCII 标识符，不支持跨 schema、视图、存储/UDF 函数、变量、锁定读、递归 CTE、optimizer hint、SELECT INTO、绑定参数与原始 DDL。未允许的函数拒绝；这是有边界的通用 MySQL 支持，不能解释为任何 SQL 都能执行。

PingCAP AST 检查引用表（含引用作用域）、副作用与复杂度。输入上限 64 KiB、AST 4096 节点。每个 SQL 任务最多 2 分钟，每条实测查询最多 2 秒，结果最多 10000 行/128 列/8 MiB。快照最多 200 万行/256 MiB，绑定列类型、默认值、可空性、collation、主键和已有索引定义，以及所有行的 NULL/值与长度。baseline/candidate 的 MySQL 版本和快照必须相同。

后台在 candidate 的专用连接取得数据库级命名锁；该连接执行所有候选 DDL、测量和清理。`idx_cand_` 前缀属于服务，操作者不得用于导入的常驻索引。候选仅允许一个索引、1–4 列、ASC/DESC；模型不能提交可执行 SQL/DDL。服务为任务生成索引名，成功、失败和取消后清理。锁释放失败时丢弃连接。导入和外部写入必须停 worker，不可依赖命名锁约束其他工具。

## 身份与审批

Bootstrap 私有 token 仅用于运维开通。通过 SSH 到 loopback 18081 调用 POST `/auth/tokens`：

```json
{"subject":"author","role":"writer","tenant_id":"team","project_id":"project","tables":["orders"]}
```

返回一次性展示的 token、token_id、expires_in=3600。JWT 使用 HS256、固定 issuer/audience、强制有效期、服务端会话与撤销检查；密钥独立于 bootstrap。POST `/auth/revoke` 接收 `token_id`。Scoped admin 只能发放/撤销自身域与已有表权限；bootstrap 可跨域开通。没有自动 refresh。

| 角色 | 权限 |
| --- | --- |
| reader | 自身项目任务/报告、RAG 查询/回答 |
| writer | reader + 创建分析任务、RAG 写入/删除 |
| reviewer | reader + 候选审批、创建基准任务、读取审计 |
| admin | 本域全部能力；bootstrap 才能跨域开通 |

项目和租户隔离约束任务/报告/审批/RAG。SQL 的表授权只控制表目录，**不提供共享表内的租户行级隔离**；不同租户的数据需要分别部署影子环境或由管理员只分配各自独立表。

GET `/jobs/{delivery_id}/report` 返回 `X-Sentinel-Report-SHA256`。POST `/jobs/{delivery_id}/approval` 接收 `{report_sha256,decision:"approved"或"rejected",reason}`。只有 COMPLETED 报告和合法候选可审批，过期哈希返回 409。审批不可覆盖；同操作者、哈希、理由、决策的重试幂等。普通 reviewer 不可审批自己创建的任务；admin 例外记录审计标志。变更 SQL/候选须创建新任务并重新审批。

POST `/jobs/{delivery_id}/benchmark` 接收 `{report_sha256,delivery_id:"new-id"}`，审批通过才创建新测量任务。worker 再次核对 SQL/候选/报告哈希。分析任务和测量任务的 report 独立，绝不把旧 L1 报告改成 L2。POST 创建返回 202、幂等冲突 409、全服务待处理任务达到 1000 时 429。认证接口全局最多 8 个并发请求。

GET `/audit?after=0` 返回自身域最多 100 条记录。任务、测量任务、发令牌、撤销、审批、RAG 写入/删除有持久记录，相关业务与审计在同一事务。管理员能操作数据库，所以这里不声称防篡改审计。历史默认保留；部署方须监控容量并制定合规归档，不能直接清理仍需重放的业务账本。

## L1 / L2 / L3

默认任务仍输出 L1 EXPLAIN。经审批的基准任务额外输出 `performance`，协议 `bounded-ab.v1`：先校验完整结果集多重集合一致性，2 轮预热，20 轮独立噪声标定、5 轮正式测量，每轮 5 次完整执行并交替 A/B 顺序。原始耗时、中位数、P95、MAD 噪声、收益、门槛和两侧 EXPLAIN ANALYZE 均保留。两侧噪声 ≤10%、读收益严格超过 2×最大噪声、快照/结果不变才允许 L2。没有收益或噪声过高返回 L1，不能人为强制提升等级。

写成本协议 `temporary-insert-256.v1` 在专用连接的临时表复制已有索引，按主键顺序取最多 256 行做插入，2 轮预热、20 轮标定、5 轮测量，完整清理，不修改原影子行。不足 32 行、噪声超限、插入中位数成本增幅超过 20% 拒绝写成本结论。它量化小批量插入成本，**不是生产并发 INSERT/UPDATE 的锁争用/吞吐回放**。读收益和写成本分别记录，生产建索引仍由人工在真实工作负载验证后执行，API 没有生产应用接口。

L3 需要操作者提供已脱敏、有代表性的具体只读工作负载及快照证明。将私有 JSON 挂载为 `SQL_SENTINEL_REPRESENTATIVE_MANIFEST_FILE`（secret loader 读取），或显式环境值 `SQL_SENTINEL_REPRESENTATIVE_MANIFEST`：

```json
{"dataset_id":"representative-v1","snapshot_sha256":"actual_snapshot_digest","workload_sha256":"sha256_of_exact_sql","approved_by":"data-owner","redacted":true,"expires_at":"2026-11-01T00:00:00Z"}
```

每次测量要求有效期、SQL SHA、实际快照 SHA 完全匹配，且 L2 已合格、结果等价才标为 L3。`approved_by` 是部署操作者的来源声明，不能自动证明代表性；数据所有者必须评估采样/分布。缺资料、不匹配或过期不会升级。此次未提供真实代表性资料，验收不声称拿到真实 L3 性能收益。

## RAG

POST `/rag/documents` 接收 `{document_id,source_uri,version,content_type,content}`；支持 plain/markdown，API 上限 32 KiB；不根据 URI 抓取远程资源。分块 400 字符、重叠 50，最多 128 块，每批 4 块调用真实 gateway embedding，维度必须与模型吻合。MySQL JSON 保存块、向量和版本，scope 内最多 10000 块；确切余弦检索适用于小型知识库，超限拒绝。更大数据需要独立向量索引方案，不能把该实现宣传为大规模向量数据库。

POST `/rag/query`、`/rag/answer` 接收 `{question,top_k,min_score}`，question 最多 1000 bytes、top_k 默认3/上限8，未指定 min_score 使用0.35（显式值范围0–1）。检索再次过滤 scope、过期片段、不可信标志与非有限评分，reranker 不得添写证据；可用 DELETE `/rag/documents/{id}` 撤回，同版本重新写入也整体替换。服务重启不丢块。

所有文档和模型文本均为不可信数据，来源/文本边界转义，拒绝 SQL/DDL 请求。answer 经 Eino/AgentMesh 真实模型返回严格 JSON，引用只能从本次检索结果取，伪造引用返回502，无引用/无证据明确拒答。引用校验证明来源绑定，不能数学保证模型每个语义陈述都正确；真实模型知识评测属于外部联调验收。缺模型/embedding 时503，不用 fake 顶替生产依赖。

## 观测和版本

Prometheus `/metrics` 仅私网，有限路由/方法标签，含请求次数、全请求/SSE耗时、并发数、pending outbox、超期租约。OTel 使用有界队列异步 OTLP 导出，HTTP、Eino draft/Graph Callback、持久任务上下文经 traceparent 接入网关与 provider；不记录原始 SQL/问题/密钥。测量/报告绑定 schema、prompt、rules、model 配置版本及快照/工作负载 SHA。配置 model alias 不是不可变权重版本，真实 provider 变更需重新验收。

Compose 集成 Prometheus 和 OTel Collector，使用固定镜像 digest；默认 Collector debug basic 只用于确认收集和有限摘要日志，没有长期可查询的 trace 数据库。可将 OTLP 导出接到部署方的追踪平台。告警规则在 Prometheus 可见，未配置外部 Alertmanager 通知通道。详见 deploy/README.md。
