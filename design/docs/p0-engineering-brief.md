# P0 工程启动与技术验证任务书

- 来源：用户在会话中提供的计划原文（2026-11-02）
- 性质：用户提供的权威任务输入，未经我改写
- 状态：已接收并归档；执行令尚未下达（本文件不代表已授权开工）
- 授权范围（原文）：仅 P0 调查、隔离原型和低风险测试；正式产品开发、修改既有仓库、付费模型调用、危险操作不在授权内

---

## ProjectOS 2.0 — P0 工程启动与技术验证任务

你已经了解 ProjectOS 2.0 的完整产品背景、功能目标、设计原则和候选架构。

现在开始实际工作。

你的身份是 ProjectOS 2.0 的技术负责人和核心开发工程师。请基于真实源码、现有接口和可执行测试，完成第一阶段工程验证，并确定后续开发路线。

本阶段的主要目标是：

**尽可能复用 Orchestrator 的成熟基础设施，验证 ProjectOS 通用任务内核的可行性，形成一套有证据支撑、可以直接进入正式开发的架构方案。**

请自主完成范围内的调查、分析、轻量原型和验证，不要仅输出概念性建议。

## 一、首先确认当前工程环境

检查当前操作系统、工作目录、Git 状态、工具链以及已安装的软件。

重点确认：

- OpenCode 的实际版本、CLI、Server 或 ACP 能力。
- Orchestrator 的安装情况及版本。
- DeepSeek Harness 的安装与可用性。
- Go、Node.js、Python、Git 等工具环境。
- 当前是否存在 ProjectOS 和 Orchestrator 的本地源码。

核心参考仓库：

- https://github.com/QiuweiLiu/research-agent-os
- https://github.com/OrchestratorInc/agent-orchestrator
- https://github.com/anomalyco/opencode

优先使用当前本机安装版本、实际源码、官方文档及接口定义。不要依赖过时的网上教程，也不要猜测 API。

如果已有本地仓库，优先检查现有代码和版本。

如果需要获取开源源码，可以在独立工作目录创建只读研究用的 Git 检出副本，不要修改已有项目或全局配置。

记录检查时的具体版本、Commit、操作系统和关键工具信息。

## 二、全面审计 Orchestrator，确定可复用模块

这是最重要的调查工作之一。

请分析 Orchestrator 的真实源码，重点研究：

### 1. 桌面客户端

- Electron / React 的实际架构。
- Chat 的消息流和状态管理。
- 多 Session 交互。
- 工具调用和权限展示。
- Worker 状态、项目界面、Diff 和 Review。
- 前后端通信协议。
- 前端扩展或复用的难度。

### 2. 后端 Runtime

- Go Daemon 的模块结构。
- Session Manager。
- Agent 进程生命周期。
- ACP / TUI 通信。
- Agent Adapter。
- Worktree 与工作空间管理。
- SQLite、事务和 Migration。
- 事件系统及 SSE。
- 执行终止、恢复、清理。
- 本地 API 的访问控制。
- 用户权限和工具授权。

### 3. 多 Agent 协作

- 协调 Agent 如何分配任务。
- Worker 如何创建及上报结果。
- 是否已有任务 DAG。
- 是否已有自动任务接续。
- 是否支持独立 Reviewer。
- 是否能控制 Review 次数。
- 是否有预算和并发限制。
- 是否可以脱离 GitHub PR 完成任务。
- 是否存在重复执行和状态竞争风险。

对于每项能力，明确记录：

- 代码路径。
- 关键接口或函数。
- 实际行为。
- 已有测试。
- 可复用程度。
- 扩展代价。
- 未验证的限制。

特别注意：区分已经实现的代码、官方文档中的功能、开发中的功能和未落地的设计提案。

## 三、审计现有 ProjectOS Core

阅读 `research-agent-os` 的真实文件，重点包括：

- `core/policies/CORE.md`
- `core/skills/`
- `adapters/opencode/`
- 已有状态管理脚本与测试
- 现有 Review、Continuity 和安全规则

将现有功能分类为：

1. 通用 Kernel 能力。
2. 通用 Skills / Policies。
3. Research Capability 专属能力。
4. 执行器 Adapter 专属能力。
5. 可以由 Orchestrator 替代的基础设施。
6. 需要改造或新增的能力。

必须保留：

- Harness 无关的 Core 原则。
- 已验证事实与 Agent 汇报的区分。
- 结构化任务契约。
- 长期记忆与上下文容量限制。
- 证据驱动的审查。
- P0/P1 严重问题阻断。
- 有界 Review。
- 严格的权限和资源边界。

重点分析 `.project/` 与 Orchestrator SQLite 的关系，以及多个 Worktree 下如何实现权威状态的单写入控制。

目前不要修改现有 ProjectOS 仓库。

## 四、确定 ProjectOS 最终的职责边界

基于源码审计，明确划分：

**ProjectOS Kernel 负责：**

- Objective / Task 管理。
- 任务依赖 DAG。
- 规划与调度决策。
- Quality Gate。
- 任务最终验收。
- 预算与权限策略。
- 长期记忆。
- 自动推进与异常恢复策略。

**Orchestrator Execution Infrastructure 负责：**

- Agent Session 创建与通信。
- 进程管理。
- Harness Adapter。
- Worktree 与工作空间。
- 消息与事件。
- 实际执行的停止和恢复。
- 执行结果回收。

需要重点回答：

- 两者如何通信？
- 谁拥有 Task 状态？
- 谁拥有 Session 生命周期？
- 谁负责重试？
- 谁判断验收成功？
- 如何避免重复调度？
- 如何在程序崩溃后恢复？
- 怎样保证未来替换执行后端时不重写 Kernel？

不要引入两个互相竞争的调度器。

## 五、验证 Orchestrator 的外部控制能力

优先调查并测试现有 API、CLI 或其他明确支持的扩展机制。

需要验证：

1. 创建 Agent Session。
2. 向 Session 提交任务。
3. 获取消息、状态和事件。
4. 判断 Worker 是否真正完成。
5. 获取任务产物及代码修改。
6. 取消正在执行的任务。
7. 关闭客户端后重新连接。
8. Daemon 重启后的状态恢复。
9. 多 Worker 工作区隔离。
10. 结构化结果回传。
11. 独立审查与审查反馈。
12. 不依赖远端 PR 的本地工作流程。

不得编造未核实的 API。

如果某项能力无法通过公开接口实现，定位真实源码，分析是否可以通过小范围扩展完成。

对能力作出明确分类：

- REUSE：直接复用。
- ADAPT：编写薄适配层。
- EXTEND：需要修改或扩展现有模块。
- BUILD：确实需要自行实现。
- UNKNOWN：证据不足。

## 六、实现一个隔离的最小验证原型

在独立的 `projectos-spike` 工作区中实现最小原型。

原型只需要证明一件事：

**ProjectOS 可以在独立于 Agent Session 的任务状态机控制下，自动执行多个有依赖的任务，并通过验证决定是否继续。**

采用如下测试场景：

```text
Objective
   |
   +-- Task A ──┐
   |            |
   +-- Task B ──┼── Task C
                |
          A、B 都通过验收
                |
                v
           Task C 执行
```

具体要求：

### 1. Task A 和 Task B

- 可以并发执行。
- 各自有独立 Run。
- 具有独立的输出与验证条件。
- 不应互相覆盖工作空间。

### 2. Task C

- 依赖 A、B。
- 只有 A、B 都 ACCEPTED 才能启动。
- 不能因为某个 Worker 自称完成就提前启动。

### 3. Quality Gate

为 A 或 B 设计一个明确的验证失败场景。

当验证失败时：

- Task 不能 ACCEPTED。
- 依赖它的 Task C 不能启动。
- 失败原因和证据必须记录。
- 系统执行预设的有限失败处理策略。

### 4. 生命周期与状态

最小原型需要记录：

- Task ID。
- Task Status。
- Dependencies。
- Run ID。
- Execution Backend。
- Execution Status。
- Result。
- Verification Evidence。
- Timestamp。

在可行的情况下，使用 SQLite 保存运行状态。

### 5. 执行后端

优先通过 Orchestrator 的真实能力运行测试。

如果真实 Agent 执行需要模型付费额度、额外认证或无法可靠隔离，则先使用 Fake Execution Backend 验证 Task Engine，并明确记录真实集成未完成。

Fake Backend 不得被当作真实 OpenCode 集成成功的证明。

不要因为测试需要而擅自启动高成本模型调用。

## 七、定义最小稳定接口契约

根据真实源码和原型结果，确定 ProjectOS Kernel 与执行层之间的最小接口。

至少考虑：

- StartTask
- ObserveRun
- CancelRun
- RecoverRun
- SubscribeEvents
- CollectArtifacts

以上是逻辑能力名称，不要求使用这些具体函数名。

接口需要明确：

- 输入参数。
- 返回数据。
- 错误语义。
- 超时与取消。
- 幂等性。
- Session 与 Run 的关联。
- 事件丢失后的恢复。
- 版本和兼容性要求。

同时确定 Objective、Task、Run、Artifact、Evidence、Review、Approval 的最小字段。

不要为尚未确定的复杂需求创建大量抽象接口。

## 八、特别关注效率和工程成熟度

架构评估不能只看功能数量，还需要考虑：

### 并发

- Worktree 隔离是否可靠。
- 共享资源是否需要锁。
- 是否可能产生语义冲突。
- 多 Worker 是否提高真实完成效率。

### Review

- 是否存在重复审查。
- 低风险任务能否自动验收。
- P0/P1 能否强制阻断。
- P2 能否非阻断。
- 是否能限制完整 Review 和定向复审次数。

### 长期运行

- Session 崩溃怎么办。
- Daemon 崩溃怎么办。
- Desktop 退出怎么办。
- 旧 Worker 的延迟结果怎么办。
- 旧执行进程未退出怎么办。
- 重启后如何避免重复执行。

### 状态文件

- SQLite 与 `.project/` 如何划分权威。
- 如何避免 Markdown 无限增长。
- 如何减少重复的项目上下文。
- 如何保存精简的恢复快照。
- 如何确保历史事实可追溯。

### 安全

- 本地 API 是否存在未授权调用风险。
- Agent 权限是否可以绕过。
- 是否有危险命令和敏感信息保护。
- 外部副作用是否需要人工审批。
- 本地测试如何避免影响真实项目。

## 九、比较三种最终实施路线

依据真实验证结果比较：

**方案 A：外部扩展**

Orchestrator 保持上游结构，ProjectOS Kernel 通过已有接口集成。

**方案 B：受控 Fork**

复用 Orchestrator 的 Go Daemon 和 Electron UI，将 ProjectOS Kernel 纳入其模块体系。

**方案 C：独立 Runtime**

ProjectOS 自己管理任务控制层，通过薄接口连接实际执行器。

对每条路线评估：

- 开发成本。
- 代码复用比例。
- 后续维护难度。
- 上游同步成本。
- 跨平台兼容性。
- 故障恢复复杂度。
- 扩展能力。
- 性能与协调开销。
- 长期架构风险。

给出一个明确的推荐路线，并解释不选择其他路线的具体原因。

目前倾向优先复用 Orchestrator，不能把这一倾向当作无需验证的结论。

## 十、交付文件

在独立工作区形成：

```text
projectos-spike/
├── README.md
├── reports/
│   ├── environment.md
│   ├── orchestrator-audit.md
│   ├── projectos-core-audit.md
│   ├── capability-matrix.md
│   ├── interface-contracts.md
│   ├── architecture-decision.md
│   ├── test-results.md
│   └── implementation-roadmap.md
├── prototype/
└── tests/
```

报告使用中文。

源码位置、版本、函数、API 和技术术语保留原文。

每一条重要技术结论都应有相应的源码、文档或测试证据。

如果未能执行某项验证，明确标记为 NOT TESTED 或 BLOCKED，不得标为 PASS。

### architecture-decision.md

这是最重要的交付物。

需要包含：

- 最终建议的分层架构。
- 具体复用的 Orchestrator 模块。
- ProjectOS 必须自研的模块。
- 是否需要 Fork。
- Task / Run / Session 的状态所有权。
- SQLite 与 `.project/` 的数据边界。
- 执行接口契约。
- Quality Gate 设计。
- 主要风险及缓解措施。
- 未来扩展能力包的方案。

### implementation-roadmap.md

规划后续正式开发。

按依赖关系而不是 UI 功能数量排序。

每个开发阶段包含：

- 要实现的功能。
- 复用的现有代码。
- 需要新增的模块。
- 完成标准。
- 自动化测试。
- 预估工作量。
- 重要风险。

优先让最小任务闭环稳定工作，再增加复杂功能与 UI。

## 十一、执行纪律

1. 先核实接口和代码，再决定架构。
2. 先复用现有实现，再考虑新增模块。
3. 所有结论都尽可能有测试证据。
4. 允许在独立工作区创建 PoC 和测试脚本。
5. 不修改现有 `research-agent-os`。
6. 不直接修改上游 Orchestrator 的原始工作副本。
7. 不改动全局 OpenCode / DSH 配置。
8. 不删除用户文件或重写 Git 历史。
9. 不执行未经授权的昂贵模型调用、实验或外部操作。
10. 不引入无必要的中间件和复杂基础设施。
11. 不擅自进入正式产品开发。
12. 不要为满足目录设计而创建大量空文件或占位模块。
13. 遇到不明确的接口，直接查源码或官方文档。
14. 在授权范围内自主推进，不要为普通只读检查和低风险本地验证反复请求确认。
15. 对无法安全完成的操作记录原因、前提和替代验证证据。

## 十二、完成标准

本阶段完成时，应该能够回答：

1. Orchestrator 实际提供了哪些可复用能力？
2. 怎样通过真实接口控制 OpenCode Worker？
3. 如何构建独立于 Session 的任务状态机？
4. Task DAG 能否可靠地自动推进？
5. 如何控制并发、审查和资源开销？
6. 如何保留现有 ProjectOS Core？
7. 如何让通用 Kernel 支持不同能力包？
8. 需要 Fork Orchestrator 吗？
9. 后续正式开发应该采用什么代码结构？
10. 第一个可靠可用版本应该包含哪些最小功能？

**最终必须交付：真实源码审计、能力复用矩阵、接口验证、最小任务原型、测试结果、明确架构决策和后续实施路线。**

请立即执行 P0 范围内的工作，而不只是编写规划建议。

最后给出简洁的总结，说明已完成内容、实际测试结果、推荐架构、主要风险及下一阶段的开发入口。

当前仅授权 P0 调查、隔离原型和低风险测试。正式产品开发、修改既有仓库、付费模型调用以及危险操作不在本次授权范围内。
