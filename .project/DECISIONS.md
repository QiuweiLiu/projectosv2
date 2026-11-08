# Decisions

## 2026-11-03 — K-D001：Go + 纯 Go SQLite
- 语言 Go（与执行层 AO 同栈，未来可局部嵌入）；SQLite 驱动用 modernc.org/sqlite，避免 cgo 与 C 工具链依赖。
- 代价：首次编译慢、二进制大；收益：跨平台构建简单。

## 2026-11-03 — K-D002：状态所有权
- Task/Run 状态只由 Kernel 的 store 写；Backend 只能报告 Run 结果。
- 重开库不猜结果：在飞 Run → unknown，Task → blocked，等显式 requeue。
- 依赖被 blocked 时，下游级联 blocked，不静默 pending。

## 2026-11-03 — K-D013：能力包、预算口径、只读看板
- 领域差异放 `internal/policy`；Kernel 只认 pack 提供的默认尝试次数与"是否必须 review"。
- token 预算用字符估算（chars/4），因为 AO 不暴露 DeepSeek 的 usage；这是估算，不是账单。
- `kernel serve` 只读：一个 HTML 表 + `/api/status` JSON；打开旧库时自动迁移可写连接、只读连接容忍缺列。
- 多机明确不做：当前是单 writer SQLite + 本机 daemon；跨机需要分布式协调，不属于 MVP。

## 2026-11-03 — K-D012：Reviewer 权限与 Start 恢复
- reviewer 会话以 `approvalMode=default` 创建：工具调用需要人工批准，实际等于只读；实测 worker=`auto`、reviewer=`default`，reviewer 未产生文件。
- Start 的幂等键稳定；仅对**传输层错误**（无 HTTP 响应）重试 3 次，HTTP 语义错误不重试。提交后立即崩溃的极端窗口仍无外部查询接口可用。
- 时间字段解析失败一律返回错误；不允许退化为零值（否则超时检查会静默失效）。

## 2026-11-03 — K-D011：审查后的状态机收紧
- 结算只有一个入口 `SettleRun`：同一事务内校验 run 仍 running、task 仍在该 attempt，写 run/task/evidence；迟到结果返回 `ErrStaleRun`。
- 未决结局（unknown / cancelled / timeout / observe 失败）一律 Block，不自动重试；重试只发生在"有明确失败原因且可修"的路径。
- 重试 prompt 追加上一轮拒绝原因，让 worker 有可操作信息。
- review 计数以数据库为准；`(task_id, artifact_version, kind)` 唯一索引兜底并发。
- 预算/计数查询失败视为错误，不允许 fail-open。

## 2026-11-03 — K-D010：单一入口与 fail-closed
- 只保留 `cmd/kernel`；演示场景改由 `objectives/*.json` 表达，避免入口分叉。
- `status` 用只读连接（`mode=ro`），不会触发恢复、不修改状态。
- 新增单次运行超时：超时 → Cancel + Run unknown + Task blocked，不自动重试。
- 后端不可达、审查无法记录、运行超时都归为 blocked，理由写入 `last_error` 与 events。
- 经验：提示词模板必须单测其参数布局（kernel-run-6 因 `%s` 错位把文件清单当成内容）。

## 2026-11-03 — K-D009：CLI 与 Objective 契约
- Objective JSON 是唯一外部输入；validator 只支持 `exact` / `contains` / `nonempty`，避免在配置里引入脚本执行。
- `--dry-run` 必须完全不触碰模型与 daemon，只做 schema + 依赖图校验。
- 退出码：0 全 accepted、2 存在 blocked、1 引擎/环境错误。
- CLI 仍是薄壳：所有状态与门禁逻辑留在 engine/store，命令层不写业务判断。

## 2026-11-03 — K-D008：复审键与提示词的三个实测修正
- **AO 的 `workspaceVersion` 不是产物内容版本**：run-11 里两次 attempt 的 `a.txt` 都没变但工作区变了，`workspaceVersion` 仍相同，导致"复审旧版本"永远挡住新结果。改为 reviewer 使用内容指纹 `sha256(content + sorted(changed files))` 作为 review version。
- **review 会话的幂等键必须按 artifact 状态区分**：run-14 第二次 review 复用 `kernel-review-A-1`，AO 直接 409 复用原会话。`StartRequest.Key` 现由 artifact 内容哈希生成。
- **定向复审不能把 code 当成"必须上报"**：run-15 中提示词要求"只报 unrequested_extra_file"，模型于是把本该正常的 `a.txt` 硬说成额外文件（false positive）。改为给出上一轮 finding 的**原始描述**并明确"已解决就不要重复"。
- 不可记录的 review（越界/过期/额度）一律 fail-closed：直接拒绝任务，不再静默放行。

## 2026-11-03 — K-D007：真实 Reviewer
- reviewer 是独立 AO 会话：只拿任务目标、artifact 路径与内容、**本次改动的文件清单**；不拿工作区写权限。
- 输出结构化 JSON；超时、会话失败、解析失败一律 P1（fail-closed），不允许"审不了就放行"。
- 每轮 review 都写库（包括 `[]`），这样"审过且无问题"也可审计，并占用该版本的一次完整复审额度。
- 已知局限：run-9 证明只给 artifact 内容会漏掉额外文件；run-10 加入文件清单后修复。reviewer 质量仍需持续回归。
- 定向复审：允许在修复后的新版本上执行，但 finding code 必须属于上一轮 P0/P1 集合（`ErrReviewScope`）。

## 2026-11-03 — K-D006：有界 Review
- 严重度：P0/P1 阻断，P2 记录不阻断，P3 仅提示；Review 不能直接改 Task 状态。
- 每个 artifact_version 只允许 1 次完整 review + 1 次定向复审；换版本（新 Run）计数重置。
- Review 必须绑定 artifact_version；版本不符即 `ErrReviewStale`，防止"审旧版、放新版"。
- 真实 agent reviewer 尚未接入；当前 `--inject-review` 仅验证门禁逻辑，不代表审查质量。

## 2026-11-03 — K-D005：证据、预算、单 writer
- 每次验收写 evidence 行：run_id、artifact_path、artifact_version、内容 sha256、accepted、reason。拒绝也可追溯。
- 预算以"已启动 Run 数"为硬上限（`MaxRuns`），超限把剩余 pending 统一 blocked，不静默重试；token 级计量留待 AO 暴露 usage。
- 单 writer 用 state 目录上的 flock（`ErrLocked`），与 SQLite 原子 claim 互补：一个防多进程调度，一个防并发重复领取。

## 2026-11-03 — K-D004：真实执行与验收
- 验收读**工作区文件内容**（`GET /workspace/file?path=`），不读 Agent 自述；Evidence 用 `workspaceVersion`/`fileFingerprint`。
- 依赖任务 prompt 用 `${TASK_ID}` 占位符注入上游 accepted 产物，避免人工转抄。
- Start 用 `clientRequestId` 做会话级幂等；Cancel 走 turn 级 interrupt，409 `CHAT_NO_ACTIVE_TURN` 视为已结算。
- AO 的 chat-host 是脱离进程：必须按 `chat-host <session> <dataDir>` 精确匹配并连子进程一起回收；否则 daemon 停止后仍泄漏。

- 真实 AO Backend 之前先用 Fake 固化控制流；Fake 通过不代表真实执行通过。

## 2026-11-03 — K-D014：目录复用、超时收尾、取消与用量
- 状态目录只在 objective hash 一致时 resume；其他情况拒绝，且拒绝发生在启动 daemon 之前。
- 目标级超时必须调用 `Abort`：取消在飞 Run（重试 2 次），再把 Run 标 unknown、Task 标 blocked。
- 取消失败不得静默：写 `cancel_failed` 事件与 `last_error`，让"外部仍在跑"可见。
- token 估算必须包含 reviewer 自身用量；用量读取失败按 fail-closed 处理，不能当 0。
- 定向复审允许发现新引入的 P0/P1；新问题同样阻断，不做"只查旧账"。

## 2026-11-03 — K-D015：回归修复
- 只读兼容按表独立判断：旧库缺 `meta` 表、缺 `reviews` 用量列都不应报错。
- blocking 以"该版本最新一条 review"为准：干净的 targeted review 可以解除旧 full 的阻断。
- 取消失败进入 quarantine：停止新调度，避免外部 Worker 与新任务并发。
- Abort 并行取消、单次 10s、整体 60s，退避可被 context 中断；cancel 错误按任务落盘。
- 任何已发生的用量（Collect 部分成功、Reviewer 解析失败）都必须记账，不能按 0 处理。
- resume 还需比对 policy pack，防止用不同策略续跑同一目录。

## 2026-11-03 — K-D016：路径、隔离与预算的最后一轮
- 进程匹配不能按空白分词：data dir 允许含空格，匹配用带边界的前缀，且保证 run-1 ≠ run-10。
- quarantine 必须持久化到 meta；重启不得自动恢复调度；清除只能由人显式 `--clear-quarantine`。
- 用量未知（Collect 失败且无字符计数）时，停止整个 objective 的后续调度，不按 0 计。
- Abort 有硬截止：到点后不再等待，按"cancel 未在截止前完成"记账并继续收尾。

## 2026-11-03 — K-D017：异常输入矩阵
- 路径形态（空格/中文/深路径/只读/旧库）纳入测试，不依赖模型。
- daemon 中途消失必须有测试：连接被拒、连接中断两种。
- 时钟前后跳变分别验证：未来不得误判超时，过去必须超时。
- 明确不覆盖：磁盘写满、真实网络分区、跨平台、断电 WAL 注入。

## 2026-11-03 — K-D018：回收与未知用量的最后修正
- 进程匹配必须消费完整 data dir 并验证其后的 worktree 归属，避免 "/tmp/project" 命中 "/tmp/project old"；ReapAll 显式跳过 session token。
- Abort 只要有一个 worker 未被确认停止，就持久化 quarantine。
- reviewer 失败且用量不可知、或审查无法记录时，停止整个 objective 的后续调度。
- 并发取消的共享 map 只在锁内复制后使用；晚到写入不得被读。

## 2026-11-03 — K-D019：空值与边界参数
- 公开入口拒绝空路径/空 ID/空 key：宁可报错，也不写不可寻址的行。
- `store.Open("")` 必须失败——空路径会被 SQLite 解释为临时库，等于静默丢数据。
- 负预算与负超时归一化为"无限制/关闭"，不静默改变语义。
- 未接线的 Engine 与 nil Reviewer 返回错误或 fail-closed，不 panic。

## 2026-11-03 — K-D020：只读看板
- 看板只读：任务表、过滤、自动刷新、任务详情（审查/证据）、事件时间线；没有写接口。
- 证据的 artifact 版本使用内容指纹（content + changed files），不沿用 AO 的 workspaceVersion。
- 事件接口按 seq 分页（after/limit），为增量刷新留接口。
- 默认只监听 127.0.0.1；要对外必须先加鉴权。

## 2026-11-03 — K-D021：P1.1 目标与计划
- Objective 与 Task 分离，持久化在 Kernel SQLite；状态机显式，非法转换直接拒绝。
- Planner 输出必须通过 Kernel 侧 Schema/DAG/上限/路径校验；自由文本不进入任务。
- 计划版本化：draft → confirmed，旧版本标记 superseded；已执行任务的目录不允许重建计划。
- 一个 state dir 只允许一个 objective（P1 约束，避免任务混合）。
- 计划变更必须有明确状态转换；本轮不实现自动 Replanner。

## 2026-11-03 — K-D022：有界重规划
- Task 增加终态 `superseded`；Objective 完成只统计当前 plan version。
- 重规划默认关闭，单次运行最多 1 次，新增任务 ≤5；已接受任务的 artifact/validators 不可改。
- 旧计划未完成任务标 superseded，不执行、不重试；交付报告如实列出。
- 真实模型重规划尚未验证，只有确定性 fake 场景。

## 2026-11-03 — K-D023：长期上下文
- SQLite 是运行时唯一权威；`.project/` 只是派生快照，可随时重建。
- 快照由唯一 writer 生成，单文件 ≤4KB；超限拒绝写入，避免 Markdown 无限增长。
- `goal resume` 只读校验哈希漂移；`--write` 才重建；数据库缺失时明确拒绝恢复。

## 2026-11-03 — K-D024：场景 A 的假阳性
- 真实待办应用场景中，T3 依赖 T2 的 app.js，但每个任务在独立 worktree，下游拿不到上游产物。
- Validator 只检查单文件内容，导致"测试根本跑不起来"仍被接受；objective completed 是假阳性。
- 修复方向：spawn 后注入依赖产物再发任务；增加受限的可执行验收（命令 exit code）。
- 在此修复前，场景 A 不计为通过。

## 2026-11-03 — K-D025：可执行验收与依赖注入
- 验收支持 `command`（白名单、无 shell、exit code），在由「本任务产物 + 依赖产物」组成的 sandbox 中运行。
- 依赖产物内容注入 worker 提示词（4KB/文件、6KB 总预算），以适配 AO 的 16KiB prompt 上限。
- reviewer 明确看到依赖文件清单，不得把已验收依赖当作额外文件。
- 交付时把已验收产物组装到 `out/`，交付物是整体而不是分散 worktree。
- `serve --control` 提供 token 保护的网页目标输入；写操作仍由 CLI 子进程执行，Kernel 保持唯一写者。

## 2026-11-03 — K-D026：重规划的真实闭环
- 真实模型会复用失败任务的 id；Kernel 拒绝这种替换（"give the fix a new id"），并带反馈重试一次。
- 替换计划中被保留的旧任务只能是已接受任务；失败任务必须退役，由新 id 的修复任务取代。
- 真实场景已通过：T1(41≠42) → 模型产出 T1-fix → accepted。

## 2026-11-03 — K-D027：崩溃恢复与传递依赖
- CLI 崩溃后重跑：复用仍存活的 daemon（running.json），不重新拉起；不由本进程启动的 daemon 不负责停止。
- 验收 sandbox 与提示词注入改为传递闭包：祖先任务的产物都会出现，支持多跳依赖。
- 崩溃后 running Run 由 store.Open 恢复为 unknown，Task blocked；不会自动重试。

## 2026-11-04 — K-D028：打开即恢复是危险的
- 现象：`store.Open` 每次打开都执行恢复（running Run → unknown，running Task → blocked），
  于是运行中执行一次 `goal status` 就把在跑的任务判死。
- 决定：恢复只属于写者。`store.Open` 在打开数据库的同时取得 `kernel.lock`；只读命令用
  `OpenReadOnly`（不恢复、不加锁）。第二个写进程得到 `ErrLocked`，而不是在别人运行时改写状态。
- 回归：`TestOpenOwnsStateDir`、`TestReadOnlyOpenDoesNotRecover`。

## 2026-11-04 — K-D029：客户端与后台解耦
- `serve --daemon` 用 `Setsid` 脱离终端；`serve.json` 记录 pid/addr；`serve.log` 收日志。
- 子进程输出写文件而不是管道：父进程死亡时管道会以 SIGPIPE 杀死 worker。
- `serve --stop` 只停 HTTP 服务，不杀已托管任务；中止任务必须用 `goal cancel`。
- token 持久化到 `control-token`，重启后不变。

## 2026-11-04 — K-D030：取消是请求，不是写
- `goal cancel` 先在状态目录写 `cancel` 文件；持有目录的 scheduler 在下一个循环消费它，
  调 `Abort` 取消 AO 会话、阻塞未完成任务、把目标置为 `cancelled`。
- 没有 scheduler 时由 cancel 自己完成同样步骤。
- 已 accepted 的任务保持 accepted；completed/failed 的目标不会被改写成 cancelled。

## 2026-11-04 — K-D031：界面是服务的一部分，不是另一个应用
- L2 用 `cmd/kernel/ui.html` + `//go:embed` 单页，无框架、无构建、无 npm；`serve` 直接提供。
- 写操作仍走 `/api/objectives/{id}/{plan|confirm|run|cancel}` → `kernel goal ...` 子进程，Kernel 保持唯一写者。
- 按钮按状态显示而不是全部禁用：四个灰按钮会被读成“坏了”，也会诱导误点。
- 计划未确认时 detail 回退到最新 plan，并用 plan spec 合成 pending 节点做预览；
  `objective.planVersion` 只在确认后递增，不能用来查草案。
- `serve` 在 `kernel.db` 不存在时先建空库（只建 schema），否则只读连接会进入坏状态、
  页面在“还没有目标”时报 500。
- 取消的 job 状态由 `settleRun` 统一判定：目标已 cancelled 时不报 "run failed"。

## 2026-11-04 — K-D032：reviewer 的依赖清单必须是祖先闭包
- G2 真实失败：T3 依赖 T2，但 worktree 里有 T1 注入的 sales.csv；reviewer 只被告知直接依赖，
  连续两轮把 sales.csv 判为“未请求的额外文件”。
- 决定：reviewer 的依赖清单与 worker 提示词、验收 sandbox 一致，都用祖先闭包；
  同时把这些路径从 `Files changed by this run` 中剔除——注入的依赖是输入，不是本次产出。
- 回归：`TestReviewerSeesTransitiveDependencyFiles`。

## 2026-11-04 — K-D033：计划质量靠声明约束，不靠放宽验收
- 规划器提示词增加：产物中引用的文件必须由别的任务产出并声明依赖；创建共享文件的任务在前。
- 规划器优先 `python3 -m unittest`：pytest 不是处处安装，缺模块会让验收因环境失败而非代码失败。
- 真实复跑：G1 一次通过；G3 改为 unittest 后通过。
- 不放松 reviewer：两次真实阻断（CSV 列解析、stats.py 越权写文件）都保留。

## 2026-11-04 — K-D034：交付物整体验收
- 逐任务验收证明“每个文件单独合法”，不证明组装后可用；`out/` 从未被整体执行过。
- 决定：组装后把 `out/` 复制到临时目录，重跑所有 accepted 任务的 command 验收；
  任一失败则 objective 置 failed，不写 `.project` 快照。
- 用副本而不是原地：命令可能有写副作用（如 summarize.js 写 summary.md），不能改动交付物。
- 命令执行统一走 `verify.RunCommand`，与逐任务验收共用白名单与 PATH 逻辑。
- 只重跑 command；exact/contains 是单产物内容检查，在组装树上重复无意义。

## 2026-11-04 — K-D035：重试必须带着 reviewer 的具体结论
- 现象：review 阻断后 `tasks.last_error` 只有 "review: blocking finding (P0/P1)"，
  worker 第二次尝试只能猜哪里错了，等于把同一轮重新赌一次。
- 决定：`expandPrompt` 在 attempt>1 时读取最近一次 full review 的全部 findings，
  P0/P1 列为必须修复项，P2/P3 列为“建议顺手修”；单条截断 400 字符。
- 没有 findings（例如验收命令失败）时仍回退到 `last_error` 文本。
- 回归：`TestRetryPromptCarriesReviewerFindings`。

## 2026-11-04 — K-D036：一个任务一个产物
- 现象：计划可以声明多个 artifacts，`compile` 也会让 worker “Also produce”，
  但 Kernel 只跟踪/验收/组装第一个；reviewer 按第一个产物划定范围，把其余判成多余文件，连续阻断。
- 决定：`Plan.Validate` 拒绝多产物任务，要求拆成多个任务并用依赖串起来；
  删除 compile 里的 “Also produce” 指示；规划器提示词同步。
- `exact` 只用于目标写明的内容；生成的数值/文本用 contains/nonempty，避免规划器编造期望值。
- 复跑 CSV：T1→T2→T3，每任务一产物，全部 attempts=1，0 阻断，交付验收通过。

## 2026-11-04 — K-D037：全量审查后的边界收敛
- 允许列表改为解析 token，不再用前缀匹配；`npm run testevil`、`node -e`、`go test -exec` 一律拒绝。
- 未知 severity 一律按 P1 处理：reviewer 不能用一个不存在的级别把缺陷放过去。
- 交付是 fail-closed：组装或整体验收出错即判 failed，不再“记录一下继续 completed”。
- `serve` 默认只绑回环；读写分离不变（GET 无认证，因此不允许默认暴露到网络）。
- 接受风险并写清楚：命令验收不是安全沙箱，`scope/inputs/allowed/forbidden/acceptance/priority`
  不参与机器判定（见 `docs/plan-semantics.md`）。

## 2026-11-04 — K-D038：重规划必须单事务
- 之前：先建计划、再退役旧任务、再确认；任一步失败会留下“任务已退役、计划未生效”。
- 现在：`store.ApplyReplan` 在一次事务里建计划、退役、确认、切换 objective 版本；
  失败整体回滚。回归测试 `TestApplyReplanIsAtomic`。

## 2026-11-04 — K-D039：daemon 身份不靠 PID
- 启动时抢 `<state>/serve.lock`（flock，进程死即释放），一个状态目录只允许一个服务。
- `/api/status` 返回随机 `instance`，`serve.json` 记录同一个值；`--stop` 先查询再比对，
  不匹配就拒绝发信号（PID 复用不会误杀）。就绪探测也用它，避免把别的 HTTP 服务当成自己。

## 2026-11-05 — K-D040：验收是独立判断，不是 review 的同义词
- reviewer 回答“有没有缺陷”；acceptance 回答“是否真的完成任务”。两者会不一致，
  因此新增 `reviews.kind=acceptance` 与会话，顺序在 reviewer 通过之后。
- 同版本各限一次；失败同样进入重试与定向复审；`--acceptance=false` 可关闭（成本考量）。
- 未知 severity 仍按 P1；acceptance 不得发明任务里没有的要求。

## 2026-11-05 — K-D041：汇报人只描述，不判定
- 规则版由 Go 直接从状态生成，中文、免费、无延迟；模型版只改写文字并写 `report.md`。
- 模型汇报在有 scheduler 占用目录时拒绝执行：`aoenv.Setup` 会清理遗留会话，不能并发。
- 界面默认显示模型版（若有），可切回规则版；两者都标注来源与时间。

## 2026-11-05 — K-D042：界面先服务“看懂”，再服务“看全”
- 状态、动作、原因一律中文；英文状态词不上屏。
- 哈希、路径、尝试次数收进「技术细节」折叠；时间线只显示动作与短摘要。
- 跨列依赖从上方通道绕行；连线画在节点下层，不允许穿过方框。

## 2026-11-05 — K-D043：继续改进只追加，不重做
- 已完成/失败的目标可以生成下一个计划版本：已接受任务原样保留（模型改写会被还原、漏写会被补回），
  只允许新增 ≤5 个任务，新产物不得与已有产物重复。
- 新计划立即生效但不自动执行：人确认后再点执行，执行仍由现有 `goal run` 完成。
- `TasksForPlan` 改为按计划成员判定，而不是按任务的 plan_version：保留的已验收任务仍属于新计划。

## 2026-11-05 — K-D044：汇报与状态绑定
- 模型汇报写入时带状态哈希（目标状态 + 计划版本 + 每个任务的状态/尝试/产物）；
  哈希不匹配即视为没有汇报，界面会重新生成，避免继续改进后仍显示旧内容。
- 成果文件由 `delivery.List` 读取 `out/`，预览上限 400 字；二进制只列路径与大小。

## 2026-11-05 — K-D045：默认用 OpenCode CLI 执行，不再依赖 AO
- 原因：用户希望使用 OpenCode 里已注册的模型；AO 在真实 HOME 下无法拉起 harness
  （process_open 失败），而 `opencode run --standalone` 不抢桌面版的后台服务。
- 决定：新增 `internal/exec/opencodecli`（实现 exec.Backend），`--executor opencode` 设为默认；
  每任务一个 `<state>/work/...` 目录，从 `<state>/repo` 播种，产物与改动文件按哈希差异收集。
- planner / reviewer / acceptance / 汇报人 改为依赖 `exec.Backend` 接口，可与 worker 共用同一后端。
- 已知代价：没有 AO 的 chat-host 清理与会话复用；崩溃时 opencode 进程可能残留。

## 2026-11-05 — K-D046：不把宿主路径交给模型
- 真实失败：规划器把状态目录的绝对路径写进任务 scope，worker 老老实实写到宿主目录，
  产物不在任务工作目录里，Collect 失败。
- 决定：planner 请求不再传 ProjectPath；任务提示词改为“当前工作目录”，不再说“仓库根目录”。

## 2026-11-05 — K-D047：用 CLI 后端复现 AO 的关键能力
- 隔离：有 git 仓库时用 `git worktree add --detach`，否则目录拷贝（跳过 `.git`）。
- 生命周期：每任务一个进程组；取消先 `SIGTERM` 整组、5 秒后 `SIGKILL`。
- 崩溃清理：任务目录写 `.kernel-run.pid`；后端初始化时按进程 cwd（lsof）校验是本目录后再杀，
  避免 PID 复用误杀。
- 权限：worker 传 `--auto`；planner/reviewer/acceptance/reporter 不传，默认拒绝越权写。
- 未复现：多轮会话续跑（当前 Kernel 的一次任务 = 一次提示词，不需要）；任务目录不自动清理。

## 2026-11-05 — K-D048：第二轮审查的 10 项修复
- 权限：CLI 后端按 ApprovalMode 决定是否 `--auto`；planner 显式声明 default，只有 worker 自动批准。
- 验收：acceptance 不再依赖 reviewer 是否开启；budget 在 reviewer 与 acceptance 前各查一次。
- 重复版本：`ErrReviewLimit` 改为沿用已有结论，阻断不会被第二次判定洗白。
- 交付：最终文件也用 `O_NOFOLLOW` 打开，符号链接目标不会被覆盖。
- 用量：有产物文件时同样解析事件流，worker 的 token 不再漏计。
- 失败会话：reviewer/acceptance 的基础设施失败返回 error，未知用量触发停止调度。
- 隔离：quarantine 持久化重试 3 次；仍失败则记录，不再假装成功。
- 完成：状态写入或交付报告写失败即返回错误，不再打印 completed。
- 取消：先拿状态目录锁，拿到就在同一把锁内取消；拿不到才留 flag，消除释放/重抢窗口。

## 2026-11-05 — K-D049：按角色配置模型
- 五个角色（planner/worker/reviewer/acceptance/reporter）各自可设模型，存 `<state>/models.json`；
  reviewer/acceptance 未设置时跟随 worker。
- 解析顺序：命令行 flag > models.json > 内置默认（deepseek/deepseek-flash）。
- 模型列表由 `goal models --refresh` 调 `opencode --standalone models` 缓存到 `models-cache.json`；
  界面只读缓存，不在请求里跑 CLI。
- 写操作与其它写一样走 `kernel goal ...` 子进程；空值表示“跟随默认”。

## 2026-11-05 — K-D050：第三轮审查的 5 项修复
- planner 显式 `ApprovalMode: "default"`，规划/重规划/继续改进不再获得 `--auto`。
- 验收结论按 `kind='acceptance'` 读取（`AcceptanceVerdict`）；targeted review 不会覆盖此前阻断。
- quarantine 写库失败时写 `<state>/QUARANTINE` 文件兜底；启动时 `RestoreQuarantine` 恢复并拒绝运行，
  需显式 `--clear-quarantine`。
- 完成顺序调整为：组装/验收 → 交付报告 → 恢复快照 → 最后写目标状态；任一失败则不报完成，
  目标保持 active 可重跑。
- `goal run` 在拿到状态目录锁后清除陈旧 `cancel` 标记，避免误取消下一轮；取消只在调度器持锁期间有效。

## 2026-11-05 — K-D051：关键失败路径必须有注入测试
- 已覆盖：quarantine 写库失败（文件兜底）、快照写失败、交付报告写失败（目标保持 active）、
  SQLite 损坏（拒绝打开且不改文件）、陈旧取消标记、空闲取消（立即生效且无残留）。
- 未覆盖：取消与调度器完成的真实并发交错、磁盘满/只读挂载、运行中 SQLite 失效。

## 2026-11-05 — K-D052：P2 可用性（多目标 / 审批 / diff / 原生壳）
- 多目标不改 Kernel 的“一目录一目标”，在 Workspace 层用 `projects/<id>/` 组合；
  每个目标独立 DB/锁/work/out，保留单写者与交付语义，并允许并行。
- 审批是执行前门禁：高风险任务或 `command` 校验必须 `--approve-risk`；不是逐工具拦截，
  command 仍不是沙箱。预算与命令清单在驾驶舱弹窗中展示。
- diff 只做文本行级对比（最新尝试 vs 上次交付/上一尝试）；二进制只标记有变化。
- 会话续跑复用 provider session，但仍用新工作目录，提示词要求忽略旧路径；审查/验收始终新会话。
- work/ 默认保留最新 20 个尝试目录；`goal prune --keep N` 可调，运行中的目录跳过。
- token 读取 `step_finish.part.tokens` 实测值；旧格式/后端缺失时才用 chars/4 并标注估算。
- macOS 外壳用 Objective-C + WKWebView，不用 Electron；退出只停自己启动的 HTTP 客户端。

## 2026-11-08 — K-D053：默认模型切到 opencode-go/deepseek-v4.1-flash（临时）
- 用户要求“暂时把所有模型都默认为 opencode-go 里的 ds flash”；全部角色（planner/worker/
  reviewer/acceptance/reporter/chat）共用 `cmd/kernel/goal.go` 的 `defaultModel` 单点。
- 旧默认 `deepseek/deepseek-flash` 在当前 v2 provider 未注册，导致未配置 models.json 时计划失败。
- 回退方式：改 `defaultModel` 一处，或在工作区 `models.json` 按角色覆盖；不改 CLI 与 API 契约。
- 验证：新增 `TestDefaultModelAppliesToEveryRole`，逐角色断言回退值。
