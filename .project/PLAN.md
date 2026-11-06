# Plan

## Now
P5 已交付：Metrics 正确性、可信共享产物、结构化 Progress、有界自主恢复、
按需 Evidence Context、Cockpit 恢复/证据展示；P3.5 极端故障注入仍部分。

## Steps
1. 用真实目标复核 Recovery/Evidence 的分类与相关性。
2. 补 P3.5 磁盘满、只读挂载、运行中 SQLite 失效、OpenCode Server 退出重连。
3. 桌面慢消费者/长会话压力回放；消息保留与日志轮转。
4. 只有在真实使用暴露问题后，才扩展并发/多设备。

## Acceptance
- 聊天不产生 Objective；转托管必须人工确认计划。
- 关闭/重开桌面后历史、Session、Objective/Task 状态一致。
- 工具调用默认 ask，批准只对本次请求生效。
- `runNow` 不绕过确认；Objective acceptance 阻断时不交付。
- 现有 CLI、Workspace、272 测试保持兼容。

## Not now
Windows/Linux、逐工具托管任务审批、Project 级复杂调度、多设备同步、日志轮转。
