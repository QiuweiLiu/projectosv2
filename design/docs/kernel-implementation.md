# Kernel 实现现状（2026-11-03）

本文件记录 ProjectOS Kernel 从"设计"进入"可运行代码"后的实际状态。代码在
`~/dev/projectos-kernel`（独立 Git 仓库、本地 APFS，不在 iCloud）。

当前实现框架图：`docs/diagrams/architecture.png`（可编辑源 `docs/diagrams/architecture.html`）。

## 已验证的能力

| 能力 | 证据 |
|---|---|
| Task/Run 状态机 + 依赖 DAG | Go 单测 33+；Fake 后端 A∥B→C 成功/失败两路径 |
| 真实执行（OpenCode 2 + DeepSeek） | `reports/real-runs.md`；run-3 全接受、run-5 失败阻断 |
| 公开 API 控制 Worker | `internal/exec/ao`：spawn / conversation / interrupt / workspace file |
| 产物验收（不信 Agent 自述） | 读取工作区文件内容 + `fileFingerprint`；evidence 存 sha256 |
| 有界 Review | P0/P1 阻断、P2 放行；每 artifact 版本 1 完整 + 1 定向 |
| 真实 agent reviewer | run-10 捕获额外文件；run-16 / kernel-run-7 完成 P1→修复→复审→接受 |
| 预算与超时 | MaxRuns / MaxReviews / run timeout，超限 blocked 不重试 |
| 崩溃语义 | 重开库把在飞 Run 标 unknown、Task 标 blocked |
| 进程回收 | chat-host 与子进程按精确参数回收，运行后残留 0 |
| CLI | `kernel run --objective objectives/*.json`、`kernel status [--json]`、`kernel serve` |
| 能力包 | `simple` / `research` 两套 policy；Kernel 无领域词 |
| 预算 | Run 数、Review 会话数、字符估算 token（chars/4） |
| 只读看板 | `kernel serve`：任务表/过滤/自动刷新、任务详情（审查+证据）、事件时间线；`/api/status`、`/api/tasks/{id}`、`/api/events` |

## 明确的边界

- **不是产品**：没有 UI、没有多机调度、没有 token 级预算。
- **reviewer 质量不稳定**：同一配置下出现过漏报（run-9）与误报（run-15），已记录在案。
- **AO 契约缺口**：`change_log` 不含项目事件，项目变化需轮询；`workspaceVersion` 是比对基线而非内容版本。
- **测试边界**：单测覆盖控制流；真实运行覆盖 happy path 与两类失败；网络中断、provider 5xx、host 崩溃未注入。
- **多机**：明确不在范围（单 writer SQLite + 本机 daemon）。
- **token 预算**：字符估算，不是账单级；AO 不暴露 DeepSeek usage。

## 与 ProjectOS Core 的关系

Kernel 不包含 Research 术语；`.project/` 仍是人类可读的权威摘要，运行事实在 Kernel 的 SQLite。
两者通过"摘要只指向运行记录"的方式分层，不做双向同步。

## 异常输入

路径（空格/中文/深路径/只读）、daemon 中途消失、取消失败、时钟跳变、用量未知均有测试，
明细见 `~/dev/projectos-kernel/reports/robustness-matrix.md`。磁盘写满、真实网络分区、
跨平台与断电 WAL 注入未覆盖。

## P1.1 目标驱动（2026-11-03）

- Goal Manager：Objective 持久化 + 状态机；Plan 版本化（draft/confirmed/superseded）。
- Planner：JSON Schema + Kernel 校验（DAG/上限/路径/validator）；Fake 与真实模型两种实现。
- CLI：`kernel goal create/plan/show/confirm/run/status`；真实模型规划 + 真实 Worker + 交付报告已跑通。
- P1.2：有界 Replanner（已接受任务不可改、新增 ≤5、单次运行 ≤1），旧任务 `superseded`；确定性场景已验证。
- P1.3：`.project/` 派生快照（4KB 上限）+ `goal export/resume` + 漂移检测；数据库仍是权威。
- 场景 A（待办应用）**已通过**：依赖产物注入 + `command` 验收（`node test.js`）+ `out/` 组装；
  14/14 测试在组装产物上通过。详见 `~/dev/projectos-kernel/reports/p1-scenario-a.md`。
- P1.2 真实重规划已通过（T1 失败 → T1-fix → 完成）。
- P1.3 崩溃恢复实测：SIGKILL CLI 后重跑，Run→unknown、Task→blocked，不重复执行；复用幸存 daemon。
- 场景 B 真实通过（CSV → Node 汇总 → summary.md）；场景 A/C 均真实通过。
- 依赖产物按传递闭包注入（A→B→C 多跳可用）。
- **L0 客户端解耦（2026-11-04）**：`serve --daemon/--stop/--status`；杀客户端后任务继续并交付；
  `--stop` 只停 HTTP 不杀任务；子进程独立会话 + 文件日志（避免 SIGPIPE）。
- **L1 API**：`/api/objectives`、`/api/objectives/{id}`、`confirm|run|cancel`、`/api/events` 游标；
  `goal cancel` 为协作式取消（已完成/失败的目标不被改写）。详见 `~/dev/projectos-kernel/docs/api.md`。
- 顺带修复：`store.Open` 打开即恢复会把运行中任务判死 → 写打开独占锁、读命令只读。
- **L2 cockpit（2026-11-04）**：`serve` 内嵌单页界面（`cmd/kernel/ui.html` + `//go:embed`）：
  目标列表、计划预览、任务图、证据/审查、时间线、confirm/run/cancel；无框架无构建。
  浏览器内真实点完 create → plan → confirm → run → completed；执行中取消 → cancelled + blocked。
- **三个真实目标（2026-11-04）**：Web/CSV/统计全部可交付。两个首次失败暴露的是声明信息不全
  （reviewer 只看到直接依赖）与计划选择不当（pytest 未安装），已修；reviewer 的两次真实阻断保留。
- **交付整体验收（2026-11-04）**：组装后把 `out/` 复制到临时目录，重跑所有 command 验收；
  失败则 objective 不判 completed，结果写入 `delivery.md`。待办应用 11/11 通过。
- **声明与执行模型对齐（2026-11-04）**：一个任务恰好一个产物（多产物在计划校验阶段拒绝）；
  重试时把 reviewer 的 findings 注入下一次 worker 提示词；`exact` 只用于目标写死的内容。
  CSV 目标复跑一次通过（3 任务 attempts=1，0 阻断）。
- **全量审查（2026-11-04）**：两个独立 reviewer，34 条（P0×3/P1×22/P2×9）；已修 25 项，
  含旧结果覆盖新 attempt、允许列表前缀绕过、token 入日志、默认全网绑定、交付 fail-open；
  重规划改为单事务，daemon 增加启动锁与 instance 校验（不靠 PID 判断身份）。
  计划字段中只有 artifacts/validators/deps/maxAttempts 真正参与判定，其余见 `docs/plan-semantics.md`。
- **验收 agent · 汇报人 · 界面（2026-11-05）**：新增 acceptance 会话（判“是否真的完成”，
  与 reviewer 并列）；汇报人提供规则版中文摘要与模型改写；界面重做为中文驾驶舱。
- **成果与继续改进（2026-11-05）**：界面展示 `out/` 文件与预览，汇报人据此说明交付内容；
  已完成/失败目标可追加任务继续改进（已验收任务保留、不重跑）。
- **执行器切换（2026-11-05）**：默认改为直接调用 OpenCode CLI（`opencode run --standalone`），
  每任务独立目录（git worktree / 拷贝）、进程组取消、pid 文件崩溃清理、worker/审查权限分离；
  `--executor ao` 保留。模型由 `--model / --review-model / --acceptance-model` 指定，
  可用 OpenCode 里注册的任意 provider/model（已在 `opencode-go/minimax-m3.1-flash` 跑通）。
  另见 `docs/executor.md`、`docs/models.md`、`docs/security.md`、`docs/plan-semantics.md`。
- **第二轮审查（2026-11-05）**：修复 9×P1 + 1×P2——角色权限未生效、acceptance 依赖 reviewer、
  重复版本洗白、交付跟随符号链接、worker 用量漏计、失败会话未知用量、review 预算超限、
  quarantine 持久化、完成写失败仍报成功、取消竞争。142 项测试。
- **第三轮审查（2026-11-05）**：planner 只读、acceptance 结论按类型读取、quarantine 文件兜底、
  完成写入顺序、陈旧取消标记——5 条修复；六个故障场景已有注入/集成测试；152 项测试。
- **P2 可用性（2026-11-05）**：
  - **多目标 Workspace**：`D/projects/<id>/` 每目标独立 DB / 锁 / work / out；一个 serve 聚合，
    可并行执行；旧式根目录单目标继续兼容。
  - **执行前审批**：高风险任务与 `command` 校验需要 `--approve-risk`；驾驶舱弹窗展示风险、命令和预算。
  - **文本产物 diff**：最新尝试 vs 上次交付/上一尝试，任务详情内查看。
  - **会话续跑**：捕获 OpenCode session id，重试传 `--session`；仍使用新工作目录。
  - **work/ 保留策略**：默认保留最新 20 个尝试目录，`goal prune --keep N` 可调。
  - **token 记账**：读取 `step_finish.part.tokens` 实测值，缺失才标注估算。
  - **macOS 原生外壳**：Objective-C + WKWebView，`desktop/build.sh` 产出 `.app`。
  - 174 项测试 + race + vet；真实目标、双目标切换、审批弹窗、diff、桌面 `--check` 已验证。
- **M1/M2 桌面工作台（2026-11-06）**：
  - **M0**：`runNow` 只生成计划，不再隐式 confirm/run；Objective 级 acceptance 成为
    completed 前的独立门禁；删除旧代码生成的 query-string SQLite 残留。
  - **持久化聊天**：`conversations.db` 保存 Conversation/Message/Event；OpenCode 2.2.5
    私有 v2 Server，Session 创建、续聊、history、interrupt。
  - **SSE**：`session.text.delta` 实时，最终文本落库；工具 `permission.asked` →
    `/chat` 批准一次/总是/拒绝；`after`/`Last-Event-ID` 重放。
  - **`/chat` 工作台**：项目/对话、聊天、Objective/Task 三栏；桌面壳默认打开；工具卡片、
    历史恢复、按项目过滤。
  - **M3 最小**：Project catalog + 目标目录 `project.json` 归属；
    `project-state/<id>/.project/` 从目标库重建项目级 PROJECT/STATE/HANDOFF/DECISIONS；
    快照有 8KB/条目上限，支持 `GET /api/projects/{id}`、refresh、decisions 和 CLI。
  - 192 项测试 + race + vet；真实聊天、工具权限、重启恢复、Objective acceptance 已验证。
- 尚未实现：Cockpit 的 Kernel SSE、Windows/Linux 桌面壳、托管任务逐工具审批、
  二进制行级 diff、取消/磁盘/SQLite 故障注入、Project 级 `.project` 上下文。

## 下一步

1. 用 cockpit 跑真实目标，收集操作摩擦；只修影响正确性的（计划质量 > 界面）。
2. 能力包扩展（自定义 validator / 更细的 policy）。
3. 真实网络故障注入（kill daemon、断网）与恢复语义。
