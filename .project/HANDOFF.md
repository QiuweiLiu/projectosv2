# Handoff
Status: p6 + 工作台 / 聊天托管 / 多标签

## Goal
在 Go Kernel 上提供长期、多目标、可恢复的 AI 科研/工程工作台；Kernel 是 Task/Run/
Evidence/Review 唯一权威，模型只提案，人在关键处确认。

## Done
- **P6.0–P6.3**：Manifest 版本锁、Evidence 预算/等级、故障注入、分页、Task A 对照。
- **工作台**：蓝白三栏 + 四 Tab + 块级 Markdown/柔和动效；窄窗 header 换行。
- **多标签**：独立 serve/端口/WKWebView；iCloud 下载提示；关闭只停自身；原生对话框
- **模型**：6 角色模型 + 思考强度（`provider/model#variant`）；`OPENCODE_BIN`；自动发现 CLI。
- **对话控制**：思考展开/折叠；生成中三处停止；Session 注入 ProjectOS 身份；
  托管前先停生成、清审批；审批 404 内联提示；启动清理遗留 running。
- **聊天托管**：建议/短句 → 内联草稿（目标+token 预算）→ 目标+计划 →
  底部流程 dock；未执行可改目标，已执行可 replan。
- **撤回**：运行中先停止再 fork Session 到目标消息前，删除其后消息/事件并恢复输入；
  消息不在远端（无 remote_id/无 session/fork 404）时只回退本地并明确提示。
- **驾驶舱**：派生执行中/已卡住/待执行；卡住时隐藏开始，顶部“需要你处理”卡片；原始 CLI
  错误不再显示；`goal replan` 真正替换未完成任务。
- **聊天审批/预算**：只读自动允许；输入区“帮我审批”开关（低风险模型自动、高风险弹卡）；
  托管确认异步；预算只填 token；建议每对话最多一次。

## Verified
- 24 包 `go test -count=1`、`vet` 全绿；`desktop --check`、`node --check` 通过。
- 聊天托管：scratch 真实草稿 25s → 预算持久化 → confirm 54s → planned 计划卡；调整目标后
  plan v2 与侧栏同步；用户目标 dock 显示 T3b 命令审批与“批准并开始执行”。
- 托管/审批：重启后旧会话已中断、0 审批 0 建议；404 拒绝无弹窗；短句开始走控制路径。
- 撤回：无 remote_id/无 session 的会话真实撤回成功（removed=1，remoteForked=false）；
  fork 404 先按文本+时间找回真实 server id；仍失败才 409 + localOnly 二次确认。
- 目标关联：撤回不删目标；cancelled/failed/completed 可“从对话移除”，撤回后已取消/失败
  目标自动断开；取消目标不再进入聊天证据，已验收产物仍保留。
- 审批/预算：只读自动通过；帮我审批开启后低风险自动、高风险弹卡并附原因；审批 dock 堆叠；
  确认异步返回；计划后台生成可重试；预算只填 token；建议每对话最多一次。
- 重规划：真实目标 v1 7 blocked → v2 4 pending；fake 测试覆盖替换/无操作；同产物复用审查结论。
- 驾驶舱：7 任务目标显示已卡住、6 条中文原因、恢复/重规划；run/pause 隐藏。
- 模型/身份/排版/多标签/撤回/问题表单按既有场景复验。

## Open
- 思考过程不落库；分类器每轮多一次轻量调用，暂无开关。
- `opencode models` 可能因插件激活失败；已有回退。
- 旧会话在 OpenCode server 重启后依赖 404 重建 Session，可能丢上下文。
- 撤回只回滚对话；缺少远端消息 ID 时会拒绝。
- 启动清理假定回合属于当前 Kernel 进程；远端仍在跑时由 Sync 补回。
- iCloud 工作区仍会 dataless；建议本地磁盘。
- P6 Task B/C、磁盘满/只读/WAL/Server 退出注入未做；独立 reviewer 鉴权过期。

## Active
工作副本 `~/dev/projectosv2`，HEAD `b11373d` 已 push；本轮未提交：`desktop/main.m`、
`chat.html`、`ui.html`、`goal*`/`control`/`serve`/`chat_api.go`、
`internal/{chat,modelcfg,opencodeserver,replan,engine,report,conversation,store}`、`docs/*`、控制面。

## Next
1. 用户确认后提交并推送本轮改动。
2. 聊天托管：确认后自动收起历史工具卡；审批历史留痕。
3. 补 Task B/C、剩余故障注入。
