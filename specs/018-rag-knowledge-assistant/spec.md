---
level: L1
feature: 018-rag-knowledge-assistant
created: 2026-09-10
---

# 受控项目知识 RAG 助手

## 目标

为 SQL Sentinel 提供可运行、可评估的项目知识检索旁路：受控文档经过版本化、SHA-256、确定性切片和 embedding 后，按 tenant/project 检索并生成带 source/chunk/hash 引用的上下文。

RAG 只能服务项目知识问答和 CandidateSpec 提案前的辅助解释。它不生成或执行 SQL/DDL，不改变 SQL Sentinel 的 L0/L1/L2、shadow gate、测量或性能资格语义。

## 范围与边界

- 允许来源：明确传入的 Markdown/plain text；限制大小、UTF-8 和 content type。
- 每个文档必须有 document ID、tenant/project scope、source URI、version、content SHA-256。
- chunk 使用固定 rune 上限、固定 overlap 和稳定 hash ID；chunk 永远标记为 untrusted context。
- 默认使用离线 deterministic fake embedding；真实 HTTP embedding/vector store 必须显式配置 endpoint、model/key，缺失时 controlled refusal，网络错误不得伪造成功。
- 检索必须携带完整 scope；过期和删除内容不可返回；新版本替换同文档旧版本。
- 无证据时返回 `ErrNoEvidence` 和拒答上下文；文档中的 prompt injection 只能作为被引用的不可信文本。

## 非目标

- 不把向量相似度当成 SQL 性能证据。
- 不接受 Kafka、Webhook、模型 prompt 或任意远程 URL 作为隐式文档来源。
- 不生成 SQL、DDL、执行计划、性能结论或绕过 CandidateSpec.Validate 的对象。

## 验收标准

1. 受控 ingestion 对规范化内容产生稳定 document/chunk SHA-256 和稳定 chunk ID。
2. 本地 vector store 支持 upsert、top-K cosine retrieval、版本替换、删除和过期过滤。
3. embedding/vector store 具备接口抽象、离线 fake、本地 adapter 和显式配置的真实 adapter。
4. AnswerContext 每条内容带 source URI、document/version、chunk ID、chunk SHA-256，并保留 untrusted 分隔标记。
5. eval 覆盖 Recall@K、无证据拒答、过期/删除、跨 tenant/project 隔离和 prompt-injection 不越权。
6. `go test ./...`、`go build ./...`、`go vet ./...`、`gofmt -l .` 和 `git diff --check` 通过；真实外部服务缺失时只验证 controlled refusal。
