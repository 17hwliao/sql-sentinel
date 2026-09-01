---
level: L2
feature: 016-agentmesh-bridge-test
created: 2026-09-01
---

# SQL Sentinel → AgentMesh 联桥验证

**原始需求：** 验证 SQL Sentinel 能以受控、安全的方式调用 AgentMesh，并在真实 Ollama 背后完成一次跨项目 SSE 往返；随后只用已验证事实润色两项目简历材料。

## 目标

- 在 SQL Sentinel 新增最小 AgentMesh SSE 客户端：只调用已发布的 `POST /v1/chat/completions`，从环境读取 loopback base URL、API Key 与模型；完整消费 SSE，但不落盘或打印模型 delta。
- 新增 `sentinel agentmesh-bridge-test`：发送固定的非 SQL 连通性提示，输出机器可读、固定 L1/`eligible_for_performance_claim=false` 的桥接报告，包含状态、AgentMesh commit SHA、trace ID、HTTP/SSE 完成状态、稳定拒绝码或安全失败码；不产生 CandidateSpec、DDL、SQL 或性能结论。
- 提供本地测试脚本：临时生成 AgentMesh bootstrap key/route，启动现有 AgentMesh 的 `--providers ollama` 网关，再调用 SQL Sentinel 命令；仅在 `AGENTMESH_REPO`、`OLLAMA_BASE_URL` 与 `OLLAMA_MODEL` 就绪时执行真实往返。
- 补离线 HTTP/SSE 测试，覆盖 Authorization、trace header、`[DONE]`、拒绝/不完整流和密钥不进入报告；README 明确此桥只验证传输，不接入 013 CandidateSpec Graph。

## 非目标

- 不修改 AgentMesh 运行时代码、Adapter、鉴权、限流、配额或 Provider fallback；不让 SQL Sentinel 自动申请/创建 Key。
- 不发送真实 SQL、数据库证据包、敏感内容或模型输出；不把连通性测试写成候选质量、索引收益、真实数据库、性能、成本或生产集成证据。
- 不改 010 阻塞态、不启 Docker、不 push/tag；真实运行缺环境时如实输出受控拒绝。

## 默认假设

- 只接受 `http://127.0.0.1:PORT` AgentMesh 地址，超时有界；API Key 仅进 Authorization header，报告只存其 SHA-256 摘要或完全不存。真实脚本解析并传入 AgentMesh 的精确 Git commit SHA，默认端口为 `127.0.0.1:18185`，避开历史 18082–18084。
- 桥接测试使用固定模型提示，任何 SSE delta 都只在内存中验证协议完成；`X-AgentMesh-Trace-ID` 是可观察关联值，不表示模型质量或执行成功。
- 简历内容写入两仓库既有忽略的私有资料，只包含已验证提交、命令、量化结果与适用边界。

## 验收条件

1. 离线 fake AgentMesh SSE 测试证明 SQL Sentinel 发出正确 Authorization/请求、读取 trace/`[DONE]`，且不会把 Key 或 delta 写入报告；401、429、非 SSE、流中断和超时返回稳定受控结果。
2. 真实脚本从 SQL Sentinel 触发现有 AgentMesh + Ollama，产生 `verification_completed` 桥接摘要；或在环境缺失/网关失败时如实失败，不伪造成功。
3. CandidateSpec、SQL、DDL、影子库、A/B、收益资格和证据等级均不受该测试改变，报告恒为 L1/false。
4. README、两个私有简历材料、全量格式/build/vet/test/diff 与 Adaptive 校验通过；阶段复盘、提交、fast-forward `master`，不 push/tag/Docker。
