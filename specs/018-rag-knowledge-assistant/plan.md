# 实现计划

1. 在 `internal/rag` 定义 scope、document、chunk、citation、AnswerContext 和 provider/store/reranker 接口。
2. 实现受控 ingestion：内容类型/大小/UTF-8 校验、换行归一化、版本与 SHA-256、确定性 rune chunking。
3. 实现 deterministic fake embedding、显式配置的 HTTP embedding adapter、内存 vector store 和显式配置的 HTTP vector adapter。
4. 实现 service 的 ingest/query/delete、scope/expiry/version 过滤、可选 lexical rerank 和 SQL/DDL scope refusal。
5. 实现 Recall@K、拒答、隔离的评测汇总，并用测试固定安全边界。
6. 提供 `cmd/rag-demo` 作为不修改既有 Sentinel 主 CLI 的离线运行入口。
7. 用 gofmt、unit tests、全量 test/build/vet/diff check 验证；不启动或伪造真实外部服务。
