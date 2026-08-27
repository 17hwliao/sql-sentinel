# CandidateSpec 编译器任务

## 任务

- [x] **T001** 实现 CandidateSpec、标识符/列数/方向/前缀/重复列校验及确定性 DDL 编译；覆盖合法和危险输入。验证：`go test -count=1 ./internal/candidate/...`
- [x] **T002** 实现严格 JSON 解码，拒绝未知字段、`sql`/`ddl` 和尾随对象；补对应测试。验证：`go test -count=1 ./internal/candidate/...`
- [x] **T003** 新增 `candidate-preview --in PATH`，只输出 DDL；更新总命令帮助。验证：合法/坏 JSON CLI 命令。
- [x] **T004** README 增加 JSON 样例与“预览不执行”边界；运行全量 Go 检查与 Adaptive Spec 校验。验证：所有命令通过。

## 测试克制说明

测试针对唯一高风险：结构化约束被绕过后，LLM 文本进入 DDL。不会为普通结构体字段或 flag 框架重复写低价值测试。
