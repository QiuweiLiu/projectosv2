# Plan

## Now
P2 可用性已交付：多目标 Workspace、执行前审批、文本 diff、会话续跑、work 清理、token 实测、
macOS 原生壳。接下来验证真实使用，不再扩功能面。

## Steps
1. 用 Workspace 并行跑 2–3 个真实目标（含一次失败/重试），记录审批摩擦、diff 可读性、
   acceptance 误伤与汇报质量。
2. 优先修计划质量（产物引用、任务拆分），而不是继续加界面。
3. 视需要做 SSE；不做 Windows/Linux 与逐工具审批。
4. 决定 AO 去留：装 tmux 试 v1，或删除 AO 路径与依赖。

## Acceptance
- 真实目标全部走默认执行器，不需要回退 AO。
- 审批弹窗不成为每次执行的纯摩擦；高风险/命令能正确拦住。
- diff 能解释“这次改了什么”，而不是只显示“相同/不同”。
- `<state>/work/` 不会无限增长。
- 未修项写进 HANDOFF 的 Open，不用“以后再说”。

## Not now
多机、Windows/Linux、逐工具调用审批、二进制行级 diff、日志轮转。
