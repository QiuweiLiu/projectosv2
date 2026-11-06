# Handoff
Status: m1_m2_chat（持久化聊天 + 实时 SSE + 工具审批）

## Goal
在现有 Go Kernel 上增加桌面 AI 工作台：普通聊天与长期托管任务分离；
聊天不创建 Objective，用户明确转换后才进入 Goal → Plan → Confirm → Run。

## Done
- **M0**：`runNow` 只生成计划，不再自动 confirm/run；Objective 级 acceptance 成为
  completed 前的独立模型门禁；删除旧代码生成的 query-string SQLite 残留。
- **M1**：Conversation SQLite（`conversations.db`）+ 消息/事件；OpenCode 2.2.5
  私有 v2 Server；Session 创建/续聊/prompt/history/interrupt；重启恢复。
- **M2**：`/chat` 三栏工作台（项目/对话、聊天、Objective/Task）；SSE `text.delta`
  实时、最终文本落库；工具 `permission.asked` → 允许一次/总是/拒绝；
  `after`/`Last-Event-ID` 重放；桌面壳默认打开 `/chat`。
- **M3 最小**：`project-catalog.json` + 目标目录 `project.json`；聊天/目标按项目过滤。
- 聊天转托管：`/api/goals` + `/api/objectives/{id}/plan`，不自动执行。
- 任务侧保留 P2：Workspace、多目标、审批、diff、`--session`、work prune、token 实测。

## Verified
- 188 项 Go 测试 + `-race` + `vet` 全绿。
- 真实 OpenCode：3 轮聊天、SSE delta、工具写文件 + permission once 批准；
  serve 重启后历史与 Session 恢复；`/api/objectives` 始终为空。
- 真实托管目标：planner → worker → reviewer → acceptance → **objective acceptance**
  → 交付 completed；objective review 记录实测 token。
- `runNow=true` 实测只到 `planned`，tasks 表 0 条。
- `/chat` 浏览器发送/流式/项目过滤/工具卡片已验证；桌面 `--check` 通过。
- 基线 HEAD：`c89a06e`；本轮改动尚未 commit/push。

## Open
- 托管任务的暂停调度（pause/resume）尚未实现；当前只有 cancel。
- Project 级 `.project` 快照、跨 Objective 上下文共享未完成；只有归属映射。
- Cockpit 的 Kernel 事件仍是轮询；聊天已 SSE，`/api/events/stream` 已提供但 UI 未切换。
- 聊天消息历史无级联清理；事件每对话保留 500 条。
- 托管任务仍是执行前审批，不是逐工具拦截；command 不是沙箱。
- 未注入：取消/完成进程级交错、磁盘满/只读挂载、运行中 SQLite 失效。
- Windows/Linux、桌面签名/公证、日志轮转未做。

## Active
工作副本：`~/dev/projectosv2`（Git HEAD `c89a06e` + 未提交改动）。
测试服务可能仍在 8801；用 `kernel serve --dir <D> --stop` 关闭。
旧演示 8787/8789/8790 不是本轮启动。

## Next
1. 在 `/chat` 关联真实 Objective，跑“聊天 → 转托管 → 计划 → 执行 → 交付”全流程。
2. 决定是否实现 pause/resume 与 Project 级 `.project`。
3. 把 Cockpit 时间线切到 `/api/events/stream`，再做日志/消息保留策略。
