---
level: L1
feature: 019-rag-production-infrastructure
created: 2026-09-11
---

# 生产化 RAG 基础设施

## 目标

将 018 的离线、证据优先 RAG 旁路扩展为可部署的持久化 Qdrant 实现：受控语料由可复核 manifest 绑定，真实 embedding 与向量库只接受显式配置，tenant/project/version/delete/expiry 在持久化边界和服务返回处均受约束。

## 范围与安全边界

- `docs/rag-corpus-manifest.json` 是 SQL Sentinel 与 AgentMesh README、specs、docs、decisions 的审查型来源清单，记录 repo URI、commit version 和精确 SHA-256；不接受 URL、glob、prompt 或任意路径作为隐式来源。
- `IngestManifestEntry` 先验证 allowlist 与原始字节 SHA-256，才允许既有受控 ingestion、chunk、embedding 和向量写入。
- 生产 backend 优先 Qdrant：由 `RAG_QDRANT_ENDPOINT`、可选 `RAG_QDRANT_API_KEY`、`RAG_QDRANT_COLLECTION` 显式配置，collection 的 vector dimension 必须与 embedding 相同。缺配置、健康检查或建 collection 失败均为受控失败，不回退到 memory/fake。
- embedding 使用 OpenAI-compatible endpoint，必须同时显式给出 `RAG_EMBEDDING_ENDPOINT`、`RAG_EMBEDDING_API_KEY`、`RAG_EMBEDDING_MODEL` 与 `RAG_EMBEDDING_DIMENSION`。密钥不进入报告、manifest 或日志。
- 生产 query 仅在已配置 `RAG_AGENTMESH_BASE_URL`、`RAG_AGENTMESH_API_KEY`、`RAG_AGENTMESH_RAG_MODEL` 时调用 AgentMesh `POST /v1/rag/answers`。它将每个检索 chunk 的精确 `TextSHA256` 映射为 citation `hash`，并传入 source URI、document ID、version、chunk ID 与原始 content；返回 facts 的每个 citation 必须逐字段匹配输入 context，否则拒绝整个 answer。
- Qdrant payload 持久化完整 chunk 与平铺 tenant/project filter；每次 write 先删除同 scope/document 的旧 version。检索、删除和服务层均强制 scope；远端 payload 仍须经过 scope、expiry、untrusted 二次验证。
- `docker-compose.rag.yml` 仅启动带 named volume 与 healthcheck 的 loopback Qdrant；它不改现有 baseline/candidate MySQL compose，也不声明 embedding 服务已配置。
- `rag-golden-set.json` 固定多正例、scope/no-evidence、expiry/delete 与 prompt-injection 的评测意图。Recall@K 是已取回正例数 / 全部正例数，Precision@K 是 K 个返回中相关项比例；不是“任一命中”替代指标。

## 非目标

- 不实现 pgvector adapter（Qdrant 为本期持久化 adapter）；不把未实现的 PostgreSQL 配置描述成可用。
- 不自动启动、配置或伪造 embedding provider；Qdrant health 不等于 embedding 或端到端检索健康。
- 不接受 AgentMesh 未引用的 prose、citation、SQL/DDL 或性能结论；无证据、SQL/DDL 问题在调用 AgentMesh 前受控拒绝。
- 不更改 SQL Sentinel L0/L1/L2、CandidateSpec 校验、shadow gate、测量、性能资格或任何性能结论。

## 验收标准

1. 缺失任一生产 embedding/Qdrant 必填配置时，构造前返回 `ErrProviderRefused`；没有 fake/memory 降级。
2. Qdrant adapter 具备 `/healthz`、collection create、scope-filtered upsert/search/delete；每点携带 scope、document/version 与 expiry/untrusted metadata。
3. 语料 manifest 与每项原始 SHA-256 绑定；变更内容或未列来源在 vector 写入前拒绝。
4. 单元测试验证 Qdrant HTTP 契约、multi-positive Recall@K/Precision@K、manifest 失配，以及已有 scope/expiry/delete/prompt injection 防线。
5. HTTP 双端合同测试验证 `/v1/embeddings`→retrieval→`/v1/rag/answers`、no-evidence、SQL/DDL refusal 与 AgentMesh citation mismatch。
6. 完成 `gofmt -l .`、`go test -count=1 ./...`、`go build ./...`、`go vet ./...`、`git diff --check`。仅当 Qdrant、embedding endpoint 与 AgentMesh answer endpoint 均存在且 `RAG_REAL_SMOKE=1` 时才允许真实 smoke；否则明确记录未验证，绝不伪造成功。
