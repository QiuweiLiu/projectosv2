# 持久化聊天（M1/M2）

## 边界

- **Conversation** 存在 `<workspace>/conversations.db`，与 Kernel 的任务库分离。
- **普通聊天不创建 Objective/Task**；只有用户点“转为托管目标”才调用 `/api/goals`。
- **Task 状态只能由 Kernel 修改**；聊天可以建议、解释、展示，但不能静默接受结果。
- **OpenCode Session** 是执行资源；Conversation 只是它的人类可读历史。

## 真实接口（OpenCode 2.2.5）

Kernel 启动一个私有的 `opencode serve --hostname 127.0.0.1 --port <free>`：

- 启动日志里的随机密码只保存在内存中，Basic auth 为 `opencode:<password>`；
- 创建 Session：`POST /api/session`，带 `model`、`location.directory`、
  `permissions:[{"action":"*","resource":"*","effect":"ask"}]`；
- 发消息：`POST /api/session/{id}/prompt`；
- 流式事件：`GET /api/event?types=...`（SSE），使用 `session.text.delta`、
  `session.text.ended`、`session.tool.*`、`permission.asked`、`session.execution.*`；
- 历史：`GET /api/session/{id}/message`；
- 中断：`POST /api/session/{id}/interrupt`；
- 权限：`POST /api/session/{id}/permission/{requestId}/reply`
  （`decision = once | always | reject`）。

没有重新实现 Agent Loop、Provider 或 Tool Calling。

## 持久化

`conversations`、`messages`、`events` 三张表：

- 用户消息和最终 assistant 文本写入 `messages`；`remote_id` 保存 OpenCode 稳定消息 ID，
  同一文本的不同消息不会合并，重复同步幂等；
- `text_delta` 只做实时推送，不写库，避免 token 级写放大；
- 工具调用、权限请求、状态变化写入 `events`，用于断线重放；
- `events` 每个对话保留最近 500 条；历史真相仍是 `messages`。

打开历史对话时，`Sync` 会用 OpenCode 的 message 列表补齐遗漏的最终文本；只导入
user/assistant 文本，不导入 reasoning 或工具内部状态。新消息优先按 `remote_id` 匹配，
旧消息仅在唯一匹配时做一次兼容 adoption。

SSE 订阅会分页回放，最多 4096 条；超过上限、事件已被清理或慢消费者导致缓冲区溢出时，
会显式发送 `event: resync`，前端重读消息而不是静默跳号。

## 工具权限

聊天 Session 的权限规则是“全部 ask”。写文件、执行命令会在 `/chat` 右侧/消息流中
弹出批准卡片；批准只对本次请求生效（也可选择 always）。关闭窗口不会让已经开始的
OpenCode 执行自动变成成功；重新打开后通过 Session/message 同步恢复。

这不是 OS 级沙箱：如果用户批准命令，它以当前用户身份执行。聊天默认工作目录是
`<workspace>/chats/<conversation>`；关联 Objective 时使用该项目目录。

## 恢复

- 服务重启：Conversation/Session ID 存在 SQLite 中；私有 OpenCode Server 重新启动后，
  若 Session 仍存在则继续，不存在则新建并标明上下文可能丢失。
- 窗口关闭：聊天本身不拥有 Task 生命周期；已授权的托管任务继续在 Kernel 中运行。
- 事件断线：SSE `after`/`Last-Event-ID` 先回放持久事件；最终文本由 `Sync` 校准。

## Goal Handoff

`POST /api/conversations/{id}/handoff` 使用一个禁用工具的短 Session，把最近对话提取成
Goal / Constraints / Expected Outputs / Acceptance / Decisions，并保存
`source_message_ids` 与 `context_version`。用户可以 `PUT` 编辑，只有
`/confirm` 才创建 Objective；计划仍需再次确认。

## 未完成

- 聊天历史的全文搜索与导出；
- 多聊天并发上限与跨设备同步；
- Goal Draft 提取对“只回复收到/不要调用工具”等聊天元指令的过滤依赖模型，需继续观察。
