# Handoff
Status: p5（可信产物/Progress/有界恢复/Evidence Context/Cockpit）

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
- **P5**：Metrics NULL/墙钟/分角色 token；共享产物仅 completed+delivery+哈希一致才发布，
  Manifest 记录来源/版本/哈希，staging+flock 原子刷新；结构化 Progress（needs_planning/
  confirmation/approval/ready/running/blocked/recoverable/manual/completed/cancelled/unknown）；
  Recovery 有界重试/重排队/审批/unknown 不重试；Evidence Context 查询相关且区分可信级别；
  Cockpit 恢复面板与共享证据；`/api/events/stream` 游标恢复。
- 聊天转托管：`/api/goals` + `/api/objectives/{id}/plan`，不自动执行。
- 任务侧保留 P2：Workspace、多目标、审批、diff、`--session`、work prune、token 实测。

## Verified
- 272 项 Go 测试 + `-race` + `vet` 全绿。
- 真实 OpenCode：3 轮聊天、SSE delta、工具写文件 + permission once 批准；
  serve 重启后历史与 Session 恢复；`/api/objectives` 始终为空。
- 真实托管目标：planner → worker → reviewer → acceptance → **objective acceptance**
  → 交付 completed；objective review 记录实测 token。
- `runNow=true` 实测只到 `planned`，tasks 表 0 条。
- `/chat` 浏览器发送/流式/项目过滤/工具卡片已验证；桌面 `--check` 通过。
- 基线 HEAD：`e5cfec4`；P3.0–P3.2 提交 `7fa3b9a`；P4 提交 `dfa913c`；P4 修复 `345187c`；P5 提交 `af17185`；P5 审查修复 `7d84920`；审查 follow-up `2c14c16`，均已 push 到 `origin/main`。
- P5 真实：A 自动恢复（T1 失败→重试→T2→交付）；B/C 同 Project 三个 Objective，B/C 真实
  worker 读 Manifest 产物并分别写 `metric_b=42`、summary；C kill -9 后 unknown 拒绝重试，
  人工 `recover --approve --task` 后完成；Metrics 实测 B 4457/127、C 4183/208 tokens。

## Open
- Recovery 诊断纯确定性，无模型辅助；unknown 需人工 `--task` 批准后恢复。
- P3.5 未注入：磁盘满、只读挂载、运行中 SQLite 失效、OpenCode Server 退出重连。
- 托管任务仍是执行前审批，不是逐工具拦截；command 不是沙箱。
- 聊天消息历史无级联清理；事件每对话保留 500 条。
- Windows/Linux、桌面签名/公证、日志轮转未做。

## Active
工作副本：`~/dev/projectosv2`（Git HEAD `2c14c16`，clean）。本轮测试服务已停止；
旧演示 8787/8789/8790 不是本轮启动。

## Next
1. 用真实模型复核 Recovery 的失败分类与 Evidence 选择质量，再决定是否加一次有界模型诊断。
2. P3.5：磁盘满/只读挂载/运行中 SQLite 失效/OpenCode Server 退出重连。
3. 桌面端慢消费者与长会话压力回放；日志/消息保留策略。
