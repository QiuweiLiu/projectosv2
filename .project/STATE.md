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

## Capabilities
- Objective/Plan/Task/Run 状态机、DAG 并发、SQLite 单写者、事务性结算。
- Planner/Worker/Reviewer/Acceptance/Reporter；Objective 级验收；有界重试/重规划/Recovery。
- OpenCode CLI/Server；Conversation/SSE/工具审批；Goal Handoff；Project Context/Evidence。
- 共享产物 Manifest + seed；Progress/Metrics；WKWebView Cockpit。

## Remaining
- **B/C benchmark 未运行**；Task A 只有 1 个样本，不能据此判断整体收益。
- 磁盘满、只读挂载、SQLite WAL 损坏、OpenCode Server 退出重连未做现场注入。
- Recovery 模型辅助诊断仅 fake backend 单元验证；真实模型质量未评估。
- 命令验收不是沙箱；托管任务仍是执行前审批，不是逐工具拦截。
- 聊天消息保留 1000、事件 500；Windows/Linux、日志轮转、桌面签名未做。
