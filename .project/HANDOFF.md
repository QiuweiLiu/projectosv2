# Handoff
Status: p6（可靠性/故障注入/长会话/首个对照样本）

## Goal
在 Go Kernel 上提供长期、多目标、可恢复的 AI 科研/工程工作台；Kernel 是 Task/Run/
Evidence/Review 唯一权威，模型只提案，人在关键处确认。

## Done
- **P6.0**：Manifest 读取在共享锁内完成；发布 crash 窗口恢复并清理 staging/old；
  Evidence 在结构化 Bundle 层限预算、UTF-8 安全、无未闭合块；可信等级拆为
  published/objective_accepted/task_accepted/user_confirmed/in_progress/unverified；
  文档与实现同步。
- **P6.1**：DB 关闭/写失败不产生虚假 success；并发 requeue 单赢家；发布 read-only/
  manifest 失败保留旧树；seed 哈希、符号链接、路径逃逸拒绝；CLI 非零退出为 failed；
  Server 启动失败不残留；Objective judge 会话接收交付文件。
- **P6.2**：1200 条消息分页无丢失/重复；事件 500/对话；慢消费者 resync 测试。
- **P6.3**：Task A 真实对照完成；发现并修复两个真实缺陷（worker 缺依赖文件、
  Objective judge 空目录），最终 ProjectOS hidden PASS。
- **桌面蓝白工作台（未提交）**：`chat.html` 新增 `modern-blue-workspace` 覆盖层实现蓝白
  Design System（蓝白 Token、系统字体、三栏 260px+流式+312px 可折叠 Inspector、
  概览|任务|审批|证据 Tab、纯文字导航）；修复 grid 高度失控（Composer/页脚首屏可见）、
  模型名重复、工具状态对齐、窄窗换行；`ui.html` 同步换肤；`docs/desktop.md` 改蓝白。
  无 dead 导航、无新增 API、无虚构按钮；Token 为 password 字段+掩码。
- P5 仍有效：自动恢复、共享 Manifest、Evidence Context、Cockpit 恢复面板。

## Verified
- 24 包测试（`-count=1`）+ `-race` + `vet` 全绿；桌面 `--check`、`node --check`、
  `git diff --check` 通过。
- Headless 浏览器验证：`/tmp/ui-blue-final.png`（1440 认证，Composer/页脚/审批卡片正常）、
  `/tmp/ui-blue-chat2.png`（空状态）、窄窗右栏隐藏。Cockpit `/` headless 挂起，以 DOM+构建代替。
- 基线 HEAD：`c218d0e`；P6 `ecf8707`；工作区 `fda956e`；UI `16808a4` 已 push。
- P6.0/P6.1：`TestSeedReadsManifestAndFilesUnderOneVersionLock`、`TestRenderBounded*`、
  `TestPublishRecoversCrashWindow...`、`TestClosedStore*`、`TestConcurrentRequeue...`、
  `TestObjectiveJudgeSeedsDeliveredFiles`、`TestExecutorExitIsFailureNotSuccess`。
- P6.2：1200 消息 183 ms / SQLite 282,624 bytes；慢消费者与事件页测试通过。
- P6.3 Task A（n=1）：baseline 140.1 s / 6,583+385 tokens；ProjectOS 629.4 s /
  45,957+1,024 tokens；两者 hidden PASS；0 人工介入。ProjectOS 成本显著更高，
  详见 `reports/p6-benchmark-2026-11-07.md`。
- 基线 HEAD：`c218d0e`；P6 提交 `ecf8707`；桌面工作区选择 `fda956e`；UI 重构 `16808a4`，已 push 到 `origin/main`。

## Open
- 蓝白升级尚未完整验收：A–H 状态、真实 API 交互、项目切换、Inspector 内容及六种截图未全面验证；不能视为完整交付。独立 reviewer 鉴权过期。
- Benchmark 只跑 Task A 一个样本；B/C 未运行，不能断言整体收益。
- 未注入：磁盘满、只读挂载、SQLite WAL 损坏、OpenCode Server 退出重连。
- Recovery 模型辅助诊断仅 fake backend 单测；未知执行状态仍需人工 `--task`。
- 命令验收不是沙箱；托管任务不是逐工具审批；聊天消息保留 1000、事件 500。

## Active
工作副本：`~/dev/projectosv2`（蓝白 UI 三文件未提交：`cmd/kernel/chat.html`、`cmd/kernel/ui.html`、
`docs/desktop.md`；另有本轮更新的 `.project/HANDOFF.md` 未提交）。
测试服务已停止；无 daemon 进程。独立 `reviewer` 子代理因鉴权过期未能执行（已向用户报告，
待用户选择替代路线）；审计为 auditor 只读核对（CONDITIONAL→4 项已修）。

## Next
1. 跑 Task B/C 真实对照，扩大样本；先记录基准与预算。
2. 根据 Task A 负结果评估低风险任务的 review/acceptance 条件化，不牺牲验收可靠性。
3. 补磁盘满/只读挂载/WAL/Server 退出重连故障注入。
