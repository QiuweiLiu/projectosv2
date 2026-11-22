# Handoff
Status: UI/UX 极简重构，已提交

## Goal
在 Go Kernel 上提供长期、多目标、可恢复的 AI 科研/工程工作台；Kernel 是 Task/Run/
Evidence/Review 唯一权威，模型只提案，人在关键处确认。

## Done
- P6.0–P6.3、蓝白工作台、多标签、6 角色模型/思考强度、聊天托管、撤回/目标关联、
  驾驶舱、审批栏与 token-only 预算。
- P0/P1 安全修复：webfetch、远程读鉴权/CSP、symlink、verify、opencode 管道、AO 超时、
  objective 缓存、rewind/evidence/replan。
- R2：三层审批（工作区规则/前缀记忆/approval 角色）、时间戳/撤回事务、Subscribe 两阶段、
  delivery out/ 原子替换、ApplyReplan PlanUsage、边界修复。
- F-01..F-07：引号感知 tokenizer 与真实 argv 路径检查、前缀/危险 flag 收紧、
  delivery 权限/互斥、裁决 root/variant、桌面导航回环限制、evidence/rewind 边界。
- 安全复核：真实路径 symlink/`..`、`--`、测试运行器裸操作数、敏感名/`secrets.*`、
  根递归搜索与 `git show/diff/log` 内容门禁、旧 always 规则、chat variant、delivery
  回滚、rewind 重复消息歧义、evidence 微预算。
- 极简工作台：chat/ui 收拢为单一 CSS，分段导航、52px 顶栏、发送/停止合一、设置浮层、
  检查器折叠与角标、审批规则管理、驾驶舱多 Tab/动作菜单；远程 Blob/SSE 鉴权。
- 复核修复：聊天草稿/预算改走正确 handoff 接口；驾驶舱异步读取与 SSE 绑定 objective；
  聊天移动端侧栏抽屉；聊天 variant 运行时重读 `models.json`；handoff 确认先原子 claim；
  模型设置子进程串行化。
- 复核二轮：model 更新用指针区分清空/未提供；handoff 持久化 objective ID、`goal create --id`
  支持崩溃后续建与幂等返回；聊天选择增加 generation，预算绑定 conversation，切换丢弃旧响应。
- 最终复核 SUBMIT；SSE/表单/审批/自动审批均绑定会话。
- 审批历史：从 `lastEventSeq` 后订阅，只渲染 pending；已答问题卡移除。

## Verified
- `go test -race -count=1 ./...`、`go vet ./...` 全绿；`desktop/build.sh` 与
  `ProjectOS --check` 通过（8799）；独立复核最终 SUBMIT（0 P0/P1）。
- F 新增用例：裸 `.env`/`.ENV`/`id_rsa`、引号路径、`<` 重定向、根软链接、
  `rg "foo|bar"`、`grep "rm -rf"`、`go test -run Test_reboot`、危险 flag、规则拒绝、
  并发 Assemble、微预算 evidence summary；复核补 symlink/`..`、测试运行器裸操作数、
  `secrets.*`、`git diff/show/log` 内容门禁与旧规则。
- 既有场景：真实目标规划/执行/验收/交付、审批弹窗、diff、多标签、撤回、故障注入仍通过。
- 浏览器 mock 数据 1440/900/700：无横向滚动、控制台无错误；聊天设置/放行规则/移动端
  抽屉、驾驶舱概览/任务/交付/设置/更多菜单均截图检查。
- 新增 `TestLiveChatVariantReadsRuntimeSettings`、`TestGoalDraftClaimIsExclusive`、
  `TestUpdateModelsVariantDoesNotClearModel`、`TestGoalCreateUsesExplicitID`；全量
  `-race`/vet/node/desktop check 再通过。

## Open
- ctx 取消/进程回收、UTF-8 截断统一、P2 清理未做。
- P6 Task B/C、磁盘/只读/WAL/Server 故障注入未做；reviewer 鉴权过期。
- 聊天消息保留 1000、事件 500；Windows/Linux、日志轮转、桌面签名未做。

## Active
- 工作副本 `~/dev/projectosv2`，HEAD 见 `git log`（本轮已 push）。
- 本轮提交：`cmd/kernel/{chat,ui}.html`、`chat_api.go`、`status.go`、`serve.go`、
  `control.go`、`internal/chat/{policy,manager,handoff,approve}.go`、
  `internal/conversation/store.go`、`internal/modelcfg/modelcfg.go`、`docs/*`、控制面。
- `.project/DECISIONS.md` 已改为索引，正文在 `decisions/2026-11.md`。

## Next
1. 本轮已提交；重启桌面应用或刷新标签确认布局。
2. 重启桌面应用，真机确认 900/1440 布局与控制台。
3. 继续 ctx 取消/进程回收与剩余故障注入。
