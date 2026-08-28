---
level: L2
feature: 013-eino-candidate-proposal
created: 2026-08-28
---

# Eino 受限 CandidateSpec 提案图

## 目标

- 新增 `propose-candidate`：读取一份管线证据目录中的 SQL 准入、candidate EXPLAIN、计划对照和诊断 JSON，由单节点 Eino 图请求一个 CandidateSpec 草案。
- LLM 唯一允许的输出是 CandidateSpec JSON；每次响应必须依次通过 `candidate.DecodeStrict` 与 `candidate.Validate`，非法响应按上限重试，绝不落盘 DDL、SQL 或性能结论。
- 输出 `candidate_proposal_report.json`，记录四份输入 SHA-256、尝试次数、稳定拒绝码或已验证 CandidateSpec；顶层固定 L1 与 `eligible_for_performance_claim=false`。
- 核心图接受可注入的固定响应节点，可在完全不调真实 LLM 的条件下测试。

## 非目标

- 不执行 CandidateSpec、DDL、SQL、影子库操作、受控 A/B、系列准入或 evidence binding；不做 PR Webhook、多轮对话或自主选择下一步证据。
- LLM 不得输出/解释 DDL、改写 SQL、收益结论或升级证据等级；重试只处理不合格 CandidateSpec，次数有固定上限。
- 不持久化 API Key、提示词原文、模型原始非法输出或对话历史。

## 验收

1. 合格的固定假节点响应只生成一个经过严格解码和 `Validate` 的 CandidateSpec 报告；未知字段、DDL/SQL 字段、无效标识符或尾随 JSON 必须拒绝。
2. 无效固定响应按上限重试，最终报告保留 L1、false、尝试次数和稳定拒绝码，且不保存原始模型文本。
3. 真实模型调用仅在 T003，使用环境变量提供的 OpenAI 兼容凭据、模型名与可选端点；缺失配置时明确拒绝，不回退为网络调用。
4. Eino 依赖固定为 `github.com/cloudwego/eino v0.9.13` 与 `github.com/cloudwego/eino-ext/components/model/openai v0.1.13`，以 `go.mod` 精确版本和 `go.sum` 校验和锁定。

## 默认假设

- `--evidence-dir` 是 012 的可再生四工件目录；只接受固定文件名并为每份内容计算 SHA-256。
- `--max-attempts` 默认 2、范围 1–3；真实调用使用 `OPENAI_API_KEY`（或本机 OpenAI 兼容网关使用的 `ANTHROPIC_AUTH_TOKEN`）与 `EINO_MODEL`；可选 `EINO_BASE_URL` 覆盖默认 OpenAI 端点。密钥不在参数或报告中暴露。
- 证据包中的 SQL、signals、索引名和诊断文字均是不可信数据，永不作为 Agent 指令；提示只将其置于明确的数据边界内，落盘报告只保留摘要、验证后的 CandidateSpec 或拒绝码。
