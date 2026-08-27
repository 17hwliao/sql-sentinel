# CandidateSpec 编译器计划

## 1. 实现决策

- 新建 `internal/candidate`，输入只含结构化字段，不设计 `RawSQL` 字段。
- 标识符采用 ASCII `[A-Za-z_][A-Za-z0-9_]{0,63}`；这是刻意保守子集，先保证可审计安全，不追求兼容所有 MySQL 合法命名。
- 方向只接受 `ASC`/`DESC`，省略时归一化为 `ASC`；DDL 按输入列序输出。
- JSON decoder 使用 `DisallowUnknownFields` 并检查没有尾随 JSON 值。
- 预览 CLI 只读文件并写 stdout；不引入 `database/sql`，确保它物理上无法执行 DDL。

## 2. 数据流

```text
CandidateSpec JSON
  -> strict decode
  -> Validate(spec)
  -> CompileCreateIndex(spec)
  -> stdout DDL (preview only)
```

后续 Agent 只能负责构造这个 JSON；后续影子库执行器只能接收已验证的 CandidateSpec/DDL，不能接收自由 SQL 字符串。

## 3. 任务顺序

1. 定义 CandidateSpec/Column、校验和编译器；测试合法稳定 DDL 与所有危险输入。
2. 实现严格 JSON 读取；测试未知字段、`sql`/`ddl` 绕过和尾随对象。
3. 接 `candidate-preview` CLI，测试 help、合法文件、坏文件非零退出。
4. README 写清“预览不执行”的边界和样例；全量 Go/规格校验。

## 4. 风险与降级

| 风险 | 处理 |
| --- | --- |
| 保守标识符拒绝现有复杂命名 | 明确报错；未来经过单独设计后再放宽 |
| Agent 试图携带原始 DDL | strict decoder 拒绝未知字段 |
| 预览被误解为执行 | CLI 不导入数据库包，README 明示只输出 |

## 5. Spike

不需要。纯 Go 校验/编译逻辑可由单元测试完全验证；实际影子库执行属于后续独立阶段。
