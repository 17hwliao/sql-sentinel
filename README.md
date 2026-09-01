# SQL Sentinel：带影子库验证闭环的 SQL 诊断与 PR 评审 Agent

> 文档导航：[docs/README.md](docs/README.md)。该导航保留并分类现有规格、阶段记录与证据，不替代原始文档。

## 1. 项目目标

SQL Sentinel 面向 GitHub PR 中的 SQL Migration 和显式 Raw SQL 字符串，自动发现数据库风险并生成可验证的优化建议。

它不允许 LLM 直接“优化 SQL”或执行 DDL。确定性规则、表结构、执行计划和影子库实测构成证据；Eino Agent 只负责选择下一步取证、生成受限候选方案和解释结果。

## 2. 首版输入范围

- `.sql` migration 文件；
- Go 源码中的显式 Raw SQL 字符串字面量；
- 手动上传的慢 SQL 模板及其参数样本。

首版不静态还原 GORM 链式调用。动态条件、运行时拼接和 ORM 行为会使静态提取准确率不可接受，应在 README 中明确为已知边界。

## 3. 核心原则

1. **证据约束**：LLM 不猜答案；结论必须指向本次规则、Schema、Plan 或验证结果。
2. **受限候选**：LLM 输出 `CandidateSpec`，Go 校验后才编译成 DDL 或 SQL 改写。
3. **影子验证**：候选索引和改写只能在隔离影子 MySQL 中验证。
4. **置信度分级**：弱证据不伪装成实测结论。
5. **无副作用诊断**：读 SQL 可在影子库 `EXPLAIN ANALYZE`；写 SQL 默认只做 `EXPLAIN FORMAT=JSON`。

## 4. 总体架构

```mermaid
flowchart LR
    GH["GitHub PR / SQL 输入"] --> WH["Webhook 验签与幂等"]
    WH --> Q["验证任务队列"]
    Q --> W["SQL Sentinel Worker"]
    W --> PARSE["SQL Parser + 静态规则"]
    PARSE --> BASE["Baseline Shadow DB"]
    BASE --> GRAPH["Eino Evidence Graph"]
    GRAPH --> TOOLS["Schema / Plan / Stats Tools"]
    GRAPH --> SPEC["CandidateSpec"]
    SPEC --> VALIDATE["Go 校验与 DDL 编译"]
    VALIDATE --> CAND["Candidate Shadow DB"]
    CAND --> BENCH["等价性 + 基准验证"]
    BENCH --> GRAPH
    GRAPH --> REPORT["置信度报告 / PR 评论 / 审批"]
    GRAPH --> MESH["AgentMesh 模型服务"]
```

## 5. 证据置信度

| 级别 | 证据 | 可以说什么 |
| --- | --- | --- |
| L0 | SQL AST、静态规则 | 存在静态风险，尚未验证 |
| L1 | Schema、索引、统计、EXPLAIN | 优化器计划级推断 |
| L2 | 可控分布基准数据、影子库 EXPLAIN ANALYZE、重复测量 | 基准数据集验证通过 |
| L3 | 脱敏代表性数据或工作负载回放 | 代表性工作负载验证通过 |

历史案例只能作为候选生成的 `Historical Prior`，不得作为本次最终报告的验证证据。

## 6. Eino Graph 设计

Graph State 建议包含：

```text
request / parsed_sql / baseline_plan / evidence[] / hypotheses[]
candidate_specs[] / validation_results[] / confidence / tool_budget
elapsed_budget / report / unresolved_assumptions
```

节点建议：

1. `Admission`：验证输入范围、SQL 类型、安全策略和预算；
2. `StaticAnalysis`：运行 AST 规则，生成初始风险；
3. `BaselineEvidence`：读取 Schema、索引、统计，获得 Baseline Plan；
4. `DecideNextEvidence`：Agent 选择下一个只读 Tool；
5. `ExecuteTool`：执行列类型、索引、统计、Plan、慢日志等 Tool；
6. `GenerateCandidateSpec`：仅输出结构化候选；
7. `ValidateCandidate`：校验、编译、在 Candidate 影子库验证；
8. `Evaluate`：判断结果集等价、读收益与写入代价；
9. `StopOrLoop`：证据充分则结束，否则在预算内回到取证；
10. `Report`：输出已确认结论、置信度和未验证假设。

循环上限建议：最多 3 轮 Tool 决策、最多 2 个候选索引、总耗时与 Token 均有限额。达到上限时必须 graceful degradation，不得强行给出优化结论。

## 7. CandidateSpec 与安全

示例：

```json
{
  "kind": "add_index",
  "table": "orders",
  "columns": ["user_id", "status", "created_at"],
  "order": ["ASC", "ASC", "ASC"],
  "reason": "...",
  "evidence_ids": ["plan-001", "schema-002"]
}
```

Go 校验器必须验证：库表字段是否存在、索引列数量与类型、索引名、白名单 DDL、重复索引、存储预算和任务权限。校验通过后才允许在影子库执行。

生产库永不执行 CandidateSpec；生产场景只生成带审批信息的建议。

## 8. 影子库与可信计时

### 阶段 0：必须先完成的 Spike

- 使用 Go 造数或 sysbench 构造高/低基数、Zipf 倾斜、NULL 比例可控的数据；先 100 万行跑通，再根据硬件扩至 500 万行；
- 构建 Baseline 与 Candidate 两套独立 MySQL 容器，确保数据快照一致；
- Baseline 测量、候选索引创建、`ANALYZE TABLE`、Candidate 测量形成完整对比；
- 正常 SQL 多次预热和重复执行，记录中位数/P95；`EXPLAIN ANALYZE` 主要用于 actual rows、loops 和 iterator 证据；
- 每个固定环境先独立完成至少 20 轮噪声标定，记录 `noise_floor = MAD(latency) / median(latency)`；每个 baseline/candidate Case 再至少重复 5 轮；
- 噪声地板原则上低于 10%，且只有中位数变化超过 `2 × noise_floor` 才报告为收益；MAD 对长尾延迟比 RSD 更稳健；
- 所有验证任务按影子库串行执行，避免 CPU、IO 与 Buffer Pool 干扰；
- 报告环境、MySQL 版本、Buffer Pool 配置、数据集版本、规则集和模型版本。

### 写入代价与等价性

- 每个候选索引除读取收益外，测量单写和 N 并发 INSERT/UPDATE 的吞吐、P95、锁等待、索引大小和建索引耗时；
- “不建议建索引”必须覆盖两类反例：低选择性/小表使优化器全扫更优；以及读查询变快但并发写入代价超过约定阈值；
- SQL 改写必须做语义等价性验证：带 `ORDER BY` 的查询比较顺序，无排序查询比较结果多重集合；处理字段类型与 NULL 语义；
- 大结果集使用稳定序列化、分块哈希与行数校验，必要时全量比较。

手工注入 `innodb_table_stats` / `innodb_index_stats` 和自定义 Histogram 属于计划模拟实验，不替代代表性数据上的真实验证。

## 9. 技术栈与选型理由

| 技术 | 用途 | 为什么需要 |
| --- | --- | --- |
| Go + database/sql | 计划、统计、影子库控制 | 诊断核心需要精确 SQL 控制，不能完全交给 ORM |
| MySQL 8.0.18+ | EXPLAIN ANALYZE、统计、索引验证 | 项目的主目标数据库 |
| Eino Graph / Tool / Callback | 受限多轮取证与可观测 | Agent 负责决策，不承担确定性校验 |
| RabbitMQ 或 Redis Streams | 耗时验证队列、串行资源调度 | 影子库验证会互相干扰且需要任务状态机 |
| MySQL + GORM | 任务、报告、审批、CandidateSpec | 常规业务状态持久化 |
| Redis | Webhook 短期去重、任务状态缓存 | 热路径防重和状态读取 |
| Go-zero | Webhook、管理 API、审批接口 | HTTP 管理面需求清晰 |
| JWT + Casbin | 库表级查看与审批权限 | 诊断与审批属于高风险操作 |
| Docker Compose / Testcontainers | 隔离双影子库 | 验证环境必须可复现 |
| Zap + Viper + Go test | 日志、配置、规则与集成测试 | 保证调试和复现能力 |

可选扩展：ES 用于历史实测数据检索、过滤和聚合；PostgreSQL 用于一次性跨引擎执行计划对照实验。两者不是 MVP 依赖。

## 10. 数据模型（建议）

- `review_tasks`：输入、状态、预算、租户、PR 信息；
- `evidence_items`：来源、内容摘要、级别、版本、关联任务；
- `candidate_specs`：结构化候选、校验状态、审批状态；
- `validation_runs`：环境、噪声标定的 `noise_floor_mad_ratio`、基线/候选指标、写压指标、等价性结果；
- `reports`：最终报告、置信度、未验证假设；
- `webhook_deliveries`：Delivery ID、签名状态、幂等状态；
- `prompt_versions` / `ruleset_versions` / `dataset_versions`：可复现性元数据。

## 11. 验收与评测

最终机器可读快照见 [`evaluation_report.json`](evaluation_report.json)。它记录评测时的 Git SHA、UTC 时间、
样例集 `dataset_version` 与 manifest SHA-256；当前快照是在有未提交 015 变更的工作树上生成，因此该事实也被
显式记录，而不是假装它对应一个干净提交。

| 维度 | 实测值 / 状态 | 诚实边界 |
| --- | --- | --- |
| 静态规则召回率 | **7/7（100%）**：4 个 signal、3 个拒绝码；样例集 `eval-sql-smells-v1` | 只评估人工维护的 7 个已知坏味道 SQL，不外推到真实 SQL 语料。 |
| 验证通过率 | 方向性 `Better` 为 **1/1**：1,000,000 行、单一 `hot_user_paid_recent_desc` Case | candidate 标定噪声 **13.02%** 超过 10% 门槛；收益资格为 **L1/false**，因此证据合格率为 **0/1**，不得声称性能收益。 |
| 安全性 | **已重跑并通过** 5 个固定包组：CandidateSpec 严格解码、Agent 注入、锁定读、webhook 签名/429/降级、pipeline 停止/部分证据 | 这是现有对抗测试清单，不等同生产渗透测试或真实 GitHub 重放演练。 |
| 最优接近度 | **豁免** | 首版没有“LLM 提案 → A/B 验证 → 反馈迭代”的自动索引推荐闭环。 |
| 结果等价性 | **范围外** | 项目没有 SQL 改写能力；不能把只读 SQL 准入或 EXPLAIN 当作改写后的结果等价性验证。 |
| 成本 | **部分可测**：最终 smoke 的四步 wall-clock 为 sql_admit 0.602ms、candidate_explain 4030.042ms、plan_compare 1.801ms、diagnosis 0.515ms | 时序是一次运行的操作观察，不是基准结论。真实 LLM 尝试数为 0；Token 用量未埋点，明确豁免。 |

最终本机 smoke 的操作记录：以 `examples/candidate-explain-query.sql` 和
`examples/candidate-explain-index.json` 启动只监听 `127.0.0.1` 的 `webhook-serve`，用一次性 HMAC-SHA256
签名的模拟 unified diff 投递到 `/webhook/pr`。实际返回 HTTP 202，依序完成 `sql_admit`、`candidate_explain`、
`plan_compare`、`diagnosis`；评论与五项工件均落盘，四份步骤文件 SHA-256 已复算匹配，summary 使用
`pipeline_report_with_own_sha256_blank` canonical 范围。该链路仍为 L1/false，不是性能收益结论。

## 12. 推荐目录

```text
sql-sentinel/
  cmd/{api,worker,verifier,datagen}/
  internal/{webhook,parser,rules,evidence,agent,candidate,validator,benchmark,approval}/
  pkg/{mysqlplan,contracts}/
  migrations/
  deployments/docker-compose.yml
  datasets/
  tests/{unit,integration,benchmark}/
```

## 13. 阶段 0 本地运行步骤（已实测）

环境：Windows 本机跑 Go，MySQL 仅以两个独立 Docker 容器提供，不使用 WSL。
本机需已安装 Go 1.26+ 与 Docker Desktop（含 Compose v2）。

### 启动双影子库

```bash
docker compose -f deployments/docker-compose.yml up -d --build
```

两个容器：`sqlsentinel-baseline`（127.0.0.1:13306）、`sqlsentinel-candidate`（127.0.0.1:13307）。
`my.cnf` 通过 `deployments/mysql/Dockerfile` 以 `COPY --chmod=0644` 烧进镜像 ——
用 Windows bind mount 挂载会让配置在容器内变成 world-writable，MySQL 会静默忽略它，
参数回落默认值（buffer pool 128M、`time_zone=SYSTEM`），测量环境即失去可信性。

确认两侧参数真的生效：

```bash
docker exec sqlsentinel-baseline mysql -uroot -psentinel -e \
  "SELECT VERSION(), @@time_zone, @@innodb_buffer_pool_size, @@performance_schema"
```

### 造数、校验、建候选索引、测量

```bash
go run ./cmd/sentinel seed   --rows 1000000 --seed 42 --target both
go run ./cmd/sentinel verify --seed 42
go run ./cmd/sentinel index  --target candidate
go run ./cmd/sentinel bench  --calibrate 20 --rounds 0 --out calibration-1.json
go run ./cmd/sentinel bench  --calibrate 20 --rounds 5 --out validation_result.json
```

顺序不可调换，且由程序强制：

- 两侧在**无候选索引**状态下各自确定性造数（行内容只由 `(seed, rowIndex)` 决定，
  因此无需 dump/restore 同步快照）；
- `verify` 按主键升序分块算 SHA-256，两侧不一致直接拒绝；
- `index` 只接受 `--target candidate`，对 `baseline` 会拒绝且不发出任何 DDL；
- `index` 与 `bench` 在动手前都**现场重算**两侧快照，不复用上一条命令的结果；
  `bench` 还要求两侧 MySQL 版本一致且 candidate 候选索引存在。

### 验证

```bash
go build ./... && go vet ./... && go test -count=1 ./...
```

### 已知限制

100 万行下 candidate 侧中位数约 2.9ms，scaled MAD 约 0.38ms，噪声比 13%，
超过阶段 0 约定的 10% 门槛。噪声比是相对量，查询越快越难达标。
因此当前环境**不足以据此声称读收益**，详见
[specs/001-trusted-ab-measurement/plan.md](specs/001-trusted-ab-measurement/plan.md) 的 Spike 记录。

### 报告消费规则

`verdict=Better` 只表示本次统计测量的方向性结果，**不等于**已经取得性能收益证据。
任何人、脚本或后续 Agent 只有在报告中读取到
`admission.eligible_for_performance_claim=true` 时，才可以将该报告用于声称性能收益；
否则必须以 `admission.evidence_level` 和 `admission.rejection_reasons` 说明证据等级与拒绝原因。

已有的 `validation_result.json` 与 `calibration-1.json` 是历史原始实验记录，不回填新增字段；
重新运行 `bench` 生成的新报告才会包含 `admission` 块。

### 批量轮次实验（N=10）

为降低极快查询被调度抖动主导的风险，`bench` 支持
`--executions-per-round N`：每个样本连续完整执行 N 次同一 prepared SQL，
再以 `batch_elapsed / N` 记录单次折算耗时；报告的
`measurement_protocol.executions_per_round` 会记录这一口径。

在同一份 100 万行数据、相同快照与候选索引上，N=1 的 candidate 标定噪声为 16.57%；
N=10 的两次独立 20 轮标定分别为 6.38% 与 **10.19%**（baseline 为 0.86% 与 1.63%）。
N=10 正式测量内部标定为 baseline 2.34%、candidate 9.62%，测量中位数为
148.433ms → 2.634ms，单次报告的方向判定为 `Better`。

但是阶段协议要求两次独立标定都不超过 10%，第二次未通过。因此本批实验的**整体证据仍为 L1**，
不得据此声称性能收益。`batched-n10-validation.json` 中的 L2 只基于它自身那一次标定，
尚未表达跨报告的稳定性要求；后续应把“多次运行的系列准入”设计成独立功能，而不是选择性忽略失败报告。

### 跨运行系列准入

使用 `admit-series` 汇总预先指定的两份以上 calibration 报告和一份正式 measurement 报告：

```bash
go run ./cmd/sentinel admit-series \
  --calibration batched-n10-calibration-1.json \
  --calibration batched-n10-calibration-2.json \
  --measurement batched-n10-validation.json \
  --out series_admission.json
```

命令会拒绝 dataset version、快照摘要、MySQL 版本、Query Case、候选索引或
`executions_per_round` 不一致的输入，也拒绝重复使用同一份 calibration 报告充数。
只有所有 calibration 的两侧噪声均不超过 10%，且 measurement 自身为 `Better`、单份准入合格时，
才输出系列 `eligible_for_performance_claim=true` / `L2`。

对当前三份 N=10 报告，实际输出为 `false` / `L1`，原因是
`calibration_noise_exceeds_limit (batched-n10-calibration-2.json)`；输入报告不会被修改。

## 14. CandidateSpec：候选索引的受限输出

Agent 不能直接输出或执行 DDL。它只能给出 `CandidateSpec` JSON，Go 会严格解码、校验并预览确定性 DDL：

```bash
go run ./cmd/sentinel candidate-preview --in examples/candidate-index.json
```

输出示例：

```sql
CREATE INDEX `idx_cand_user_status_created` ON `orders` (`user_id` ASC, `status` ASC, `created_at` DESC)
```

该命令只读取文件并输出 stdout，不连接 MySQL、不会执行 DDL。Spec 只允许普通二级索引、1–4 个安全标识符列，
索引名必须以 `idx_cand_` 开头；`sql`、`ddl` 等未知字段会被拒绝，不能作为自由 SQL 通道。

## 15. 只读 SQL 准入（L0 静态信号）

在把一条 SQL 交给后续影子库取证前，可先执行本地、无副作用的准入检查：

```bash
go run ./cmd/sentinel sql-admit --in examples/read-only-query.sql
```

示例输出：

```json
{"accepted":true,"evidence_level":"L0","signals":["select_star","leading_wildcard_like"]}
```

该命令只接受单条 `SELECT` 或 `WITH ... SELECT`，会拒绝空输入、多语句、DDL/DML、
`INTO OUTFILE` / `DUMPFILE`、`FOR UPDATE` 和 `LOCK IN SHARE MODE`。扫描时忽略字符串、
反引号标识符和普通注释中的关键字或分号；MySQL 可执行注释会保守拒绝。命令只读取文件并写 JSON 到 stdout，
不连接 Docker 或 MySQL，也绝不执行 SQL。

`signals` 仅是需要继续调查的静态 L0 线索（目前包括 `select_star`、`leading_wildcard_like`、
`function_on_probable_column`），不是性能结论；任何优化判断仍须由 schema、`EXPLAIN` 和影子库验证支撑。

## 16. Candidate 影子库 EXPLAIN 门禁

在已通过 `seed` 与 `verify`、且 baseline/candidate 快照一致的前提下，使用受限 CandidateSpec
在 candidate 影子库创建或复用候选索引，并取得执行计划：

```bash
go run ./cmd/sentinel candidate-explain \
  --sql examples/candidate-explain-query.sql \
  --candidate examples/candidate-explain-index.json \
  --out candidate_explain_report.json
```

该命令在连接数据库前先复用 `sql-admit` 和严格 CandidateSpec 校验；随后检查两侧 MySQL 版本、
现场重算 `orders` 数据快照，并在 candidate 确认表/列与同名索引定义。只有 candidate 会执行受限
`CREATE INDEX` 和 `ANALYZE TABLE`；baseline 仅用于版本和快照门禁，绝不接收 DDL。输入 SQL 仅以
`EXPLAIN FORMAT=JSON` 形式提交，原 SQL 不会执行。

输出报告包含快照、MySQL 版本、L0 静态信号、确定性候选 DDL、索引创建状态和 EXPLAIN JSON。它固定标为
L1，`eligible_for_performance_claim=false`：计划显示了什么并不等于该索引有性能收益；要得出收益结论仍须执行
受控 A/B 测量并通过既有的准入门槛。

## 17. Baseline/Candidate EXPLAIN 对照

当需要把“candidate 的计划是什么”扩展为“它与没有候选索引的 baseline 有何计划差异”时，运行：

```bash
go run ./cmd/sentinel explain-compare \
  --sql examples/candidate-explain-query.sql \
  --candidate examples/candidate-explain-index.json \
  --out explain_comparison_report.json
```

该命令先执行与 `candidate-explain` 相同的 SQL、CandidateSpec、MySQL 版本、快照、candidate schema
与索引定义门禁。随后 baseline 和 candidate 都只运行 `EXPLAIN FORMAT=JSON`，候选侧可创建或复用受限索引并
`ANALYZE TABLE`；baseline 永不接收 DDL，原 SQL 也不会执行。

对照报告同时保留两侧原始 JSON，并只提取可稳定陈述的访问事实：表访问方式、候选/实际索引、预计扫描行数、
覆盖索引与 filesort 状态。示例实测中 baseline 为全表扫描且 `uses_filesort=true`，candidate 使用
`idx_cand_status_amount`、`uses_filesort=false`。报告固定为 L1 且
`eligible_for_performance_claim=false`；预计扫描行数、cost 或 filesort 差异都是计划证据，不是性能收益结论。

## 18. 受限诊断假设

在已经生成 L1 EXPLAIN 对照报告后，可将其中已有事实转换为后续取证用的机器可读假设：

```bash
go run ./cmd/sentinel diagnose-plan \
  --in explain_comparison_report.json \
  --out diagnostic_hypotheses.json
```

命令只读取严格的本项目 L1 对照 JSON；未知字段、多个 JSON 值、非 L1 输入或已经具备性能收益资格的输入都会拒绝。
输出会记录输入文件 SHA-256、快照与 MySQL 版本，并且仅在事实存在时生成稳定的诊断码：
`candidate_index_removes_filesort`、`candidate_index_changes_access_path`、
`candidate_index_reduces_estimated_scan`。

这不是索引建议，也不会连接 MySQL、执行 SQL 或 DDL。每条假设都明确要求先经过
`controlled_ab_measurement` 与 `series_admission`；诊断报告固定为 L1 且
`eligible_for_performance_claim=false`。

## 19. 诊断—测量证据绑定

`bind-evidence` 只读取既有的诊断、EXPLAIN 对照、正式 measurement 和系列准入 JSON，使用
诊断所记录的对照 SHA-256、快照、MySQL 版本、SQL 原文、候选索引和 measurement 文件身份进行精确关联：

```bash
go run ./cmd/sentinel bind-evidence \
  --diagnosis diagnostic_hypotheses.json \
  --comparison explain_comparison_report.json \
  --measurement validation_result.json \
  --series series_admission.json \
  --out evidence_binding_report.json
```

报告只会在全部身份一致且既有系列准入已经是
`eligible_for_performance_claim=true` / `L2` 时继承该资格；命令本身不会重新测量或升级证据。
因此 `binding_complete=false`、L1 与稳定的 `rejection_reasons` 是正常的证据状态，**不是 bug**：它说明这些
报告不能被安全地关联来声称性能收益。未知字段、损坏 JSON 或尾随 JSON 值会被拒绝，而不是宽松解析。

## 20. 端到端只读证据管线

`pipeline` 把只读 SQL 准入、candidate shadow EXPLAIN、baseline/candidate 计划对照和受限诊断串成一次运行：

```bash
go run ./cmd/sentinel pipeline \
  --sql examples/candidate-explain-query.sql \
  --candidate examples/candidate-explain-index.json \
  --out-dir pipeline-output
```

`--out-dir` 必须不存在或为空。成功时会写入 `sql_admission.json`、
`candidate_explain_report.json`、`explain_comparison_report.json`、
`diagnostic_hypotheses.json` 和 `pipeline_report.json`。前四份步骤工件带直接输入 SHA-256；汇总记录四个已完成步骤、
每份工件的 SHA-256，以及自身的规范化 SHA-256（将该自摘要字段置空后计算，避免不诚实的自引用哈希）。

若 SQL 被拒绝，管线立即停止但仍写入已完成的 `sql_admission.json` 和 `pipeline_report.json`，然后打印汇总路径、
停止步骤及稳定拒绝码，并以非零码退出。例如 `SELECT ... FOR UPDATE` 的停止步骤为 `sql_admit`，拒绝码为
`locking_read`。这同样是正常、可消费的证据结果：不会执行后续 EXPLAIN、计划对照或诊断，更不能声称性能收益。

## 21. Eino 受限 CandidateSpec 提案

`propose-candidate` 只读取 012 成功管线的四份步骤工件，并由单节点 Eino 图请求一个 CandidateSpec JSON 草案：

```bash
go run ./cmd/sentinel propose-candidate \
  --provider openai \
  --evidence-dir pipeline-output \
  --out candidate_proposal_report.json
```

运行时须提供 `EINO_MODEL`，以及 `OPENAI_API_KEY`；本机 OpenAI 兼容网关可改用
`ANTHROPIC_AUTH_TOKEN`。认证回退顺序固定为：非空 `OPENAI_API_KEY` 优先，否则读取
`ANTHROPIC_AUTH_TOKEN`。默认端点是 OpenAI；设置可选 `EINO_BASE_URL` 才会改用例如
`agentrouter.org` 所提供的 OpenAI 兼容端点。端点、模型名和密钥只从环境读取；密钥**永不落盘**，也不会写入
命令参数、日志或报告。成功报告只记录模型名、四份输入 SHA-256、尝试次数和通过严格校验的 CandidateSpec。

证据内的 SQL、signals、索引名和诊断码是**不可信数据**，被置于 `<untrusted_evidence>` 边界中，绝不作为
模型指令。每个响应都必须先经 `candidate.DecodeStrict` 与 `candidate.Validate`；DDL、SQL、未知字段、尾随 JSON
或其他非法草案最多重试 3 次，原始输出绝不落盘。报告固定为 L1 与
`eligible_for_performance_claim=false`。提案在通过后续 candidate shadowgate 前仍只是文本，**没有任何执行语义**，
更不能成为性能收益声称。

T003 于 2026-08-28 尝试真实调用前进行了运行时门禁：本机进程中 `OPENAI_API_KEY`、
`ANTHROPIC_AUTH_TOKEN`、`EINO_MODEL` 和 `EINO_BASE_URL` 均未配置。因此命令以
`runtime_model_configuration_missing` 受控拒绝，模型名/端点均为“未配置”、尝试次数为 0，未发送网络请求，
也没有可验证的模型草案。这是配置不足时的正常安全状态；待以环境变量提供网关端点、模型和凭据后，才可重新运行。

## 22. 本地 PR 评审 Webhook

`webhook-serve` 是一个只用于本机模拟 delivery 的 HMAC 入口，**只能**监听 `127.0.0.1`，拒绝
`0.0.0.0`、`localhost` 和其他地址；它不是 GitHub 集成，也不会发布远程评论。启动前须设置仅用于进程的
`SQL_SENTINEL_WEBHOOK_SECRET`，并提供本地的严格 CandidateSpec：

```powershell
$env:SQL_SENTINEL_WEBHOOK_SECRET = '<local secret>'
go run ./cmd/sentinel webhook-serve `
  --listen 127.0.0.1:8080 `
  --candidate examples/candidate-explain-index.json `
  --out-dir webhook-deliveries
```

客户端把最小 JSON（`delivery_id`、`pr_number`、unified `diff`）原始 UTF-8 字节以 HMAC-SHA256 签名，置入
`X-SQL-Sentinel-Signature: sha256=<hex>`。缺失/错误签名和超限 body 在 JSON 解码、SQL 提取、影子库调用或
delivery 文件写入前只返回稳定拒绝码。只提取新增 `.sql` 行；每条文本先过 `sqladmit`，锁定读、多语句、零条或
多条候选 SQL 会生成本地 `verification_unavailable` 评论，绝不进入影子库。

每个有效 delivery 最多调用一次 012 `pipeline.Run`；服务默认最多同时验证 1 个（范围 1–4），满载立即返回
HTTP 429 / `queue_full`，没有无界队列。delivery 输出包含 `comment.md` 与 `webhook_report.json`，管线工件位于其
独立 `artifacts/` 子目录。评论转述真实的证据等级、资格字段、已完成步骤、拒绝码和 artifact SHA-256；影子库或
门禁失败明确标为 `verification_unavailable`，没有证据时显示 `evidence_level=none`，不会伪造 L1 或“通过”。
当前成功管线的资格仍固定为 `eligible_for_performance_claim=false`。

2026-08-28 的本机模拟投递复用 `examples/candidate-explain-query.sql` 与
`examples/candidate-explain-index.json`：HTTP 202，依序完成 `sql_admit`、`candidate_explain`、
`plan_compare`、`diagnosis`，评论记录 5 项 artifact 摘要，结果为 L1/false；这是计划与诊断证据，不是性能收益。

## 23. AgentMesh 本地联桥验证

`agentmesh-bridge-test` 只消费 [AgentMesh](../01-AgentMesh-018/) 已发布的
`POST /v1/chat/completions` SSE 契约，用于验证两个项目的本机传输互操作；它不把模型输出、SQL、证据包或
CandidateSpec 传给对方。命令只接受 `http://127.0.0.1:PORT`，从进程环境读取 `AGENTMESH_BASE_URL`、
`AGENTMESH_API_KEY`、`AGENTMESH_MODEL` 与 `AGENTMESH_COMMIT_SHA`，将 Key 仅置于 Authorization header。
输出报告固定为 `evidence_level=L1`、`eligible_for_performance_claim=false`；SSE delta 只在内存消费，绝不写入
报告、终端或日志。

当本机已有 AgentMesh 工作树及 Ollama 时，可从本仓库运行：

```powershell
$env:AGENTMESH_REPO = 'C:\path\to\AgentMesh'
$env:OLLAMA_BASE_URL = 'http://127.0.0.1:11434'
$env:OLLAMA_MODEL = 'qwen2.5:7b'
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/demo-agentmesh-bridge.ps1
```

脚本仅临时生成 bootstrap Key 与路由，在 `127.0.0.1:18185` 启动 AgentMesh；启动前会拒绝已被占用的端口，特意避开
历史 demo 使用的 18082–18084。成功报告含 AgentMesh Git commit SHA、HTTP 状态、trace ID 与 SSE 完成状态，可用于
跨项目溯源；这只是一次本机 SSE 传输实证，不是模型质量、数据库、性能、成本或生产集成结论。若环境缺失或网关失败，
脚本如实输出受控拒绝/失败，且不会伪造成功。
