# 批量轮次测量计划

## 1. 实现决策

- 增加 `executions_per_round`，默认值为 1；每一轮的样本值为 N 次完整查询总耗时除以 N。
- 批量轮次复用同一个 prepared statement，但每次调用都重新 `QueryContext`、完整读取 rows、检查 `rows.Err` 并关闭结果集。
- 计时从第 1 次查询前开始，到第 N 次结果集关闭后结束；中途错误或取消时返回错误，不生成部分样本。
- 报告顶层新增 `measurement_protocol.executions_per_round`；人类摘要同时显示该值。`raw_ms` 继续表示单次折算值。
- 不修改 `Decide()`、`Admit()`、10% 噪声线、候选索引、Query Case 或 Docker/MySQL 配置。

## 2. 模块与数据流

```text
CLI --executions-per-round=N
  -> Session.Warmup / Session.Once(N)
  -> N × 完整 QueryContext + rows 消费
  -> elapsed / N
  -> calibration / measurement raw samples
  -> Result.MeasurementProtocol
  -> JSON + terminal summary
```

纯函数层抽出“执行 N 次并折算”的循环，使用注入的执行函数测试次数、失败短路和取消短路；
`Session` 只负责把真实 prepared statement 适配到该循环。这样测试不依赖 MySQL，也不把计时边界复制到两处。

## 3. 任务顺序与验证

1. 实现批量轮次内核和纯逻辑测试：N=1、N=3、执行失败、context 取消、非法 N。
2. 把 `Session`、`RunOptions`、CLI 的预热/标定/正式测量接到 N，并验证 `--help`、非法参数与全量 Go 检查。
3. 扩展报告协议、JSON 与摘要测试；确认 `rounds=0` 仍无方向 verdict。
4. 在现有 100 万行双容器数据上执行一次 N=1 对照和两次 N=10 标定加一次 N=10 正式测量；仅实验期间停止已获许可的无关容器，结束后恢复。
5. 将真实结果写回本 plan，运行全量检查；若准入不通过，保留 L1 并停止，不临时调参。

## 4. 风险与降级

| 风险 | 处理 |
| --- | --- |
| N 次执行使 baseline 单轮过长 | N 默认 1；真实实验先用 10，不继续增大 |
| 批量掩盖单次异常 | 原始样本仍按“每轮折算单次”保留；报告显式标出 N |
| N=10 仍不达标 | 报告保持 L1，记录结果；不修改准入规则 |
| 其他容器干扰实验 | 实测前先声明、仅 stop 指定容器，完成后 docker start 恢复 |

## 5. 实验记录（待执行）

假设：批量轮次会降低 candidate 的相对噪声，同时不改变方向判定和快照门禁。
命令：N=1 对照一次；N=10 两次 `--calibrate 20 --rounds 0`，再一次 `--calibrate 20 --rounds 5`。
通过门槛：两次 N=10 标定每侧噪声比均 ≤10%，门禁通过，且正式报告 admission 为 L2。
结果：N=1 candidate 16.57%；N=10 两次 candidate 6.38% / **10.19%**，baseline 0.86% / 1.63%；正式运行 candidate 9.62%、baseline 2.34%、148.433ms → 2.634ms、`Better`。全部报告快照一致，容器已恢复。
结论：批量协议降低了噪声，但第二次独立标定超 10%，阶段整体为 L1，不能声称收益；正式报告单次 admission=L2 不足以代替跨运行稳定性准入。
