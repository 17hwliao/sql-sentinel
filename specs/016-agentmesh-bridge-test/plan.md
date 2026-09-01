# 016 联桥验证计划

## 实现路径

1. 在 `internal/agentmeshbridge` 定义窄 SSE client 和安全报告；严格限制 loopback URL、请求超时、Authorization header、SSE content type、`X-AgentMesh-Trace-ID` 与 `[DONE]`；报告记录脚本从 AgentMesh Git 工作树解析的精确 commit SHA。
2. 加入 `sentinel agentmesh-bridge-test`，环境缺失在拨号前返回稳定拒绝；真实响应 delta 只被协议读取器消费，不进入 JSON/日志。
3. 用 HTTP fake 覆盖成功和拒绝/中断边界；以 PowerShell 脚本临时在 `127.0.0.1:18185` 启动现有 AgentMesh/Ollama，运行一次跨仓库真实往返并清理子进程与临时 Key/日志。
4. 更新两边私有简历材料，只写该次传输实证及此前 017/021 的限定数据。

## 风险与边界

- SQL Sentinel 保持 L1 提案者边界：联桥只测传输，绝不将模型返回当 CandidateSpec 或执行输入。
- AgentMesh repo 路径、Ollama endpoint/model 和临时 API key 均仅走环境；脚本不输出 key、prompt、delta 或上游原文。
- 章程：暂未建立。

## 验证

运行相关包测试、`sentinel agentmesh-bridge-test` 受控拒绝与真实脚本；再执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`git diff --check` 和 Adaptive 校验。
