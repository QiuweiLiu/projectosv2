# State

## Stage
P2 完成：自然语言目标 → 计划 → 人工确认 + 执行前审批 → 并行执行 → 三层验收 →
有界重规划 → 交付 → 继续改进/恢复。默认执行器是 OpenCode CLI；有后台服务、读写 API、
中文驾驶舱与 macOS 原生外壳。单机、单用户、多目标 Workspace。

## Verified
- **174 项测试 + `-race` + `vet` 全绿**；真实运行后无残留进程。
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

## Remaining
- **命令验收不是沙箱**：允许列表只防注入，不防合法命令读全盘/联网（见 `docs/security.md`）。
- 计划字段 `scope/inputs/allowed/forbidden/acceptance/priority` 不参与机器判定（`docs/plan-semantics.md`）。
- 会话续跑只续对话上下文，不续工作目录；重试仍在新目录。
- `aoenv` 可能采纳外来 daemon；日志无轮转；worker 无资源限制；审查首次阻断成本高。
- 未做：SSE、Windows/Linux 桌面壳、逐工具调用审批、二进制行级 diff、日志轮转。
- 未注入：取消与调度器完成的进程级交错、磁盘满/只读挂载、运行中 SQLite 失效。
