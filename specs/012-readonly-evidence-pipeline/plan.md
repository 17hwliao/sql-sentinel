# 计划

1. 新建 `internal/pipeline`：定义步骤状态、稳定停止码、SHA-256 溯源和固定 L1 汇总；为 SQL 准入、shadowgate、对照和诊断报告添加可选但严格解码的直接输入溯源字段，保持已有单命令 JSON 兼容。
2. 编排器按顺序调用现有 `sqladmit`、`shadowgate.Prepare/Gate`、`plancompare.BuildReport`、`diagnosis.Analyze`；每个成功步骤先落盘、计算输出摘要、再进入下一步。拒绝写入已完成工件与汇总后停止；运行错误不继续后续步骤。
3. CLI 接收 `--sql`、`--candidate`、`--out-dir`、`--chunk`，复用已有 `openBoth`、30 分钟超时和 candidate-only DDL 边界；输出目录门禁先于任何数据库连接。
4. 任务顺序：纯编排/序列化与单元测试 → CLI 垂直切片 → 健康影子库的真实五工件运行和 README → 全量验证、复盘、提交并 fast-forward master。
5. 风险：报告换行或手工编辑会改变字节摘要，按精确溯源处理而非规范化；任一步失败时以汇总报告呈现，不尝试补跑或跳过。章程：暂未建立。

真实实验记录（T003）：2026-08-28 使用既有 `examples/candidate-explain-query.sql` 与
`examples/candidate-explain-index.json` 在运行中的影子库执行，`pipeline-output-v2/` 产出五份 JSON；
汇总有四个 completed steps 和五个摘要，四个步骤工件的文件字节 SHA-256 均复算一致，第五项是明确标注范围的
summary canonical SHA-256。为复现拒绝，临时将既有 `examples/read-only-query.sql` 加 `FOR UPDATE` 后立即恢复；
`pipeline-rejected/` 仅有 sql admission 与 summary，`stopped_step=sql_admit`、`locking_read`、CLI exit 1。未操作 Docker。
