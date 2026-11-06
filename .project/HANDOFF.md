# Handoff
Status: p4_min（机器检查/权限补拉/共享产物/Progress/Metrics）

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
- **P3.0**：Objective acceptance 指纹绑定完整 plan/验收条件/全部产物内容哈希；Conversation
  remote-id 映射与幂等同步；SSE 分页回放、慢消费者 resync。
- **P3.1**：多轮对话 Goal Draft（来源/context version）→ 编辑确认 → Objective → Plan；
  notes 目标真实 handoff→执行→验收→交付完成。
- **P3.2**：Project Context Builder 注入聊天首条消息与 Planner `--context`；项目快照回写。
- **P4**：机器检查进入 worker prompt；计划语义预检拒绝未声明实现符号；权限 pending 重启
  补拉；provider 瞬态有界退避；跨 Objective `project-artifacts/` 共享；`goal progress`/
  `/progress` 确定性下一动作；`/metrics` 任务/token/耗时；消息同步 cursor 分页 2000 条；
  Handoff 元指令确定性过滤。
- 聊天转托管：`/api/goals` + `/api/objectives/{id}/plan`，不自动执行。
- 任务侧保留 P2：Workspace、多目标、审批、diff、`--session`、work prune、token 实测。

## Verified
- 222 项 Go 测试 + `-race` + `vet` 全绿。
- 真实 OpenCode：3 轮聊天、SSE delta、工具写文件 + permission once 批准；
  serve 重启后历史与 Session 恢复；`/api/objectives` 始终为空。
- 真实托管目标：planner → worker → reviewer → acceptance → **objective acceptance**
  → 交付 completed；objective review 记录实测 token。
- `runNow=true` 实测只到 `planned`，tasks 表 0 条。
- `/chat` 浏览器发送/流式/项目过滤/工具卡片已验证；桌面 `--check` 通过。
- 基线 HEAD：`e5cfec4`；P3.0–P3.2 提交 `7fa3b9a`；P4 提交 `dfa913c`；P4 修复提交见下一次 push。
- P4 真实验证：计划语义预检（坏计划拒绝/好计划通过）、worker 机器检查、权限重启补拉、
  跨 Objective 共享产物、真实 Handoff 过滤、`order=asc` cursor 分页。

## Open
- Goal Draft 合同/来源已保存；解析层已过滤常见聊天元指令，模型新措辞仍需观察。
- P3.3 智能干预、P3.5 故障注入/指标仍未完整。
- Cockpit 的 Kernel 事件仍是轮询；聊天已 SSE，`/api/events/stream` 已提供但 UI 未切换。
- 聊天消息历史无级联清理；事件每对话保留 500 条。
- 托管任务仍是执行前审批，不是逐工具拦截；command 不是沙箱。
- 未注入：取消/完成进程级交错、磁盘满/只读挂载、运行中 SQLite 失效。
- Windows/Linux、桌面签名/公证、日志轮转未做。

## Active
工作副本：`~/dev/projectosv2`（P4 修复提交后 clean）。
测试服务可能仍在 8801；用 `kernel serve --dir <D> --stop` 关闭。
旧演示 8787/8789/8790 不是本轮启动。

## Next
1. 在 `/chat` 关联真实 Objective，跑“聊天 → 转托管 → 计划 → 执行 → 交付”全流程。
2. P3.5：取消/完成交错、磁盘满/只读挂载、运行中 SQLite 失效注入。
3. 把 Cockpit 时间线切到 `/api/events/stream`，再做日志/消息保留策略。
