# 本地后端改进契约（2026-10-11）

本轮完善只读 SQL 分析、任务状态、RAG 对照和局部性能证据。MySQL 通用 schema、审批和 L2/L3 门禁保持原有证据要求；没有新的生产性能或真实模型质量结论。

## SQL 安全与改写

`sqladmit.Analyze` 一次解析并收集表范围、外层排序与各级 SELECT/集合操作中未排序的 LIMIT。改写候选仍须满足只读安全门、相同物理表范围及排序契约。旧的字符串 `limit` 检查已移除：字面量中的 `unlimited` 等内容不会误拒绝；外层 ORDER BY 不能掩盖子查询/CTE 中未排序的 LIMIT。

`internal/sqladmit/testdata/local-cases.json` 保存命名正反例，包括 SELECT/WITH、JOIN、聚合、子查询、UNION、NULL、BOM，以及写入、多语句、文件访问、锁、用户变量、提示、跨 schema、递归和不允许函数。真实 MySQL 的 15 组结果对照覆盖 NULL、重复行、列标签、顺序、聚合、JOIN 基数、UNION、LIMIT 与 CTE 可见性。

非递归 CTE 仅在自身定义结束后可见。定义中与自身或后续 CTE 同名的真实表仍计入物理表范围；已有外层 CTE 按可见性解析。独立 AST/服务权限回归与真实 MySQL 对照防止别名隐藏越权表。

结果指纹检查仍是有界数据上的证据：不能证明对所有数据等价，ORDER BY 也不自动证明排序键唯一。正式候选验收仍需隔离快照、结果比较、计划/性能证据和批准；不能自动执行生产 DDL。

## 工作流状态

同一状态验证用于内存 Store（24 组）和专用 MySQL Store（6 组）：每组包含 16 个并发认领者、重复投递、过期租约接管、旧 owner 拒绝、到期重试，以及完成/拒绝/死信终态不可重开。数据库测试仅接受 `sentinel_improvement`。内存 Store 的 Job.SQL、CandidateSpec、CompletedAt 和 outbox payload 返回副本，外部调用者不能通过返回值改写存储状态。

## RAG 三条件对照

`rag.EvaluateContrast` 对相同问题提供无上下文、检索上下文、无关上下文三条件，复用租户/项目隔离、过期与不可信文本规则。调用者提供标注的预期 chunk ID；无关样本不能包含这些 ID。仅同时提供显式 answerer 和 grader 才评价答案，否则每条记录为 `skipped_no_answerer`，不输出正确率。

```sh
go run ./cmd/rag-demo --in docs/backend-roadmap.md --question 'What is the retention policy?' --contrast
go test -run '^$' -bench 'BenchmarkSQLAdmissionCorpus|BenchmarkScopedRetrieval' -benchmem -benchtime=1s -count=3 ./internal/sqladmit ./internal/rag
```

demo 使用 fake embedding 和受控无关样本，预期 ID 来自首次检索，只验证对照流程，不能当作独立人工标注的召回质量。单元测试另使用固定标注和脚本答案验证 evaluator 接线；真实模型质量仍需代表性语料、人工标注和真实 answerer。RAG 用于项目知识问答，不授权由文档生成或执行 SQL/DDL。文档读取限制为 2 MiB 加 1 字节探测，避免在大小校验前无限读入。

局部 benchmark 记录 AST 准入与 100 文档内存/fake-embedding 检索的耗时和分配量，不包含 Ollama、向量数据库、MySQL、Kafka 或云网络。联合验收保留 PASS/FAIL/SKIP，不把依赖缺失计为通过。
