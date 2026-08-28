---
level: L2
feature: 015-final-acceptance
created: 2026-08-28
---

# 最终验收评测与项目收尾

## 目标

- 新增 `examples/evaluation/` 的 7 个已知 SQL 坏味道样例和严格 manifest；以 `sqladmit` 的 signal 或拒绝码作为“检出”，生成静态规则召回率的机器可读实测结果。
- 新增 `sentinel evaluate`，读取样例、001 的既有 100 万行 `validation_result.json` 与本次安全测试/管线时序，写出 `evaluation_report.json`；报告逐项标记 README §11 的实测值、部分可测值、豁免或范围外，顶层固定 L1/`eligible_for_performance_claim=false`。
- 为 012 pipeline 报告加入各已执行步骤的耗时；使用本地签名 webhook delivery 重跑“SQL 文本 → 评论/工件”的烟雾链路，记录命令、状态、SHA-256 和时序。
- 重写 README §11 为六项可审计验收矩阵：静态召回、验证通过率、安全性、最优接近度豁免、结果等价性范围外、成本部分可测；豁免/范围外不得表述为通过。

## 非目标

- 不新增自动索引反馈迭代、SQL 改写/结果等价性 Agent、Token 埋点、真实 GitHub、远程队列、权限系统或自动执行索引。
- 不重跑或篡改 001 历史测量来追求合格噪声；不把单 Case 的方向性 Better、EXPLAIN、LLM 提案或 webhook 成功表述为性能收益。
- 不 push、打 tag 或删除用户数据；Docker 仅复用运行中的影子库，不启动、停止或修改环境。

## 验收

1. 7 个 manifest 样例全部由预期 signal 或稳定拒绝码检出，报告给出分子、分母、比例及逐例结果；这只评估该人工样例集，不外推到真实 SQL 语料。
2. 报告严格读取 `validation_result.json`，记录 1,000,000 行、单 Case、方向 `Better` 与候选噪声 13.02%；同时明确整体性能收益资格为 false/L1。
3. 报告列出并重跑 CandidateSpec 严格解码、Agent 注入、SQL 锁定读、webhook 签名/429/降级和 pipeline 停止等对抗测试；失败不得写为已通过。
4. 本次真实 webhook smoke 产出评论与五项工件；报告保存四项文件 SHA-256、summary canonical 范围、四个 pipeline 步骤耗时和 L1/false。
5. 最优接近度标为豁免（无“提案→A/B→反馈迭代”闭环）；结果等价性标为范围外（无 SQL 改写能力）；成本记录步骤耗时与真实 LLM 尝试数 0，Token 用量标为未埋点豁免。

## 默认假设

- 样例均为人工维护的 `.sql` 文本，manifest 的期望值是 signal 或拒绝码；只读取，不执行原 SQL。`evaluation_report.json` 是最终可再生的公开评测快照，可提交；webhook/pipeline 临时工件仍受既有 ignore 规则保护。
- 安全测试由 `evaluate` 调用固定、无用户拼接的 `go test -count=1` 包列表并记录 exit status；本机缺少 `go` 时如实标为未执行，不伪造通过。
- pipeline 时序只描述当前运行的 wall-clock 步骤耗时，不是基准性能结论，也不改变任何既有 L1/L2 准入规则。
