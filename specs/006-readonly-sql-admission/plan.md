# 计划

1. 实现状态机扫描器，识别普通、单/双引号、反引号、行/块注释，并找出语句外 token 与分号。
2. 基于 token 进行只读准入；拒绝列表优先于 SELECT 判断，所有失败返回稳定原因码。
3. 在同一 token 流产出三个静态风险信号；JSON 输出带 `accepted`、`reason_code`、`signals`，不带性能结论。
4. 接 CLI、测试真实文件，再写 README、全量验证和阶段复盘。

风险：文本规则不是 AST。处理：不解析嵌套语义、不生成 CandidateSpec，并在输出中标注 `evidence_level=L0`。
