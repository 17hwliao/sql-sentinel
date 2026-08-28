# 计划

1. 新建 `internal/evaluation`：严格读取 manifest 与历史 validation JSON，调用 `sqladmit` 计算逐例检出/召回率，组装固定 L1 最终报告；测试样例、错误 manifest、历史噪声与豁免字段。
2. 为 `pipeline.Report` 增加仅描述已执行步骤的 wall-clock 毫秒字段；保持 existing completed/rejection/artifact 语义与固定 L1/false 不变。`evaluate` 以固定 package 参数运行安全测试，记录执行状态而不把命令文本当用户输入执行。
3. `sentinel evaluate` 接本地 manifest、历史测量、输出路径和可选安全测试开关；T003 用已有 CandidateSpec/SQL 启本机 webhook 并签名投递，复算四个 artifact SHA-256，将步骤时序、LLM 尝试数 0 和 smoke 事实写入报告及 README §11。
4. 任务顺序：纯评测/时序与测试 → CLI → 真实静态、安全、webhook smoke 与文档 → 全量验证、私有最终复盘、提交/fast-forward master。
5. 风险与降级：历史 JSON 或影子库不可用时输出明确未执行/不可用字段；不回填、更改或选择性重跑实验。章程：暂未建立。

真实实验记录（T003）：2026-08-28 用七例 manifest 运行 `evaluate`，静态检出 7/7；固定五组安全测试均通过。
以已有 SQL/Spec 在 `127.0.0.1:18082` 投递一次性 HMAC 模拟 PR，HTTP 202、四步完成、五项 artifacts；前四份文件
SHA-256 复算匹配，summary 走 canonical 范围。最终报告记录 1M 单 Case Better、candidate 噪声 13.02%、L1/false、
各步骤时序、LLM 尝试 0、Git `37a4a27…`、工作树 dirty、样例版本和 manifest SHA。服务随后停止，未操作 Docker。
