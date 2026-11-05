# ProjectOS 2.0 — 上游能力证据与路线建议

日期：2026-11-02（Asia/Shanghai）。状态：静态源码核验完成；以下路线为建议，尚未批准或运行验证。配套规格：mvp-acceptance.md。

## 1. 固定版本与核验方法

| 仓库 | 固定 commit | commit 日期（UTC） | 本轮方式 |
|---|---|---|---|
| OrchestratorInc/agent-orchestrator | `d2c997ba935783d2f7105e2f5c6804834fbea9df` | 2026-11-02 05:51:09 | GitHub 官方 connector 查询 main 的 commit，再按该 SHA 读取树与选定文件 |
| QiuweiLiu/research-agent-os | `b7ac8a9a8c355731158244509b3dffbba59fbb5c` | 2026-10-18 08:12:45 | 同样按 SHA 查询 Core、README、LICENSE 和 OpenCode 插件 |

所有下列链接固定到该 SHA，避免 main 漂移。Orchestrator 树查询未截断（6122 项）；这不等于审查了每个文件。所读源码保存在 .scratch/upstream/ 供本次审查，永久证据为本页的固定版本链接；临时目录不承担长期事实源。

证据等级：**S**=源码/静态配置存在；**T**=测试代码存在（未运行）；**I**=基于证据的推断/建议；**U**=未验证。没有运行上游、安装依赖、调用本机 daemon 或消耗 Harness 模型额度；不能把 S/T 写成实际可用。

## 2. 关键结论

1. **S：可复用会话执行基础，不应把展示状态当业务验收。** 上游 SessionStatus 是持久事实与 PR 事实的展示投影；本轮所查 SessionRecord 绑定执行器、活动、工作区和会话恢复身份。ProjectOS 的 Task ACCEPTED 和依赖接续需要独立契约。[展示状态](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/domain/status.go#L3)；[SessionRecord](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/domain/session.go#L263)。
2. **S：已有稳定投递键，不必新造一个上游 API。** 创建 Worker 使用 clientRequestId；重放检查 payload hash 和 committed 状态。Chat 消息提供 clientMessageId，经控制器进入 Chat Service。崩溃时的端到端无重复副作用仍为 U，不能从字段存在推导 exactly-once。[创建入口](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/sessions.go#L325)；[重放判定](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/service/session/service.go#L1912)；[Chat 消息契约](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/dto.go#L2790)。
3. **S：上游已有 OpenCode 2 与 DSH 的 Chat 接入，适配方式并不相同。** OpenCode 2 使用 `opencode acp`；DSH 使用 `dsh --profile acp`。两者在本机能否启动、授权、恢复仍为 U。[OpenCode 2 Chat](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/adapters/chatdriver/opencodev2acp/driver.go#L21)；[DSH Chat](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/adapters/chatdriver/deepseekharnessacp/driver.go#L20)。
4. **S/I：daemon 重启主要接管/核对已有执行；不可承诺任意缺失进程都会自动续跑。** 正常退出不销毁全部 Session；后台 reconcile 明确不启动缺失 Agent 或恢复 shutdown-saved workspace。ProjectOS 必须识别可重连、需明确恢复和不确定阻塞。[退出边界](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/daemon/daemon.go#L1317)；[后台核对限制](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/session_manager/manager.go#L4234)。

## 3. 能力证据表

| MVP 所需能力 | 上游证据与等级 | 可复用部分 / 本项目缺口 |
|---|---|---|
| 桌面与 daemon 分离 | S：[系统结构](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/docs/architecture.md#L84)；[前端清单](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/frontend/package.json#L1)；[Go 模块](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/go.mod#L1) | Electron/React 与 Go 基础存在；本轮不构建完整桌面，也未验证打包。版本以 commit 为准，不能只靠 package.json 的 0.13.0 识别发行版 |
| 项目与 Worker 创建/读取 | S：[API 前缀](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/api.go#L265)；[projects 路由](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/projects.go#L26)；[sessions 路由](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/sessions.go#L210) | `/api/v1` 下项目、Session 资源可作为候选边界；需实际 daemon wiring 与响应验证，单独 controller 可在依赖缺失时返回 501 |
| 创建幂等 | S：[SpawnSessionRequest](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/dto.go#L533)；[Spawn 重放](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/service/session/service.go#L353)；[冲突/未完成错误](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/service/session/service.go#L1867) | 同一 Worker clientRequestId 与 hash 重放；未完成创建返回 CLIENT_REQUEST_INCOMPLETE，不能换键盲重试。Kernel 仍须在请求前持久化 Run/键 |
| 顺序 Chat turn 与可查询历史 | S：[Chat 路由](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/conversations.go#L130)；[send 处理](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/conversations.go#L943)；[Chat Service.Send](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/service/chat/service.go#L1337) | 同 Session 顺序发 A/B，DTO 提供 clientMessageId。202 仅接收消息，不代表执行完成；turn 与 Run 的精确映射和静止条件需实测 |
| OpenCode 2 | S：[执行器身份](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/domain/harness.go#L11)；[TUI 消息确认限制](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/adapters/agent/opencodev2/opencodev2.go#L38)；[ACP 绑定](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/adapters/chatdriver/opencodev2acp/driver.go#L21) | harness 身份是 opencode-v2，不能与 opencode 混淆；TUI hook 不表示持久接收。首版倾向明确 Chat，拒绝静默回退 TUI |
| DeepSeek Harness | S：[headless 与 ACP 的区别](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/adapters/agent/deepseekharness/deepseekharness.go#L1)；[ACP 驱动](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/adapters/chatdriver/deepseekharnessacp/driver.go#L20) | headless 与 Chat 的模型/推理控制不同，不能把 headless 退出当任务验收；本机兼容性与授权均待测 |
| 工作区与产物 | S：[工作区元数据](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/domain/session.go#L167)；[产物与工作区路由](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/sessions.go#L223)；[受限桌面工作区定位响应](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/dto.go#L943) | 可读取产物与工作区视图，不能假定普通 SessionView 直接暴露绝对路径；最小 spike 必须核验受限路径/读取接口，冻结可复现产物后在可信验证环境测试 |
| SQLite 与状态持久化 | S：[SQLite 依赖](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/go.mod#L31)；[持久事实模型](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/domain/session.go#L263)；[CDC 结构](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/docs/architecture.md#L205) | 复用 Session 持久化，不直接写上游数据库。Kernel 自己的 Task/Run 状态与上游 Session 状态分属不同职责，不宣称已实现跨库原子性 |
| 事件与断线补偿 | S：[CDC 定义](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/cdc/event.go#L1)；[poller](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/cdc/poller.go#L84)；[SSE replay](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/events.go#L148) | 有 cursor/Last-Event-ID 回放；客户端持久化自己的 offset，重连时重新读取快照核对。事件流不是业务验收日志或无限历史保证 |
| daemon 重启、缺失进程 | S/T：[启动安全核对](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/daemon/daemon.go#L924)；[后台恢复接线](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/daemon/daemon.go#L1219)；[探测边界](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/session_manager/startup_health.go#L15) | 正常重启可核对并接管已有执行；缺失/身份不明不自动 replay task。不能将 manager 中存在 RestoreAll 方法当作默认启动路径已调用 |
| 严格会话恢复身份 | S：[native 恢复](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/session_manager/native_restore.go#L17)；[awake reconnect](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/session_manager/awake_reconnect.go#L11) | 已有 handle/launch/native identity/controller generation 的边界；Kernel 不自行重启同一执行进程。provider 中途失败后的实际恢复语义待测 |
| 代码 Review | S：[review 路由](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/reviews.go#L107)；[trigger](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/service/review/review.go#L484)；[PR 刷新边界](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/service/review/review.go#L547) | 所查触发链以 PR/branch/head 为中心；不能假定可直接作为无 PR 文件验收。首版确定性检查优先；本地独立 Review 需显式绑定冻结版本，不启动自动注入循环 |
| 通用 Objective/Task DAG/ACCEPTED | I/U：已查 domain/session.go、status.go、Session/Chat/Review 路由和完整文件树 | 本轮未找到能满足本规格的独立完整业务契约；保留为 ProjectOS 自身责任。这是本次调查范围内的缺口，不是“全仓绝无依赖管理”的断言 |
| 权限与审批 | S：[启动审批模式](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/dto.go#L554)；[会话审批路由](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/backend/internal/httpd/controllers/conversations.go#L134)；[listener 与信任边界](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/docs/architecture.md#L1046) | 执行器工具审批不等于目标授权；不得默认 auto/bypass。loopback 与 LAN 的信任边界不同，接入前核验 listener；不把本地 API 称为天然已认证 |
| Research Agent OS 规范与连续性 | S：[Core](https://github.com/QiuweiLiu/research-agent-os/blob/b7ac8a9a8c355731158244509b3dffbba59fbb5c/core/policies/CORE.md#L1)；[压缩注入](https://github.com/QiuweiLiu/research-agent-os/blob/b7ac8a9a8c355731158244509b3dffbba59fbb5c/adapters/opencode/plugins/continuity.ts#L8)；[写前守卫](https://github.com/QiuweiLiu/research-agent-os/blob/b7ac8a9a8c355731158244509b3dffbba59fbb5c/adapters/opencode/plugins/project-os-guard.js#L15) | 可复用规则/契约和精简状态；所查插件提供上下文注入与部分写入守卫，不是持久化 Task 调度器，不证明 Codex 已安装 JS hooks |

## 4. 许可事实与复用边界
Orchestrator 根 LICENSE 为 Apache-2.0，而同版本 frontend/package.json 的 license 字段仍为 MIT，记录为元数据差异；不能据单字段自行选择许可。Research Agent OS 根 LICENSE 为 MIT。未来若分发/Fork，需单独核对实际复用文件、依赖与 NOTICE/版权声明，不把本次元数据读取当完整许可审计。

来源：[Orchestrator 根 LICENSE](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/LICENSE#L1)；[frontend 元数据](https://github.com/OrchestratorInc/agent-orchestrator/blob/d2c997ba935783d2f7105e2f5c6804834fbea9df/frontend/package.json#L8)；[Research Agent OS LICENSE](https://github.com/QiuweiLiu/research-agent-os/blob/b7ac8a9a8c355731158244509b3dffbba59fbb5c/LICENSE#L1)。

## 5. 三条物理路线比较（推断，未批准）

| 路线 | 对本次 MVP 的意义 | 代价与待验证事项 |
|---|---|---|
| 外部小型 Kernel → daemon REST/Chat/SSE | 建议先做可行性验证；复用上游执行生命周期，保留独立 Task/Run/验收契约；不先改桌面或上游 | 两个进程但不同职责；发送/业务提交之间存在不确定窗口，靠稳定键、持久意图和状态核对处理；产物读取、消息映射、恢复必须实测 |
| 受控 Fork，在 Go daemon 中增加 ProjectOS 业务模块 | 若外部接口缺关键能力，可降低跨进程业务/执行协调成本，未来复用 UI | 要承担上游合并、迁移、测试与许可证核对；不能修改上游 Session 展示状态语义来冒充 Task 状态 |
| 独立 Runtime，自建 Session/进程/桌面 | 仅在上游无法满足关键边界时重新评估 | 与已存在的会话、工作区、恢复、驱动基础重叠最多；本次证据不支持直接走此路线 |

**当前建议：批准一个范围更小的外部接口 spike，而不是现在冻结最终架构。** 使用同一 Chat Worker 完成 A/B 只需证明“可创建、可关联 turn、可观测静止、可冻结读取、可稳定恢复”；其中一项不成立，就具体记录缺失接口，再判断最小上游补丁或受控 Fork。不能把静态 API 存在写成路线已可用。

Kernel 不直接导入上游 Go internal 包、不直写上游 SQLite、不直接启动/杀死 daemon 管理的 provider。经公开服务接口操作；若选 Fork，则在同一模块遵循其 ports/service/lifecycle 的职责边界。

## 6. 下一阶段 spike 的明确输出与停止条件
1. 用户确认一种 Harness、是否允许安装/运行专用上游环境、模型预算与专用工作区；不要触碰现有 OpenCode 配置。
2. 固定版本环境上验证显式 mode=chat、创建与重复 clientRequestId、冲突 payload、clientMessageId/turn 关联与审批阻塞。
3. 验证 A 结束/队列静止可识别；产物可读并冻结；B 仅在 A 验收后发送；用户暂停/取消可观察。
4. 验证响应丢失、ProjectOS 重启、daemon 重启和 Harness 断线的不同处理；不确定就安全阻塞，不用换键重发掩盖问题。
5. 形成各项通过/失败证据与阻断接口清单，再请求确认路线和最小实现计划。

进入 Fork/上游补丁、安装、真实模型调用、远程环境或变更验收定义前升级确认。技术验证不是正式科研实验；也不因 EXPERIMENT_GATE.json 存在就声称运行已获授权。

## 7. 本阶段验证记录
- 固定 SHA 的树与所需源码可读；关键路径由主代理核验，恢复/CDC 由只读 scout 核验。
- 本页是静态源码核验，未运行上游测试、应用、API 或 Harness。
- 两份交付文档的 M01–M09 编号、固定 SHA 链接及控制面一致性已检查；52 个 GitHub blob 引用只指向上述两个 SHA，42 个全文来源锚点检查行界，其余由带原始行号的恢复摘录复核。
- 单次独立 reviewer gate：PASS。控制面旧核验表述及暂停/取消恢复判据在同一 gate 内修正，并定点确认；没有未解决的 P0/P1/P2。控制面最终审计结果见 STATE/HANDOFF。
