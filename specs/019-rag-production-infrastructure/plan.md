# 实现计划

1. 保持 018 的 offline fake/memory 测试入口，将 production wiring 独立为 `ProductionConfig` 与 `NewProductionService`，要求全部显式环境变量。
2. 实现 HTTP Qdrant adapter：显式 health、collection 创建、稳定 UUID point ID、带 tenant/project/document/version metadata 的 upsert/query/delete，服务层继续二次隔离。
3. 增加静态、可审查 corpus manifest：SQL Sentinel 和 AgentMesh 的 README/specs/docs/decisions 用 `repo://` URI、commit version 和 SHA-256 绑定；由 manifest 入口拒绝未列或变更的源字节。
4. 实现 AgentMesh grounded-answer client：只传 retrieval 已给定的原始 chunks，hash-bind citation，并拒绝 response 的任何不匹配 citation；提供 production query/verify CLI。
5. 定义黄金集并修正评测为真正 multi-positive Recall@K/Precision@K；将 scope、no-evidence、expiry/delete 和 injection 作为固定安全评测维度。
6. 提供独立 Qdrant Compose（持久 volume、loopback ports、healthcheck），更新操作文档，并在没有真实服务时只做配置拒绝与 HTTP fake 验证。
7. 执行 gofmt、全量 test/build/vet/diff check；真实 smoke 只在依赖已存在时执行并记录实际结果。
