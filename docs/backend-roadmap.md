# SQL 后端扩展契约（2026-10-10）

沿用 backend-expansion.md 的 AST、表/JWT域、审批、RAG和L2/L3门禁。新增能力均执行于隔离影子或专用实验库；不得输入生产可写DSN。

## 有限提案图与改写

API模型提案使用Eino显式 evidence→draft→validate→decision 图，非法候选反馈固定修正指令，最多3次模型调用/12图步骤；取消立即停止、失败清空草稿。输入/输出有界，SQL/schema/plan为不可信数据。确定性影子验证、结果评估、报告持久化和人工审批在服务流程执行，不把LLM自述当证据。现阶段一个任务一个候选；重新取证/换候选需新job，不开放模型任意工具或任意DDL。

POST /jobs 的 candidate_spec 除索引外，可显式提交：

```json
{"kind":"rewrite","sql":"SELECT id FROM orders WHERE 42=user_id ORDER BY id"}
```

原SQL和改写都必须通过只读AST、同物理表、同排序性质；无ORDER的LIMIT改写保守拒绝。验证两条查询在baseline和candidate上的完整有界结果，对排序查询保留顺序，对无序查询比较多重集合，包含NULL、重复、列标签；版本与前后快照一致。不同数据或相反排序反例会拒绝并保存报告。当前数据相等不等于所有未来数据上的语义证明。

改写L1报告经不可变hash审批后，才运行配对A/B；workload SHA绑定两条SQL。相同20噪声/5正式轮次、等价/快照/显著收益门禁，真实代表性声明缺失最高L2；本机噪声导致L1时如实保留。没有自动把改写应用于生产。

## 独立Verifier与历史先验

verifier持影子凭据；Kafka worker只持控制库和mTLS资料。RPC校验job/lease owner/attempt，durable lease fence防止旧执行器覆盖新任务；并发4、期限2分钟，取消等待执行结束，明确completed并拒绝截断。TLS/SAN/service token/消息边界同AgentMesh。

SQL报告及历史outbox同一事务提交。ES只接收已完成任务的安全摘要，以job ID的SHA256作为文档ID；包含域、表、候选列、有效期、版本/证据摘要，不索引原始SQL或字面量。GET /history/search?table=orders&q=... 按tenant/project/table过滤并重新验证全部引用表，最多20条；historical_only=true。历史仅是候选提示，不能提升当前证据等级。查询最多512字节、HTTP3秒/1MiB，HTTPS内部CA及最小权限API key，不跟随重定向。

ES故障时检索503，SQL当前证据任务仍执行；异步relay有限重试20次/1分钟、14天过期清理。告警监控 backlog，长期故障恢复后按手册停写执行rebuild-history.sh重置未过期outbox。MySQL是权威，ES可重建，不要求备份索引替代报告。ES key在生成30天后过期，必须轮换并重建API/verifier。

## PostgreSQL和并发写入实验

pg-compare只支持专用非超级用户sentinel_compare，通过主键/已有BTREE索引/安全logical类型映射导入唯一临时schema，最大16表/单表10000行/共20000行/32MiB。原MySQL只读事务与前后快照、两库结果等价、版本/settings、EXPLAIN ANALYZE和buffers、20噪声+5正式轮次均保留。C collation可能与MySQL不同，默认值/自增/外键/前缀或表达式索引等不模拟，不能当完整迁移验证。命名锁阻止重复实验，运行期间暂停共享影子worker，避免污染读测量。

write-probe仅允许独立MySQL sentinel_write及固定orders合成字段；不修改导入影子或生产行。256行基线/候选、4workers×64操作/轮、共享行UPDATE锁争用和独立INSERT键，事务回滚；记录索引构建时间/估计大小、P95、吞吐、服务器row-lock counter delta、20噪声/5正式轮次、写开销拒绝门禁。命名锁覆盖建表/测量/清理；异常遗留只删除有生成命名与可信时间注释的旧表。结果始终L1，不能声明真实生产写负载或L3已验证。原generic临时256行insert成本路径继续存在。

原计划的大规模/真实代表性数据实验、稳定Case2b负例、真实模型回答质量及真实GitHub仍需数据/权限；本机合成样本不代替。部署、恢复、轮换及安全的最终联调步骤见 deploy/ROADMAP-RUNBOOK.md。
