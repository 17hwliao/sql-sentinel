# 实现方案：001-trusted-ab-measurement

**等级**：L3（取自 spec.md frontmatter，不得在此变更） | **日期**：2026-08-26

## 1. 技术选择

- `database/sql` + `go-sql-driver/mysql`：需精确控制连接、预处理与逐条计时，ORM 会掩盖开销
- 单个 CLI + 四子命令 `seed` / `verify` / `index` / `bench`：Spike 阶段无需常驻服务
- **确定性造数，两侧各自独立生成**：行内容仅由 `(seed, rowIndex)` 决定，无需 dump/restore 同步快照；本切片最关键的简化
- 快照摘要用「主键升序分块 + 逐行规范化拼串 + SHA-256 增量」：`CHECKSUM TABLE` 依赖引擎与版本，不可靠
- 金额存整数分、时间基于固定 UTC 基准、单连接顺序批写：消除浮点 / 时区 / 写序造成的两侧差异
- 统计只用标准库排序算中位数与 MAD；两容器共享同一 `my.cnf`、同组 `command` 参数与相同资源上限；端口 13306 / 13307 避开本机 3306

## 2. 目录与模块变更

- 新增 `go.mod`（模块 `sqlsentinel`，Go 1.26）
- 新增 `deployments/docker-compose.yml` + `deployments/mysql/my.cnf`：baseline / candidate 两服务
- 新增 `cmd/sentinel/main.go`：子命令分发与参数解析
- 新增 `internal/dataset/`（DDL、确定性行生成、批写）、`internal/snapshot/`（分块哈希与比对）
- 新增 `internal/measure/`（预热、交替测量、中位数 / scaled MAD / 三态判定）、`internal/report/`（`validation_result.json` + 终端摘要）
- 修改 `README.md`：仅**追加**「阶段 0 本地运行步骤」一节，不改动既有内容

## 3. 关键数据与状态

- `orders`：`id` PK、`user_id`（高基数 Zipf 热点）、`status`（5 值低基数偏斜）、`created_at`（固定基准递增带抖动）、`amount_cents`（整数）、`note`（约 10% NULL）、`payload`
- **两侧先执行完全相同的建表 DDL**；初始化后 baseline 仅保留主键，不再执行任何变更 schema 或新增二级索引的 DDL。candidate 仅在 `verify` 后新增候选二级索引
- **目标读 SQL**（bench 全程固定不变；三个绑定参数取同一组固定值，来自 Zipf 热点区且选择性已知，避免参数漂移混入噪声）：
  `SELECT id, amount_cents, created_at FROM orders WHERE user_id = ? AND status = ? AND created_at >= ? ORDER BY created_at DESC LIMIT 20`
- **候选索引**：`idx_cand_user_status_created (user_id, status, created_at)`。对应关系：`user_id` / `status` 是等值条件放前两位，`created_at` 既是范围条件又是排序键放末位 → 同一索引可完成过滤 + 范围裁剪 + 有序读取，省掉全表扫描与 filesort；baseline 无此索引，同一 SQL 必须全扫 + 排序，A/B 差异方向即读收益方向（spec 验收 5）
- **执行时序（不可调换）**：① `seed` 两侧在**无候选索引**状态下各自确定性造数 → ② `verify` 两侧 SHA-256 一致才继续，不一致直接拒绝出报告（spec 验收 6）→ ③ `index` **仅在 candidate** 建候选索引，随后 `ANALYZE TABLE orders` → ④ `bench` 先按下述噪声协议标定，再对上述固定 SQL 做 baseline / candidate 逐轮交替测量
- **噪声协议**：baseline 与 candidate 对**当前固定 Query Case 分别独立标定 20 轮**；**采样执行按 B→C 交替，统计结果按侧独立计算**；三态判定门槛 = **2 × 两侧 scaled MAD ratio 的较大值**（`SignificanceMultiplier=2.0`，对齐 spec「超过 2 × 噪声比才算收益」），中位数变化须**严格超过**该门槛才给 Better/Worse；**禁止使用全局 noise floor**（跨 Case、跨实例复用同一个噪声地板会掩盖单侧异常）
- 候选索引在 `verify` **之后**创建，故不影响快照摘要。状态迁移：`empty → seeded → snapshot_verified → indexed(仅 candidate) → measured`；`snapshot_verified` 之前不允许 `index` 与 `bench`
- `dataset_version` = `sha256(seed, rows, schema_version, generator_version, distribution_version)` 前 12 位
  - `generator_version`：行生成算法与批写顺序规则
  - `distribution_version`：Zipf 参数 s、`status` 权重、NULL 比例、`payload` 生成规则、时间抖动范围；任一分布参数变更**必须递增它**，否则不同分布会共用同一 `dataset_version`

## 4. 风险与回滚

| 风险 | 触发条件 | 回滚动作 |
| --- | --- | --- |
| 噪声超标 | **任一侧** 20 轮标定 scaled MAD ratio > 10% | 见下方「噪声超标处置顺序」 |
| 两容器资源争抢污染测量 | 并行测量 | 测量严格串行，一侧跑完再跑另一侧；两侧资源上限相同 |
| 两侧造数结果不一致 | 浮点、时区、写序 | 整数金额 + UTC 固定基准 + 单连接顺序批写；不一致由 `verify` 拦下 |
| 100 万行导入过慢 | 单条 INSERT | 批量多值 INSERT + 事务分批；行数可调，先小规模验证链路 |

**噪声超标处置顺序** —— 增加轮数只提高噪声估计的**可信度**，**不会降低噪声本身**，因此不作为处置手段：

1. 记录环境事实：CPU / 内存 / 磁盘、Docker 版本、MySQL 参数、当时的后台负载
2. 消除无关负载（关闭其他容器与占用 CPU/IO 的进程），确认两侧资源对称
3. 重新预热后重测
4. 可选实验：限制容器 `cpus`。**仅当实测确实降低噪声才保留**，否则撤回该限制
5. 仍超标 → 按 timebox 降级为单实例协议，报告置信度**只能标 L1**，不得宣称性能收益

- 章程：项目暂未建立章程（不阻塞，继续）

## 5. 验证命令

当前仓库**尚无 `go.mod`**，以下命令在第一个任务建立模块后才可执行：

```bash
go build ./... && go vet ./... && go test ./...
docker compose -f deployments/docker-compose.yml up -d
go run ./cmd/sentinel seed   --rows 1000000 --seed 42 --target both
go run ./cmd/sentinel verify --seed 42
go run ./cmd/sentinel index  --target candidate
go run ./cmd/sentinel bench  --calibrate 20 --rounds 5
```

## Spike 记录

- 假设：Windows + Docker Desktop 双容器下，两侧对固定 Query Case 的 scaled MAD ratio 均可稳定低于 10%
- 实验命令：`go run ./cmd/sentinel bench --calibrate 20 --rounds 0`
- 通过门槛：两侧各 20 轮标定后 scaled MAD ratio 均 < 10%，且同侧连续两次标定差值 < 3 个百分点
- 结果（2026-08-27，100 万行，MySQL 8.4.11，windows/amd64，dataset_version=daf8a3324856）：
  两次 20 轮标定 —— baseline 3.30% / 1.83%（漂移 1.47pp），candidate 11.93% / 13.02%（漂移 1.09pp）；
  中位数 baseline 137.9ms → candidate 2.9ms，程序判定 Better（delta −97.90%，门槛 26.04%）
- 结论：**通过门槛未达成，不声称性能收益**。漂移标准两侧均合格（< 3pp，可复现性良好），
  但 candidate 噪声比两次都 > 10%，触发第 4 节降级：置信度**只能标 L1**（计划级推断）。
  根因是噪声比为相对量 —— candidate 的 scaled MAD 仅 0.38ms，因中位数只有 2.9ms 才显得超标。
  下一步按第 4 节「噪声超标处置顺序」处置，先记录环境、消除无关负载、确认两侧资源对称后重测
