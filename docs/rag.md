# RAG 扩展说明

`internal/rag` 是 SQL Sentinel 的知识辅助旁路，不是性能判定链。它的输出是 `AnswerContext`，而不是 SQL、DDL、执行计划或性能结论。

## 运行离线 demo

```powershell
go run -buildvcs=false ./cmd/rag-demo --in README.md --question "what is the evidence boundary?"
```

demo 使用 deterministic fake embedding 和 process-local vector store，输出文档/切片元数据以及带引用的上下文。真实 embedding/vector service 需要显式 endpoint、model、key；缺少配置或服务不可用时返回 controlled refusal/error，不伪造成功。

## 面试讲法

- ingestion 先做 content type、大小、UTF-8、scope 和 version 校验，再统一换行并计算 SHA-256。
- chunk ID 由 tenant/project scope、document/version/ordinal/text 的稳定输入计算，既便于幂等更新、删除和引用回溯，也避免相同文档在不同作用域共享 vector-store key；当前实现按 rune 上限切片，生产替换 tokenizer-aware 策略前需要重新评测。
- retrieval 由 embedding provider 和 vector store 接口隔离；本地 fake/memory 用于可重复测试，HTTP adapter 只在显式配置后联网。
- 每次查询强制 tenant/project scope，过期文档和删除文档不可见；服务层还会对远程 store 的返回再次做 scope/expiry/untrusted 过滤。
- 文档内容始终包在 `<untrusted_context>` 中，提示注入只是文本证据，不能覆盖系统规则。无可靠证据时返回 `ErrNoEvidence`。
- RAG 只帮助项目知识问答和 CandidateSpec 提案前的解释；CandidateSpec 仍必须经过既有严格解码/校验，L0/L1/L2、shadow gate、测量和性能资格不受 RAG 影响。

评测入口 `rag.Evaluate` 以“命中的期望证据数 / 去重后的期望证据数”统计 Recall@K，并统计无证据拒答和隔离通过率；单元测试还覆盖版本替换、过期/删除、跨作用域 key 隔离以及 prompt-injection 文档。
