---
level: L2
feature: 005-candidate-spec-compiler
created: 2026-08-27
---

# CandidateSpec 受限索引候选编译器

## 原始需求

后续 Agent 需要给出索引候选，但 LLM 不能直接生成或执行 DDL 文本。
必须先将其输出限制为结构化 CandidateSpec，再由 Go 在 candidate 影子库上下文中校验并编译。

## 目标

- 定义只支持普通二级索引的 `CandidateSpec`：table、index_name、1–4 个列及每列 ASC/DESC。
- 校验表名/索引名/列名均为单段 MySQL 标识符；禁止空值、关键字拼接、反引号、点号、空白、注释和重复列。
- 校验 index_name 必须以 `idx_cand_` 开头；不支持 UNIQUE、PRIMARY、DROP、ALTER、函数列、表达式、algorithm/lock 选项。
- 编译器只生成一条确定性的 `CREATE INDEX ... ON ... (...)`，并使用反引号安全引用已校验的标识符。
- CLI 支持从 CandidateSpec JSON 文件校验/预览 DDL；只输出 DDL，绝不连接数据库或执行它。

## 非目标

- 不接 LLM/Eino、不执行 DDL、不读取真实 schema、不处理索引是否已存在。
- 不支持 SQL 改写、写压、覆盖索引、前缀索引、函数索引、全文索引、空间索引或 PgSQL。
- 不把 JSON 中出现的任意 `sql`/`ddl` 字段当作可信输入。

## 默认假设

- MySQL 8.4 支持本项目使用的单列/多列普通索引与 DESC；列方向默认 ASC。
- JSON 使用严格解码：未知字段直接拒绝，防止调用方以为额外字段会生效。
- 这是“源头约束”，不是授权执行；真正的 candidate DB DDL 仍需后续影子库门禁。

## 验收条件

1. 合法 3 列 CandidateSpec 编译为稳定、可预测的 DDL；相同输入必得相同输出。
2. 任何注入样式标识符、重复列、列数越界、错误 index 前缀或未知 JSON 字段均失败，且无 DDL 输出。
3. JSON 中即使包含 `sql`/`ddl` 字段也因未知字段失败，不能绕过 CandidateSpec。
4. `candidate-preview --in spec.json` 仅打印校验后的 DDL，不连接 MySQL；坏文件非零退出。
5. 全量 Go 检查、静态单元测试和 Adaptive Spec 校验通过。

## 任务将在 tasks.md 中拆分

计划会先实现纯校验/编译内核与注入边界测试，再接 CLI 文件读取与 README 示例。
