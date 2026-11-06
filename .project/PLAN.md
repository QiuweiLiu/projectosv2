# Plan

## Now
P3.0-P3.2 已交付：验收缓存正确性、消息 ID 同步、SSE resync、Goal Handoff、
Project Context 注入；P3.3-P3.5 部分完成。

## Steps
1. 继续用 Goal Handoff 跑真实目标，优先修计划 validator 与 worker 产物不匹配。
2. 补 P3.3 智能干预与 P3.5 故障注入/指标。
3. 把 Cockpit 时间线切到 `/api/events/stream`；给聊天消息加保留策略。
4. 只有在真实使用暴露问题后，才扩展 UI/并发/多设备。

## Acceptance
- 聊天不产生 Objective；转托管必须人工确认计划。
- 关闭/重开桌面后历史、Session、Objective/Task 状态一致。
- 工具调用默认 ask，批准只对本次请求生效。
- `runNow` 不绕过确认；Objective acceptance 阻断时不交付。
- 现有 CLI、Workspace、222 测试保持兼容。

## Not now
Windows/Linux、逐工具托管任务审批、Project 级复杂调度、多设备同步、日志轮转。
