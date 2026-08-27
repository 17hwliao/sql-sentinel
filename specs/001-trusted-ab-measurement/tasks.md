# 任务清单：001-trusted-ab-measurement

**等级**：L3 | **方案**：[plan.md](./plan.md) | **日期**：2026-08-26
**12 项。`[P]` = 可并行（改动文件不重叠）。/implement 每次只做 1–3 项。**

## 垂直切片顺序

本项目类型：**Go CLI / 后端**（仓库当前无 `go.mod`，T001 负责建立）

① 能跑的 CLI（`--help` 有输出）② 两容器起来 + 相同建表 DDL + 造数端到端落库
③ 两侧快照 SHA-256 一致性判定跑通

**T001–T003 即最小可运行垂直切片**：做完这三项，就能用真实的两个 MySQL 容器造出数据并观察到两侧行数一致，不需要等统计与报告模块。

## 任务

- [x] **T001** 建立模块与 CLI 骨架 `go.mod`、`cmd/sentinel/main.go` —— 完成：四个子命令 `seed`/`verify`/`index`/`bench` 已注册，flag 解析可用，未实现的子命令返回明确「未实现」而非 panic；验证：`go build ./... && go vet ./... && go run ./cmd/sentinel --help`
- [x] **T002** 双容器编排与相同建表 DDL `deployments/docker-compose.yml`、`deployments/mysql/my.cnf`、`internal/dataset/schema.go` —— 完成：两容器 healthy（13306/13307），两侧执行**完全相同**的 `orders` 建表 DDL，`SHOW CREATE TABLE` 输出一致；验证：`docker compose -f deployments/docker-compose.yml up -d` 后 `go run ./cmd/sentinel seed --rows 0 --target both`
- [x] **T003** 确定性造数与 `seed` 子命令 `internal/dataset/generate.go`、`internal/dataset/writer.go` —— 完成：小规模（`--rows 10000`）写入两侧，行内容只由 `(seed, rowIndex)` 决定，同 seed 重跑两侧行数与抽样内容一致；验证：`go run ./cmd/sentinel seed --rows 10000 --seed 42 --target both`
- [x] **T004** 分块快照摘要与 `verify` 子命令 `internal/snapshot/digest.go`、`internal/snapshot/compare.go` —— 完成：主键升序分块 + 逐行规范化拼串 + SHA-256 增量，输出两侧摘要并判定一致/不一致（spec 验收 2）；验证：`go run ./cmd/sentinel verify --seed 42`
- [x] **T005** 造数与摘要的确定性单测 `internal/snapshot/digest_test.go`、`internal/dataset/generate_test.go` —— 完成：同 seed 得同摘要；改动任一行字段则摘要必变（覆盖**造数确定性回归风险**，这是「两侧各自造数」这一简化的唯一保险）；验证：`go test ./internal/dataset/... ./internal/snapshot/...`
- [x] **T006** `index` 子命令：仅 candidate 建候选索引 `internal/dataset/index.go` —— 完成：在 candidate 建 `idx_cand_user_status_created (user_id, status, created_at)` 并 `ANALYZE TABLE orders`；对 baseline 调用时**必须拒绝**（spec 验收 5 前置）；验证：`go run ./cmd/sentinel index --target candidate`
- [x] **T007** 单侧测量：预热 + 重复计时 `internal/measure/runner.go` —— 完成：对固定 SQL 与固定绑定参数预热 N 次丢弃，再采集 M 轮延迟样本，单侧可独立调用；验证：`go test ./internal/measure/...`
- [x] **T008** 交替测量与三态判定 `internal/measure/stats.go`、`internal/measure/verdict.go` —— 完成：中位数、MAD×1.4826、**两侧各自独立标定**、门槛 = **2 ×** 两侧 scaled MAD ratio **较大值**（严格超过才算显著）、输出 Better/Worse/NotSignificant（spec 验收 3）；验证：`go test ./internal/measure/...`
- [x] **T009** 判定逻辑单测 `internal/measure/verdict_test.go` —— 完成：三条断言——1.4826 缩放系数正确、门槛确为两侧较大值（不是全局 noise floor）、中位数差未超门槛时**必须**返回 NotSignificant（覆盖**验收 3、4 与判定逻辑风险**）；验证：`go test ./internal/measure/...`
- [x] **T010** 报告输出 `internal/report/result.go` —— 完成：`validation_result.json` + 终端摘要，含环境、MySQL 版本、`dataset_version`/`generator_version`/`distribution_version`、两侧中位数与 scaled MAD ratio、判定；验证：`go run ./cmd/sentinel bench --calibrate 20 --rounds 5`
- [x] **T011** 拒绝路径：快照不一致时禁止出报告 `internal/measure/gate.go`、`internal/measure/gate_test.go` —— 完成：状态未达 `snapshot_verified` 时 `index` 与 `bench` 均拒绝并说明原因（覆盖**验收 6 与状态迁移风险**）；验证：`go test ./internal/measure/...`
- [x] **T012** 扩到 100 万行跑完整链路并回写 Spike 结果 `README.md`、`specs/001-trusted-ab-measurement/plan.md` —— 完成：真实跑一次 seed→verify→index→bench，把两侧实测 scaled MAD ratio 写回 plan.md「Spike 记录」的结果/结论；README **追加**运行步骤（不改既有内容）；验证：`go build ./... && go vet ./... && go test ./...`

## 批次

| 批次 | 任务 | 本批验证命令 |
| --- | --- | --- |
| 1 | T001–T003 | `go build ./... && go vet ./... && go run ./cmd/sentinel seed --rows 10000 --seed 42 --target both` |
| 2 | T004–T006 | `go test ./internal/dataset/... ./internal/snapshot/... && go run ./cmd/sentinel verify --seed 42` |
| 3 | T007–T009 | `go test ./internal/measure/...` |
| 4 | T010–T012 | `go build ./... && go vet ./... && go test ./...` |

## 测试立项说明

只立三项测试任务（T005 / T009 / T011），各自对应一个具体风险，不按数量铺开：

- **T005** → 造数确定性。「两侧各自造数、不做 dump/restore」是本方案最大的赌注，这是唯一保险。
- **T009** → 判定正确性。缩放系数、门槛来源、NotSignificant 兜底，错一个则全部结论失效。
- **T011** → 状态门禁。快照不一致却出报告，比不出报告更危险。

写放大、并发写、SQL 改写等价性均不在本切片，**不立测试任务**。
