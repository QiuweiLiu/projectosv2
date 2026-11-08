# Handoff
Status: p6 + 蓝白工作台真机验收

## Goal
在 Go Kernel 上提供长期、多目标、可恢复的 AI 科研/工程工作台；Kernel 是 Task/Run/
Evidence/Review 唯一权威，模型只提案，人在关键处确认。

## Done
- **P6.0–P6.3**：Manifest 版本锁、Evidence 预算/等级、故障注入、分页、Task A 对照
  （见 `reports/p6-benchmark-2026-11-07.md`）。
- **蓝白工作台**：`chat.html` 蓝白 Token/系统字体/三栏（260+流式+312 可折叠）/四 Tab；
  `ui.html` 同步换肤；`docs/desktop.md` 改蓝白。
- **本轮真机验收修复**：
  - 流式去重键导致第二轮起不再逐字显示（`addMessage` 空文本占位）；
  - 会话状态显示英文/绿色（`interrupted`）→ `convLabel/convBadge`；
  - 回复 Markdown 原样输出 → 轻量转义后渲染粗体/代码/列表/链接；
  - 空工作区无欢迎态、无会话仍可发送 → `renderEmptyState` + 发送禁用；
  - 项目切换后仍显示他项目会话 → 关闭会话、保留草稿并提示；
  - 断线只改页脚 → 顶部“连接中断”、禁用发送、重连后自动补偿；
  - `/#goal=<id>` 深链被首个目标覆盖（`loadObjectives` 抢先改写 hash）；
  - Inspector 证据 Tab 原为静态说明 → 真实 sharedArtifacts/delivery/Findings；
  - 已处理审批刷新后仍显示“待处理” → `permission_replied` 复位；
  - 任务标题常显；IME 回车误发送；会话行不可键盘访问。

## Verified
- 24 包 `go test -count=1`、`-race`、`vet` 全绿；`desktop --check`、`node --check`、
  `git diff --check` 通过。
- `OPENCODE_BIN` 实测：PATH 仍为 v1.18.15，仅设该变量即可规划（12,922 in / 185 out）。
- 默认模型实测：空工作区无 models.json，日志 `planner model: opencode-go/deepseek-v4.1-flash`，
  生成 1 个任务。
- 审批闭环：新会话 → write 触发 `permission` → 允许一次 → 卡片消失、工具完成、
  `approve-check.txt` 落盘、回 idle（`state-C2-*.png`）。
- 流式：第 2 轮起出现 `.bubble.streaming`、状态“回复中”，结束回“空闲”并渲染 Markdown。
- Handoff：草稿可编辑并确认 → 建 Objective + 计划；计划失败错误留在弹窗，不误报成功。
- Cockpit：计划→确认→执行前审批弹窗（未真正执行）；失败目标显示 Recovery 与预算；
  完成目标显示 1/1 与共享证据 Manifest。
- 断线/重连：停 serve → “连接中断”+发送禁用；重启 → 自动重连并 resync。
- 项目切换：会话列表按项目过滤、草稿保留、上下文更新。
- 截图：`/tmp/state-A-empty2.png`、`final-B-conversation2.png`、`state-C2-pending|approved.png`、
  `state-D-draft.png`、`state-E-rungate.png`、`state-F|G|H-*.png`。

## Open
- 未点击：拒绝/总是允许、继续改进、暂停/取消、Recovery（避免再执行与花费）。
- 环境：PATH 上 `opencode` 仍为 v1.18.15；已修 `goal.go` 让 planner/worker/reviewer/
  acceptance 全部使用 `OPENCODE_BIN`（新增回归测试）。仅设置该变量即可规划，无需改 PATH。
- 默认模型已统一为 `opencode-go/deepseek-v4.1-flash`（全部角色，单点 `defaultModel`）。
- 独立 `reviewer` 子代理鉴权过期，未执行独立评审；auditor 只读核对已完成。
- 蓝白升级尚未在 WKWebView 真窗口截图；Cockpit headless 截图曾挂起，改用 CDP。
- P6 Task B/C 未跑；磁盘满/只读/WAL/Server 退出注入未做。

## Active
工作副本 `~/dev/projectosv2`，未提交 `chat.html`/`ui.html`/`goal.go`/新增测试/控制面。
服务已停止；`/private/tmp/projectos-chat-012346` 留有测试会话与已取消目标。

## Next
1. 用户确认后提交并推送本轮 UI 验收、`OPENCODE_BIN` 与默认模型修改。
2. 补点击类验证（拒绝/总是允许、继续改进、暂停/取消、Recovery）。
3. 跑 Task B/C 与剩余故障注入。
