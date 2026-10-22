# 真实依赖、SQL 负对照与本地 RAG 质量评估

联合一键演示及完整负载/崩溃/双实例流程见同级 AgentMesh 的 `docs/engineering-verification.md`。SQL Sentinel 提供 `docs/openapi.json`，并在 CI 增加隔离真实 MySQL 工作流、锁和语义反例的 mandatory 验证。

```sh
go vet ./...
go test ./...
python -m pip install -r scripts/requirements-contracts.txt
python scripts/validate-contracts.py
docker build --target verify -t backend-integration-runner .
python scripts/verify-integrations.py --runner-image backend-integration-runner --out .private/integration-results
```

runner 仅允许专用测试数据库 `sentinel_improvement`。32 组 SQL 对照包含 CTE 遮蔽与引用顺序、NULL/NOT IN、LEFT JOIN 过滤、聚合、HAVING、重复行、排序、窗口、别名与 collation。指纹规范化 DECIMAL 使用精确有理数，等值的 `0`/`0.00` 不再误判，超出 float64 精度的金额不丢失低位。

`TestPersistentSkewIndexNegativeEvidence` 建立十万行数据，其中 95% 匹配同一索引键；用强制非覆盖索引对照全表扫描。分批造数控制内存，先 20 次噪声轮，再 5 次正式轮，交替顺序、核对完整聚合结果，并保留实际 EXPLAIN ANALYZE 与每轮耗时。负对照在候选未测得更慢时会失败为 inconclusive，不能放宽断言获取通过。该实验始终是合成 L1 证据，不能升级成 L3。

## 实际 Ollama 评估

安装并启动可信 loopback Ollama，准备 `qwen2.5:7b` 和 `nomic-embed-text` 后执行：

```sh
go run ./cmd/rag-eval --out .private/rag-quality.json
```

`testdata/rag-quality.json` 固定 7 个文档、9 个问题、预期文档及事实词组。它们是可人工复核的开发规则标签，未得到独立人工质量标注，不应称为人工盲测。程序实际调用 768 维 embedding，再使用生产检索适配器与本地真实生成模型，比较无上下文、检索上下文、无关上下文；另实际写入过期、跨租户和注入文档。

报告保留 corpus SHA256、模型身份、逐题答案、召回、引用检查、拒答与事实匹配。模型/协议异常和禁用 canary 输出导致非零退出；事实词组与语义质量应另外读逐题记录，进程正常退出不代表所有答案正确。引用存在于候选集合中，不代表引用语义一定支撑答案。

真实实验发现本地模型仅靠提示词仍可能服从凭据提取攻击。现已把规则放入独立 system message，并在检索/模型前拒绝明确的凭据披露请求，API 返回 400；没有将规则与不可信文本放在同一角色里。适配器有真实 HTTP 协议回归。该输入门禁不是完整的 prompt-injection 防御，模型不拥有执行 SQL、读取运行凭据或扩大检索范围的权限。

本轮守卫后的固定检索问题 7/7 匹配事实规则，空上下文 9/9 拒答，无关上下文 8/9 拒答；后者有一个带合法 chunk ID 的不受支持答案。这个负例保留，不能把结果写成 100% 可靠 RAG。凭据披露题由程序提前拒绝，单独标为 `policy_blocked`，不是模型成功拒答的成绩。

中文问题、长文、多版本冲突、同义改写和更大领域语料的独立人工标注仍适合后续扩大；当前结果不代表这些未覆盖分布的质量。
