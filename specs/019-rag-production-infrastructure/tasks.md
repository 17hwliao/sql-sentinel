# 任务

- [x] T001 定义生产配置拒绝模型，确保没有隐式 fake/memory 回退。
- [x] T002 实现 Qdrant persistence adapter、health、collection 与 scoped lifecycle 操作。
- [x] T003 建立 SQL Sentinel + AgentMesh 可追溯 corpus manifest 与受控写入校验。
- [x] T004 增加黄金问答集，并将 Recall@K/Precision@K 改为多正例真实指标。
- [x] T005 新增显式 AgentMesh grounded-answer client、citation 回绑与 production query/verify 入口。
- [x] T006 新增独立 Qdrant Compose 与运维说明。
- [x] T007 完成 offline HTTP fake、manifest、指标、AgentMesh 双端合同和既有安全边界测试。
- [ ] T008 真实 Qdrant + 显式 embedding + AgentMesh answer smoke（依赖服务存在；本任务不伪造）。
