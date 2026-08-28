# 计划

1. 新建 `internal/agentgraph`：严格读取四份 012 证据 JSON、构造仅含事实的提示输入、计算 SHA-256；定义可注入 `DraftNode`，其返回文本唯一按 CandidateSpec 严格解码并 Validate。报告不保存原始模型文本。
2. Eino Compose Graph 只包含“证据包 → CandidateSpec 文本”节点，适配 `DraftNode`；生产适配器用 OpenAI ChatModel，测试替换为固定节点，因此图编排和重试不依赖网络或真实模型。
3. CLI 以 `--evidence-dir`、`--out`、`--max-attempts` 读取本地工件并落盘 L1 报告；只在显式 `--provider openai` 时读取 `OPENAI_API_KEY`（兼容本机 `ANTHROPIC_AUTH_TOKEN`）、`EINO_MODEL` 与可选 `EINO_BASE_URL`，缺失即拒绝。
4. 新依赖依据 README §6 的 `GenerateCandidateSpec`/`ValidateCandidate` 图节点与 README §9 的 Eino Graph/Tool/Callback 选型：T002 在 `go.mod` 精确添加 Eino v0.9.13 和 OpenAI adapter v0.1.13，`go mod tidy` 写入 go.sum；不使用浮动版本。
5. 任务顺序：离线核心与假节点测试 → Eino 图/CLI/依赖锁定 → 有环境凭据时唯一一次真实调用并更新 README → 全量验证、复盘、提交和 master fast-forward。风险：模型输出越界或不可用；降级为 L1 拒绝报告，永不执行候选。章程：暂未建立。

真实实验记录：待 T003 如实记录；此前不调用任何真实 LLM，也不操作 Docker。
