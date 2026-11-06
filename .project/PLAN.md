# Plan

## Now
M1/M2 已交付：持久化聊天、OpenCode v2 Session/SSE、工具审批、`/chat` 工作台；
M0 正确性门禁已修；M3 只做了 Project 归属映射。

## Steps
1. 在 `/chat` 关联真实 Objective，跑完“聊天 → 转托管 → 计划 → 执行 → 交付”闭环。
2. 决定是否补 pause/resume 与 Project 级 `.project` 共享上下文。
3. 把 Cockpit 时间线切到 `/api/events/stream`；给聊天消息加保留策略。
4. 只有在真实使用暴露问题后，才扩展 UI/并发/多设备。

## Acceptance
- 聊天不产生 Objective；转托管必须人工确认计划。
- 关闭/重开桌面后历史、Session、Objective/Task 状态一致。
- 工具调用默认 ask，批准只对本次请求生效。
- `runNow` 不绕过确认；Objective acceptance 阻断时不交付。
- 现有 CLI、Workspace、188 测试保持兼容。

## Not now
Windows/Linux、逐工具托管任务审批、Project 级复杂调度、多设备同步、日志轮转。
