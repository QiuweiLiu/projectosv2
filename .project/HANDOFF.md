# Handoff
Status: F 收口完成，已提交

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

## Verified
- `go test -race -count=1 ./...`、`go vet ./...` 全绿；`desktop/build.sh` 与
  `ProjectOS --check` 通过（8799）；独立复核最终 SUBMIT（0 P0/P1）。
- F 新增用例：裸 `.env`/`.ENV`/`id_rsa`、引号路径、`<` 重定向、根软链接、
  `rg "foo|bar"`、`grep "rm -rf"`、`go test -run Test_reboot`、危险 flag、规则拒绝、
  并发 Assemble、微预算 evidence summary；复核补 symlink/`..`、测试运行器裸操作数、
  `secrets.*`、`git diff/show/log` 内容门禁与旧规则。
- 既有场景：真实目标规划/执行/验收/交付、审批弹窗、diff、多标签、撤回、故障注入仍通过。

## Open
- ctx 取消/进程回收、UTF-8 截断统一、P2 清理未做。
- P6 Task B/C、磁盘/只读/WAL/Server 故障注入未做；reviewer 鉴权过期。
- 聊天消息保留 1000、事件 500；Windows/Linux、日志轮转、桌面签名未做。

## Active
- 工作副本 `~/dev/projectosv2`，HEAD 见 `git log`（本轮已 push）。
- 本轮提交：`desktop/main.m`、`docs/chat.md`、
  `internal/chat/{policy,approve,rewind,manager_test,approve_test}.go`、
  `internal/delivery/{report,assemble_test}.go`、`internal/evidence/*`、控制面。
- `.project/DECISIONS.md` 已改为索引，正文在 `decisions/2026-11.md`。

## Next
1. 用户确认后提交并推送 F 收口。
2. 启动/重启桌面应用以使用新的导航限制。
3. 继续 ctx 取消/进程回收与剩余故障注入。
