# RAG 扩展说明

`internal/rag` 是 SQL Sentinel 的知识辅助旁路，不是性能判定链。它的输出是 `AnswerContext`，而不是 SQL、DDL、执行计划或性能结论。

## 运行离线 demo

```powershell
go run -buildvcs=false ./cmd/rag-demo --in README.md --question "what is the evidence boundary?"
```

demo 使用 deterministic fake embedding 和 process-local vector store，输出文档/切片元数据以及带引用的上下文。真实 embedding/vector service 需要显式 endpoint、model、key；缺少配置或服务不可用时返回 controlled refusal/error，不伪造成功。

## 面试讲法

- ingestion 先做 content type、大小、UTF-8、scope 和 version 校验，再统一换行并计算 SHA-256。
- chunk ID 由 document/version/ordinal/text 的稳定输入计算，便于幂等更新、删除和引用回溯；当前实现按 rune 上限切片，生产替换 tokenizer-aware 策略前需要重新评测。
- retrieval 由 embedding provider 和 vector store 接口隔离；本地 fake/memory 用于可重复测试，HTTP adapter 只在显式配置后联网。
- 每次查询强制 tenant/project scope，过期文档和删除文档不可见；服务层还会对远程 store 的返回再次做 scope/expiry/untrusted 过滤。
- 文档内容始终包在 `<untrusted_context>` 中，提示注入只是文本证据，不能覆盖系统规则。无可靠证据时返回 `ErrNoEvidence`。
- RAG 只帮助项目知识问答和 CandidateSpec 提案前的解释；CandidateSpec 仍必须经过既有严格解码/校验，L0/L1/L2、shadow gate、测量和性能资格不受 RAG 影响。

评测入口 `rag.Evaluate` 至少统计 Recall@K、无证据拒答和隔离通过率；单元测试还覆盖版本替换、过期/删除以及 prompt-injection 文档。

## 生产持久化（019）

生产模式只使用显式配置，且不会回退到 fake embedding 或内存 store：

```powershell
$env:RAG_EMBEDDING_ENDPOINT = 'https://embedding.example/v1/embeddings'
$env:RAG_EMBEDDING_API_KEY = '<secret kept only in process environment>'
$env:RAG_EMBEDDING_MODEL = 'approved-embedding-model'
$env:RAG_EMBEDDING_DIMENSION = '1536'
$env:RAG_QDRANT_ENDPOINT = 'http://127.0.0.1:6333'
$env:RAG_QDRANT_COLLECTION = 'sql_sentinel_rag'
# $env:RAG_QDRANT_API_KEY = '<only when the Qdrant deployment requires it>'
$env:RAG_AGENTMESH_BASE_URL = 'http://127.0.0.1:18080'
$env:RAG_AGENTMESH_API_KEY = '<tenant API key kept only in process environment>'
$env:RAG_AGENTMESH_RAG_MODEL = 'approved-rag-model'
$env:RAG_AGENTMESH_TIMEOUT_SECONDS = '60' # default; local CPU smoke may explicitly use 180
docker compose -f deployments/docker-compose.rag.yml up -d qdrant
```

`NewProductionService` 会先检查 Qdrant `/healthz` 并创建 collection；这只证明 Qdrant 服务和 collection API 可用，不证明 embedding endpoint、AgentMesh 或端到端答案质量。缺配置或服务故障返回 controlled refusal/error。真实 smoke 只能在全部外部依赖均由操作者显式提供时执行。

持久化 query 由三个独立入口组成：

```powershell
# 先将 manifest 中明确允许且 SHA 匹配的源写入 Qdrant。
go run -buildvcs=false ./cmd/rag-production-ingest --document-id sql-sentinel-readme --source README.md

# 调用 AgentMesh /v1/embeddings、Qdrant 和 /v1/rag/answers。
go run -buildvcs=false ./cmd/rag-production-query --question 'What is the evidence boundary?'

# 真实 smoke 需要额外显式门禁；缺门禁时不会发起网络请求。
$env:RAG_REAL_SMOKE = '1'
go run -buildvcs=false ./cmd/rag-production-verify
```

`rag-demo` 保持为 fake/memory 离线 demo，不能用作生产服务成功证据。production query 先完成本地 embedding/Qdrant retrieval，再把未修改的 chunk `content` 及其 `TextSHA256 → citation.hash` 发送给 AgentMesh。所有返回 facts 必须引用完整、逐字段匹配的输入 citation；无证据、SQL/DDL 与 citation mismatch 都受控拒绝。

真实 retrieval 评测必须使用已写入 manifest 全部源文档的 collection，并以 checked-in golden document IDs 计算指标，不能用 LLM 作为相关性裁判：

```powershell
$env:RAG_REAL_SMOKE = '1'
go run -buildvcs=false ./cmd/rag-production-eval --allow-fixture-skips
```

带 `fixture` 的 golden case（过期、删除、prompt injection）只在隔离 fixture harness 中执行，production collection 不注入测试文本。跨项目 case 只断言 forbidden document 不会泄漏；真正的无证据拒答由独立的跨 tenant case 验证，以免把项目内合理的 bridge 文档误判为泄漏。命令会明确报告 fixture 未运行 case；未加 `--allow-fixture-skips` 时会失败，防止把部分生产 smoke 伪装成全量安全测试。

[`rag-corpus-manifest.json`](rag-corpus-manifest.json) 将允许写入的 SQL Sentinel/AgentMesh README、spec、docs、decision 绑定到 `repo://` URI、commit 和源字节 SHA-256；`IngestManifestEntry` 拒绝清单外或 hash 改变的内容。[`rag-golden-set.json`](rag-golden-set.json) 固定多正例、scope/no-evidence、expiry/delete 和 prompt-injection 评测语义。Recall@K 计算全部正例的取回比例，Precision@K 计算前 K 个返回中相关项比例。
