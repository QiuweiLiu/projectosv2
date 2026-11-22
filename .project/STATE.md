# State

## Stage
P6 可靠性加固进行中。P6.0（Manifest 并发读取、Evidence 预算/可信等级、文档）已完成；
P6.1（DB/执行器/文件系统/并发故障注入）完成关键项；P6.2 完成长会话分页与事件保留验证；
P6.3 完成 1 个真实 OpenCode/ProjectOS 对照样本，B/C 尚未运行。
Kernel 仍是 Task/Run/Evidence/Review 唯一权威；没有第二套 Scheduler 或 Agent Loop。

## Verified
- **298 项 Go 测试 + `-race` + `vet` 全绿**；桌面 `--check` 通过。
- **P6.0 Manifest**：seed 现在先取共享锁再读 Manifest，Manifest 与文件处于同一版本区间；
  发布 crash 后恢复 `.artifacts-old`、清理 staging；`TestSeedReadsManifestAndFilesUnderOneVersionLock`
  证明不会跨版本组合。
- **P6.0 Evidence**：`RenderBounded` 在结构化 Bundle 上控制预算；不截断 UTF-8、不产生未闭合
  EVIDENCE 块、超长条目降级为 provenance 摘要；转义恶意分隔符；长中文/Emoji/多决策/大历史测试通过。
- **P6.0 可信等级**：`published` / `objective_accepted` / `task_accepted` / `user_confirmed` /
  `in_progress` / `unverified`；只有 Manifest 哈希匹配的 artifact 标为 published，未完成
  Objective 的 accepted Task 不会伪装成已交付成果。
- **P6.0 文档**：recovery/artifacts/evidence/STATE/HANDOFF 与实际实现同步；可选
  `--recovery-model` 已记录，不再标记未实现。
- **P6.1 DB**：关闭 store 后 settle/review/completion 写失败均不产生虚假成功；重启后
  interrupted Run 变 unknown/blocked；并发 requeue 只有 1 个赢家。
- **P6.1 文件系统**：read-only DB/目录、snapshot/delivery/manifest 写失败、seed 哈希/符号链接/
  路径逃逸、发布并发、crash 目录恢复均有测试；旧完整树在发布失败时保留。
- **P6.1 执行器**：OpenCode CLI 非零退出显示 failed 及 stderr；Server 未打印密码即退出时
  Start 失败且不残留；Objective judge 会话现在会收到交付文件，不再检查空目录。
- **P6.2**：1200 条消息分页无丢失/重复（183 ms，SQLite 282,624 bytes）；事件保留 500/对话；
  慢消费者 5000 事件 resync；发送/读取分页与恢复测试通过。
- **P6.3 Task A**（n=1，deepseek-v4.1-flash）：baseline 140.1s / 6,583 input / 385 output，
  hidden PASS；ProjectOS final 629.4s / 45,957 input / 1,024 output，hidden PASS，0 人工介入。
  ProjectOS 多出 Planner + 2 Workers + 5 review/acceptance calls；这是成本负结果，不是
  ProjectOS 优势证明。详见 `reports/p6-benchmark-2026-11-07.md`。
- **P5 保留**：场景 A 自动恢复、B/C 真实 worker 读取共享产物、C 中断后人工恢复完成。
- **蓝白工作台真机验收**：审批闭环、流式、Handoff 草稿/确认、Cockpit 计划与执行前门禁、
  失败 Recovery、完成证据、断线重连、项目切换均通过；修 11 项前端缺陷（流式去重、
  状态标签、Markdown、欢迎态、深链、证据 Tab、审批复位、IME 回车等）。截图见 HANDOFF。
- **OPENCODE_BIN**：planner/worker/reviewer/acceptance 现与聊天共用该变量（`goal.go`），
  回归测试 `TestOpencodeBinHonoursEnvironment`；PATH 仍为 v1.18.15 时 planner 实测成功。
- **多工作区标签**：每标签独立 serve 端口与 WKWebView，同时保活；关闭标签只停自身服务，
  退出停自己启动的服务；偏好持久化；`--check-multi` 与 `--dump-tabs` 已验证。
- **显式重新规划**：`goal replan` + `POST /replan` 保留已通过、替换 blocked/pending，
  升到 vN+1 并回到 active；真实 7 blocked → 4 pending（v2）；fake 测试覆盖替换/无操作。
- **聊天内托管**：建议/短句 → `POST /handoff/start`（不产生额外回合）→ 内联草稿（目标/
  预算/约束/产物/验收）→ 目标+计划 → 底部“托管流程”dock + 侧栏步骤；5s 轮询。未执行可
  “调整目标”（amend+plan），已执行/卡住可带 instruction 重新规划。
- **撤回/关联**：fork 404 先按文本+时间找回真实 ID，仍失败才 409 + localOnly 二次确认；
  撤回不删 Kernel 目标，cancelled/failed/completed 可“从对话移除”（只清 objective_id），
  撤回后已取消/失败目标自动断开；取消目标不进入聊天证据，已验收产物仍保留。
- **审查预算修复**：同一产物版本重试不再重复调用 reviewer，直接复用已记录结论；审查落库
  失败仅在用量确实未知时才全局停止，已知用量的策略性拒绝只阻塞当前任务。新增
  `TestIdenticalRetryReusesRecordedReviewAndDoesNotPoisonOthers`。
- **模型选择**：6 角色下拉与思考强度（`#variant` 写入 variants，reviewer/acceptance 跟随
  worker）；空缓存自动刷新；`goal models --refresh` 遵循 `OPENCODE_BIN`；实测 159 项。
- **聊天界面**：用户消息右对齐、思考折叠块、Markdown/柔和动效；中断清理审批并提供重新编辑/继续。
- **聊天审批/预算**：只读自动 once；输入区“帮我审批”开关（低风险模型自动允许，高风险
  弹卡并附原因；失败按高风险）；审批 dock 可堆叠；托管确认异步、计划可重试；预算只填 token；
  每个对话最多一条托管建议（≥2 用户回合、无提问、confidence≥0.75）。
- **对话控制**：思考过程默认展开、输入区可切换并持久化；生成中输入区/思考行可停止；
  question 工具通过 OpenCode form 渲染并提交答案，解决等待问题时的卡住。
- **桌面对话框**：WKWebView 接入 `WKUIDelegate`；confirm/alert/prompt 不再静默失败。
- **驾驶舱状态**：派生执行中/已卡住/待执行；卡住时隐藏开始，显示任务级原因与恢复/重规划。
- **R2/F 收口**：三层审批；F-01..F-07 与复核（真实路径 symlink/`..`、`--`、敏感文件、
  递归与 `git diff/show/log` 内容门禁、旧规则、delivery 回滚、rewind 歧义、evidence 预算）；
  时间戳/撤回、Subscribe、PlanUsage；复核 SUBMIT；`-race`/vet/check 通过。
- **安全审查 P0/P1**：webfetch 不再自动放行；远程读 API/files 需 token，files 加 CSP
  sandbox；token 恒定时间 + Host/Origin 同端口；delivery 符号链接、verify 绝对路径、
  opencode 管道死锁、AO 超时、objective 瞬时错误缓存、rewind localOnly/Sync、evidence
  去重/预算、replan 跨计划、review kind 过滤等已修；`-race`/vet/desktop check 通过。
- **托管一致性**：托管前先停止生成并清审批；已托管会话不再显示建议卡，按钮改
  “打开托管目标”；启动时把遗留 running 会话改为 interrupted；审批 404 内联提示。
- **桌面环境**：外壳自动发现 OpenCode Desktop CLI 并注入 `OPENCODE_BIN`；
  `env -i PATH=/usr/bin:/bin --check-models` 通过（Finder 短 PATH 场景）。
- **异步标签 + iCloud**：窗口先开，标签各自准备；未下载目录会请求 iCloud 下载并显示状态，
  完成后自动启动；失败可重试。实测 `Documents/测试` 下载完成并启动 8800。
  `RunKernel` 20s 超时；端口池 16 个。

## Capabilities
- Objective/Plan/Task/Run 状态机、DAG 并发、SQLite 单写者、事务性结算。
- Planner/Worker/Reviewer/Acceptance/Reporter；Objective 级验收；有界重试/重规划/Recovery。
- OpenCode CLI/Server；Conversation/SSE/工具审批；Goal Handoff；Project Context/Evidence。
- 共享产物 Manifest + seed；Progress/Metrics；WKWebView Cockpit。

## Remaining
- P1 剩余：ctx 取消/进程回收、UTF-8 截断统一；P2 清理。
- **B/C benchmark 未运行**；Task A 只有 1 个样本，不能据此判断整体收益。
- 磁盘满、只读挂载、SQLite WAL 损坏、OpenCode Server 退出重连未做现场注入。
- Recovery 模型辅助诊断仅 fake backend 单元验证；真实模型质量未评估。
- 命令验收不是沙箱；托管任务仍是执行前审批，不是逐工具拦截。
- 聊天消息保留 1000、事件 500；Windows/Linux、日志轮转、桌面签名未做。
