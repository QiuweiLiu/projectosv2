# State

## Stage
M1/M2 完成：在 P2 的 Kernel 之上增加持久化聊天工作台。普通聊天不创建 Objective；
OpenCode v2 Session + SSE 流 + 工具级 ask 审批；聊天可显式转成托管目标。
Kernel 仍是 Task/Run 唯一写者；单机、单用户、多目标 Workspace + Project 归属。

## Verified
- **192 项测试 + `-race` + `vet` 全绿**；真实运行后无残留进程。本轮提交 `0fe6495`，已 push。
- **场景 A**：待办应用 3 任务，`node test.js` 验收，`out/` 组装后 11–14 项测试通过。
- **场景 B**：sales.csv → summarize.js → summary.md，总额 60（一次通过，0 阻断）。
- **场景 C**：真实模型重规划（T1 失败 → T1-fix → 完成）。
- **场景 D**：SIGKILL CLI → Run unknown / Task blocked，不重复执行；复用幸存 daemon。
- **L0/L1**：杀 `serve` 后任务继续并交付；`goal cancel` 14s 生效、0 残留；事件游标不重复。
- **L2**：浏览器内完成 创建 → 计划 → 确认 → 执行 → 交付；执行中取消 → cancelled。
- **第二轮审查 10 项**：权限、验收独立性、重复版本、交付符号链接、用量、失败会话、
  预算、隔离持久化、完成写失败、取消竞争——全部修复。
- **第三轮审查 5 项**：planner 权限、acceptance 结论按类型读取、quarantine 文件兜底、
  完成写入顺序、陈旧取消标记——全部修复；quarantine 写库失败、快照写失败、
  陈旧取消标记都有故障注入/集成测试。
- **P2**：Workspace 双目标创建/切换/详情/汇报/事件；真实目标全流程；审批弹窗、
  文本 diff、桌面 `--check`；token 为 provider 实测值。
- **M0**：`runNow` 只生成计划，不再隐式确认/执行；Objective 级 acceptance 成为独立
  完成门禁；删除旧代码生成的 query-string SQLite 残留并加忽略规则。
- **M1/M2 真实聊天**：OpenCode 2.2.5 私有 v2 Server；Session 创建/续聊/prompt；
  SSE `text.delta` 实时、最终文本落库；工具 `permission.asked` → UI 批准；重启后历史恢复；
  聊天不产生 Objective。SSE 事件 `after`/`Last-Event-ID` 可重放。
- **M3 最小**：Project catalog + objective 的 `project.json` 归属；聊天可按项目过滤；
  项目级 `project-state/<id>/.project/` 快照（目标/成果/阻塞/决策），有 8KB 上限。
- 真实校验：`/chat` 浏览器发送、流式回复、工具权限、重启恢复；详见 HANDOFF。
- **CLI 执行器**：`opencode-go/minimax-m3.1-flash` 与 `deepseek-v4.1-flash` 真实跑通 计划 → worker → reviewer →
  acceptance → 交付；崩溃残留进程回收有回归测试。
- **全量审查**：34 条（P0×3/P1×22/P2×9），已修 25，未修 9 见 Remaining。
- 证据：`reports/`（p1-acceptance、p1-l0-l1、l2-cockpit、l2-friction、delivery-checks、
  retry-and-artifacts、acceptance-and-reporter、outcome-and-continue、full-review-2026-11-04）。

## Capabilities
- **目标与计划**：objective 状态机、plan 版本（draft/confirmed/superseded）、Schema/DAG/路径校验；
  一个任务恰好一个产物且全局唯一；planner 不接收宿主绝对路径。
- **执行**：并行 DAG、依赖产物祖先闭包注入、命令验收 sandbox、预算/超时/quarantine。
- **执行器**：默认 `opencode`（`opencode run --standalone --format json`，每任务独立目录；
  有 git 仓库用 worktree，否则拷贝；进程组取消；pid 文件 + cwd 校验的崩溃清理；
  worker `--auto`、审查/规划默认拒绝）。`--executor ao` 保留。
- **模型**：按角色配置（规划/执行/审查/验收/汇报），存 `models.json`，界面下拉框可选；
  解析顺序 flag > models.json > 内置默认；模型列表来自 `opencode --standalone models` 缓存
  （见 `docs/models.md`）。
- **验收三层**：Kernel 确定性检查 → reviewer（查缺陷，P0/P1 阻断）→ acceptance（查完成度）。
- **失败处理**：有界重规划（1 次、≤5 新任务、已接受不可改、修复必须新 ID）；重试注入 findings。
- **继续改进**：已交付目标可追加 ≤5 个新任务；已验收任务原样保留、不重跑。
- **交付**：`out/` 组装 + 副本上重跑命令验收，失败即 failed；`delivery.md/json`、`.project` 快照。
- **服务**：`serve --daemon/--stop/--status`、`serve.lock`、instance 身份校验、token 0600、默认回环；
  Workspace 聚合 `projects/<id>` 多目标，每目标独立锁/预算/out，可并行。
- **界面**：中文状态、汇报卡片、成果区、任务图、证据/审查、时间线、confirm/run/cancel、继续改进；
  执行前审批弹窗（风险/命令/预算）、任务产物的文本 diff、多目标切换。
- **恢复**：写打开独占 `kernel.lock` 并恢复；读命令只读打开，不触发恢复。
- **P2**：多目标 Workspace、执行前审批、文本 diff、`--session` 续跑、work 保留 20、token 实测优先、
  macOS 原生外壳（WKWebView）。
- **聊天**：Conversation SQLite、OpenCode v2 Session/SSE、工具 ask 审批、历史同步、Project 过滤。

## Remaining
- **命令验收不是沙箱**：允许列表只防注入，不防合法命令读全盘/联网（见 `docs/security.md`）。
- 计划字段 `scope/inputs/allowed/forbidden/acceptance/priority` 不参与机器判定（`docs/plan-semantics.md`）。
- 会话续跑只续对话上下文，不续工作目录；重试仍在新目录。
- 聊天事件只保留最近 500 条/对话；`text_delta` 不持久化；消息历史无级联清理。
- 托管任务仍是执行前审批，不是逐工具拦截；command 不是沙箱。
- 项目级 `.project` 快照已生成；尚未把它自动注入聊天/Planner 的 prompt。
- 未做：Cockpit SSE（聊天已 SSE）、Windows/Linux、二进制行级 diff、日志轮转。
- 未注入：取消与调度器完成的进程级交错、磁盘满/只读挂载、运行中 SQLite 失效。
