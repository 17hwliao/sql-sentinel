---
level: L2
feature: 006-readonly-sql-admission
created: 2026-08-27
---

# 只读 SQL 输入准入与静态风险信号

## 目标

- 新增 `sql-admit --in query.sql`：仅接受单条只读 `SELECT` 或 `WITH ... SELECT`，只输出机器可读准入结果，不连接数据库、不执行 SQL。
- 词法扫描忽略字符串字面量、反引号标识符和注释中的分号/关键字；真实语句边界外的第二条语句必须拒绝。
- 拒绝 DDL/DML、`SELECT ... INTO OUTFILE/DUMPFILE`、锁定读 `FOR UPDATE` / `LOCK IN SHARE MODE` 及空输入。
- 输出有限、透明的静态信号：`select_star`、`leading_wildcard_like`、`function_on_probable_column`；信号只表示待调查，不声称性能结论。

## 非目标

- 不执行 EXPLAIN、不访问 MySQL、不尝试完整 MySQL AST 解析、不生成索引建议。
- 不将文本规则输出包装成 L1/L2 性能证据；复杂语义留给影子库取证。

## 验收

1. 单条 SELECT、带字符串或注释内分号的 SELECT 被接受；第二条语句被拒绝。
2. INSERT/UPDATE/DELETE/CREATE/ALTER/DROP、OUTFILE、FOR UPDATE、LOCK IN SHARE MODE 被拒绝。
3. 静态信号命中只来自真实 SQL token，不从注释或字符串误触发。
4. `sql-admit` 仅输出 JSON；合法和非法文件均不连接 Docker/MySQL。

## 默认假设

- 这是严格子集：不支持存储过程、分隔符命令与多语句协议。
- 规则的目的只是产出待验证假设，后续必须交给 schema/EXPLAIN/影子库验证。
