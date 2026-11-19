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

同步使用 `order=asc` + cursor 分页，最多导入 2000 条，连续同步保持幂等；若远端 session
已空闲而本地仍是 running，末尾的 `idle` 消息会纠正本地状态。超过 2000 条的长会话只
保留最早的一批，属于已知边界。

SSE 订阅会分页回放，最多 4096 条；超过上限、事件已被清理或慢消费者导致缓冲区溢出时，
会显式发送 `event: resync`，前端重读消息而不是静默跳号。

服务重启或事件流重连后，Manager 会调用 `/api/permission/request` 主动补拉 pending
permission，因此工具批准不会因为一次断线永久悬挂。

聊天使用查询相关的 Evidence Context（`docs/evidence.md`）：只注入已验收产物、显式决策
和活跃/阻塞状态；文件正文以数据块引用，未验证输出不会成为项目事实。Context 按内容哈希
缓存版本：首条消息注入，之后只有证据/决策/状态变化时才在同一对话中重新注入，避免每轮
重复消耗 token。

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
`/confirm` 才创建 Objective；计划仍需再次确认。提取结果使用用户语言，并忽略
“先不要写代码/只确认需求/不要查看文件”等仅约束当前回合的指令。

## 身份与排版

- 每个聊天 Session 会附加一条 `projectos` instruction：助手以 ProjectOS 助手身份回答，
  OpenCode 只作为底层运行时，不再把它当作自身身份。已有 Session 在下一次发送时补写。
- 回复使用块级 Markdown：段落、标题、列表、引用、代码块和表格。代码块改为浅灰卡片，
  表格有横向滚动与边框，行高 1.8；不再把表格按原文展示。
- 按钮、消息、思考块展开使用 120–200ms 的柔和过渡；尊重 `prefers-reduced-motion`。

## 界面反馈

- 用户消息右对齐（头像在右），助手正文左对齐；不再使用左右同侧的聊天气泡。
- 发送后、工具执行间隙显示“正在思考”三点动画，顶部状态点同步闪烁；
  首个 `text_delta`、工具调用或权限请求出现时隐藏，工具完成/审批回复后再次出现。
- 输入区左侧可切换“思考过程：展开/折叠”，默认展开并持久化；思考块本身也可单独点击折叠。
- 生成中发送按钮禁用，输入区出现“停止”，思考行也提供停止；停止走同一个中断接口。
- 动画尊重 `prefers-reduced-motion`，关闭动态效果时只显示静态文字。

## 思考过程

OpenCode 会发送 `session.reasoning.started/delta/ended`。Kernel 把它们映射为
`reasoning_*` 实时事件，不落库（避免把链式思考写进长期事件表）。界面显示可折叠的
“思考过程”，首字出现后自动折叠；每个回合保留一个折叠块，刷新后不保留。

## 中断与失效审批

状态回到 `idle`、`interrupted` 或 `error` 时：

- 移除所有未处理的审批卡片，右侧说明“本轮已结束 / 本次运行已中断，未处理的审批已失效”，
  避免看起来仍在等待；正在“执行中”的工具标为“未完成/已中断”；
- 审批回复遇到上游 404（`PermissionNotFoundError`：已过期、已在别处处理或会话已重建）时，
  不弹错误框，直接移除该卡片并提示“这条审批已失效”；
- Kernel 启动时把上次进程遗留的 `running` 会话改为 `interrupted`：聊天回合属于当前进程，
  重启后不可能继续，否则界面会永久显示“回复中”并保留失效审批；
- 如果最后一条消息是用户消息、没有对应的助手回复，显示“本次回复已中断/失败，没有生成最终回答”，
  并提供“重新编辑”（把原文放回输入框）和“继续”（发送“继续”）。

撤回按钮在用户消息左侧常显（低对比，悬停加强），不再只依赖 hover。

## 问题表单（question 工具）

模型有时会调用 question 工具向你提问。收到 `action=question` 的权限请求时，界面自动允许
一次，然后读取 OpenCode 的 form（`GET /api/session/{id}/form`），渲染单选、多选、布尔、
数字或文本字段；提交走 `POST /api/session/{id}/form/{formID}/reply`，answer 以字段 key
为键。表单接口 5 秒超时；被上游清理或超时时提示“无法加载问题表单，可停止后重试”，
不会把权限卡当成普通“允许/拒绝”处理。

## 撤回消息（回滚）

每条用户消息左侧显示“撤回”。运行中也可以点：确认后先停止当前生成，等待空闲，再执行回滚。执行顺序：

1. 用 OpenCode `POST /api/session/{id}/fork` 在目标消息之前创建子 Session（复制此前的
   投影历史），把 Conversation 切换到新 Session；
2. 删除该用户消息及其后的所有消息与事件（避免 SSE 回放把旧回合带回来）；
3. 把该消息原文放回输入框。

只回滚**对话上下文**：已经执行的文件写入、命令和托管 Task 不会恢复。

撤回分两种结果：

- 消息在 OpenCode 会话里仍存在：fork 到该消息之前，切换会话并删除本地消息/事件；
- 消息没有远端 ID（本地控制短语）或会话没有 session：直接只回退本地对话；
- fork 返回 `MessageNotFoundError`（服务端重启、会话被重建）：接口先返回 409
  `rewind_local_only`，界面明确告知“只能从本地移除 N 条，上下文不会回退”，用户再次确认后
  以 `{"localOnly":true}` 重试，才删除本地消息/事件。结果 `remoteForked:false`。
  不再把本地 `msg_...` ID 当作远端 ID 去 fork，也不会把整段 provider 错误弹给用户。

## 自动托管建议

每轮助手回复结束后，聊天模型会做一次轻量判断（禁用工具、无 reasoning variant）：
对话是否是需要长期推进的目标。命中时写入 `handoff_suggestion` 事件（含 goal/reason/
confidence/sourceMessageId），界面在消息末尾显示“这看起来是一个可以托管的目标”卡片。
用户点击“开始规划”，或在输入框直接回复“可以开始规划/开始吧”这类短句，才会提取
Goal Draft；不会自动创建目标或执行。短句由客户端识别后走控制路径：消息存入对话，
`POST /api/conversations/{id}/handoff/start` 返回草稿，**不产生额外模型回合**。建议按
会话去重，忽略后记在本地，刷新仍可见但不会重复打扰。会话一旦有 `objectiveId`，历史
建议卡片不再显示，顶部按钮变为“打开托管目标”。

## 托管前的停止

点击“转为托管目标”（或建议卡片）时，如果这一轮还在生成，界面先提示并停止：

1. `POST /api/conversations/{id}/stop`，轮询会话直到不再 `running`；
2. 清理失效审批卡片与“执行中”的工具状态，显示中断提示；
3. 再调用 `POST /api/conversations/{id}/handoff/start`（或旧接口 `/handoff`）提取
   Goal Draft。

服务端在 confirm 阶段还有一道守卫：会话仍为 `running` 时返回 409，避免把“正在生成”
的对话直接变成目标。托管完成后对话保留为历史，不再继续生成，也不会再弹旧审批。

## 聊天内托管流程

确认面板不再是弹窗，而是会话流中的一张内联卡片：

- 目标（可直接改）、执行预算（最多运行/审查次数、token 预算）、三步流程、硬约束/
  预期产物/验收标准/决策摘要；“编辑细节”展开多行输入；
- 预算随 Goal Draft 持久化（`goal_drafts.budget`），刷新后仍在；
- 提交后只做“创建目标 + 生成计划”，不会执行任何任务。

计划生成后，会话末尾出现“托管流程”卡片，并每 5 秒从目标状态派生一次：

- 状态/计划版本/任务列表/预算，以及目标→计划→审批→执行→验收→交付的步骤条；
- `planned`：列出需要确认的命令/高风险项，按钮“批准并开始执行”；没有风险时是
  “确认计划并开始执行”；
- `active`：运行中显示暂停；有待执行且无需恢复时显示开始；被卡住时显示“批准恢复并重试 /
  重新规划 / 调整计划”；
- `failed` / `completed`：重新规划或查看交付。

右侧检查器同时显示同样的步骤条与下一步，任务/证据 Tab 仍用于监控。

## 在聊天里修改目标

- 目标尚未执行（`planned`）时，卡片提供“调整目标”：编辑目标文本 → `POST
  /api/objectives/{id}/amend`（仅 draft/planned 且没有任务）→ `POST /api/objectives/{id}/plan`
  重新生成计划；目标文本与验收标准一起更新；
- 目标已经执行或被卡住时，卡片提供“调整计划”：把说明作为 `--instruction` 传给
  `POST /api/objectives/{id}/replan`，保留已通过任务，替换未完成任务；不改写目标文本，
  避免和已验收证据冲突。

## 未完成

- 聊天历史的全文搜索与导出；
- 多聊天并发上限与跨设备同步；
- Goal Draft 提取在模型输出后按常见“只回复/不要调用工具”句式做确定性过滤；模型仍可能
  用其他措辞表达聊天控制，必要时继续扩展过滤表。
