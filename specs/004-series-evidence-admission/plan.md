# 系列证据准入计划

## 1. 实现决策

- 新建 `internal/series`，只依赖现有 `report.Result` 与 `measure.NoiseRatioLimit`，不改单份报告 schema。
- 纯函数接收带来源文件名的报告，输出 `SeriesAdmission`；读取文件和输出 JSON 留在 CLI 层。
- 先校验输入数量、报告角色和环境/协议指纹，再校验噪声与单份 admission；错误字段不产生部分成功结果。
- 拒绝原因固定排序：输入/指纹问题优先，随后 calibration 噪声、measurement 未测量、方向非 Better、单份不合格。
- 输出中保留参与文件名和每份 calibration 的噪声，便于审计；不复制 raw samples。

## 2. 数据流

```text
--calibration file ×2+ + --measurement file
  -> 读取 report.Result JSON
  -> 角色/指纹一致性校验
  -> calibration 噪声 + measurement admission 校验
  -> SeriesAdmission (L1/L2 + reasons + files)
  -> JSON + terminal summary
```

指纹是 dataset version、snapshot digest、MySQL version、case 名称/SQL/参数、候选索引、
executions-per-round 的组合。显式逐字段比对而非哈希整份 JSON，避免时间戳与 raw samples 无关变动导致误拒绝。

## 3. 任务顺序与验证

1. 定义系列输入、稳定原因码和纯 `Evaluate()`；用内存报告测试 quiet L2、噪声超标 L1、角色/单份结论边界。
2. 加入指纹校验，测试每个关键字段不一致都不能汇总。
3. 增加 CLI repeatable flags、读取/写入、摘要；验证帮助与坏参数。
4. 用阶段 003 的两个 N=10 calibration 和 N=10 validation 实跑，断言 L1/噪声原因；全量 Go 与规格校验。
5. 将命令与真实结果写入 README/plan，保留输入报告不变。

## 4. 风险与降级

| 风险 | 处理 |
| --- | --- |
| JSON 来自旧版本缺字段 | 读取失败并退出，不猜默认值 |
| 同名但不同协议的报告混入 | 指纹逐字段拒绝 |
| 只看最后一份好报告 | 至少两份 calibration，所有文件都必须合格 |
| 系列尚未合格 | 输出 L1 及原因；不调阈值、不重跑挑数 |

## 5. 实验记录（待执行）

假设：阶段 003 的三份 N=10 报告被汇总时会保留 L1，不被最后一份单次 L2 覆盖。
命令：`admit-series --calibration ...-1.json --calibration ...-2.json --measurement ...-validation.json`。
通过门槛：输出 `eligible_for_performance_claim=false`、L1、`calibration_noise_exceeds_limit` 且关联第二份文件。
结果：输出 `false`、`L1`、`calibration_noise_exceeds_limit (batched-n10-calibration-2.json)`；未操作 Docker，输入 JSON 未改。
结论：通过。跨运行准入已阻止最后一份单次 L2 覆盖第二次失败标定。
