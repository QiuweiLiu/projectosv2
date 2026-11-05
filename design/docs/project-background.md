# ProjectOS 2.0 — 完整项目背景与架构上下文

## 1. 项目概述

**项目名称：ProjectOS 2.0**

**项目定位：通用、长期运行、可验证、可扩展的自主多 Agent 工作系统。**

ProjectOS 2.0 的核心愿景是构建一个真正能够长期推进复杂工作的个人 AI 工作平台。

用户通过自然语言提出目标，系统理解当前项目状态与历史决策，将目标组织为具有依赖关系和验收标准的任务，调用一个或多个 AI Agent 执行工作，持续收集产物和证据，判断任务是否完成，并在满足条件时自动推进后续工作。

整个系统具有持久化状态、可靠的任务生命周期、并行执行能力、上下文连续性、质量门禁、资源管理与人工控制机制。

ProjectOS 希望解决的问题，是现有 AI 编程助手和多 Agent 工具在长期复杂任务中仍然存在的一些不足：

- 长期项目依赖聊天上下文，跨 Session 后容易丢失进度和重要决策。
- Agent 可以完成局部任务，但整个项目仍需要用户频繁指挥下一步。
- 多 Agent 并行容易产生重复劳动、文件冲突和上下文不一致。
- Agent 自己声称任务完成，缺乏可靠、独立的验收依据。
- Reviewer 容易对低价值问题反复审查，带来较高的 Token 和时间开销。
- 长期运行过程中可能出现重复执行、错误恢复、资源消耗失控等问题。
- 状态文件、日志与聊天记录不断膨胀，导致上下文和维护成本增加。
- 编程、科研、文档、数据等不同任务缺乏统一的长期任务管理机制。

ProjectOS 2.0 的目标是在复用成熟 Agent 执行基础设施的前提下，提供统一的长期工作管理能力。

它既可以服务于个人日常开发，也可以服务于复杂科研、数据分析、信息调研、文档处理以及其他需要持续推进的工作。

ProjectOS 不局限于科研，不绑定某个模型供应商、Agent Harness 或具体业务领域。

---

## 2. 项目发展背景

ProjectOS 2.0 起源于已有的 Research Agent OS 项目。

现有仓库：

https://github.com/QiuweiLiu/research-agent-os

Research Agent OS 已经形成一套面向长期科研与工程开发的 Agent 工作规范，包括：

- 跨 Harness 的 Core Policies
- 结构化任务契约
- 多 Agent 逻辑角色
- 可复现性与证据要求
- 独立代码审查规范
- 上下文连续性
- 项目状态持久化
- 科研实验门禁
- 资源预算和安全约束
- OpenCode Adapter 与相关插件

ProjectOS 2.0 是对这套系统的产品化、通用化和运行时能力升级。

原有 Research Agent OS 更侧重规范 Agent 如何工作、如何保存科研状态以及如何审查结果。

ProjectOS 2.0 在此基础上增加真正的软件系统能力，包括：

- 独立的任务领域模型
- 持久化任务调度
- 自主任务接续
- 多 Agent 生命周期管理
- 任务依赖与并发控制
- 质量验收状态机
- 统一桌面交互
- 资源与成本调度
- 故障恢复
- 通用 Capability Packs

原有 Research Agent OS 的科研优势需要完整保留，同时将其中具有普适性的能力提升为通用系统核心。

科研专用规则则可以作为 Research Capability Pack 存在。

### 2.1 已有项目的主要规范

现有 Core 定义了如下工作流：

`inspect → plan → execute → verify → review → persist → handoff`

强调：

- 行动前读取真实项目文件与接口。
- 明确任务目标、范围、输入、限制和验收标准。
- 在已授权范围内自主执行。
- 验证实际产物，而非仅依据 Agent 的主观汇报。
- 对重要变更实施独立审查。
- 将经过验证的状态持久化。
- 保证跨 Session 的连续性与可恢复性。

现有项目具备五种逻辑角色：

1. `research-lead`：协调目标、规划任务、整合结果。
2. `scout`：只读调查、定位事实、收集证据。
3. `runner`：实际执行代码、工具或其他任务。
4. `reviewer`：独立审查正确性与风险。
5. `auditor`：核验数据、实验、配置、指标与证据一致性。

这些是逻辑职责，并不意味着所有任务都必须启动五个独立 Agent。

角色与模型解耦，可以使用不同的模型、推理强度和 Harness 承担不同工作。

### 2.2 现有长期状态文件

长期项目使用 `.project/` 保存权威状态：

```text
.project/
├── PROJECT.md
├── STATE.md
├── PLAN.md
├── DECISIONS.md
├── HANDOFF.md
├── EXPERIMENT_GATE.json
└── archive/
```

各文件职责：

- `PROJECT.md`：项目目标、范围、约束和主要架构。
- `STATE.md`：当前真实状态、已验证成果、问题和风险。
- `PLAN.md`：当前有效执行计划。
- `DECISIONS.md`：重要决策及其依据。
- `HANDOFF.md`：最小化的跨会话恢复快照。
- `EXPERIMENT_GATE.json`：科研实验的正式执行授权。
- `archive/`：被替代的旧状态、计划和历史资料。

现有 Core 对文件大小有明确约束：

- `PROJECT.md`：不超过 6KB。
- `STATE.md`：不超过 8KB。
- `PLAN.md`：不超过 6KB。
- `HANDOFF.md`：不超过 4KB。
- 启动时的主要上下文包不超过 20KB。

这些约束反映了 ProjectOS 一项重要的设计思想：

**长期记忆应当精简、权威、结构化，并且能够低成本恢复，而不应无限累积聊天和总结。**

通用化以后，`.project/` 可以继续承担项目级长期事实管理。

科研实验门禁属于可选领域能力，普通软件、文档或数据项目不需要强制创建科研专属文件。

---

## 3. 最终产品定位

ProjectOS 2.0 希望成为一个用户日常可以直接使用的桌面应用。

用户不需要分别打开 Agent 编程工具、任务管理软件、进度 Dashboard 和审查平台。

主要功能统一在 ProjectOS Desktop 中。

### 3.1 主要使用场景

**软件开发**

例如开发一个 Android 应用。

系统可以组织需求分析、架构设计、代码实现、测试、审查、打包和交付，并维护功能之间的依赖关系。

**科研工作**

例如实现 GPU 调度算法或工业视觉检测系统。

系统可以组织资料研究、算法实现、实验验证、结果分析、审查和科研状态更新，并通过专用能力包执行实验门禁。

**数据处理**

例如处理 CSV、Excel 或实验数据。

系统可以安排数据清洗、统计、绘图、验证和报告生成。

**信息调研**

例如比较一批技术项目或研究方向。

系统可以并行搜集信息、阅读材料、验证来源、整理证据并形成报告。

**文档处理**

例如撰写技术文档、研究计划、项目申请书或分析报告。

系统可以完成资料整理、提纲、起草、修改、引用检查与成果交付。

**个人项目**

例如长期维护网站、应用、知识库或自动化工具。

系统可以记录待办、主动执行已授权任务、验证结果并持续维护项目。

**自动化工作**

例如周期性状态检查、项目维护或外部系统操作。

系统可以按时间或事件触发任务，并通过权限与审批机制控制外部副作用。

以上是目标使用场景，不意味着每种能力在初始版本中都已具备。

### 3.2 用户体验

用户可以在 ProjectOS Desktop 中：

- 创建、切换与管理长期项目。
- 与 AI 直接聊天。
- 使用 OpenCode、DSH 或其他可用执行引擎。
- 提交普通单次任务。
- 将复杂目标托管给 ProjectOS。
- 查看任务规划和依赖关系。
- 观察多个 Agent 的实时进度。
- 查看代码、文件及其他任务产物。
- 查看自动测试与独立审查意见。
- 处理权限与重要操作审批。
- 暂停、恢复或取消任务。
- 查询历史决策与项目状态。
- 查看 Token、时间、资源和费用消耗。
- 在关闭应用后重新打开，恢复原有项目状态。

ProjectOS 应支持普通聊天模式和长期托管模式。

普通聊天模式可以直接使用底层 Agent 的原有能力，不强制创建任务 DAG 或进行多 Agent 编排。

托管模式会将用户确认的目标纳入持久化任务系统，由 ProjectOS 管理后续工作。

两种模式可以共存，但任务所有权和授权范围必须清晰。

---

## 4. 核心设计理念

### 4.1 目标驱动

系统最重要的对象是用户目标及其可验证成果。

Agent Session 只是执行资源。

一个 Session 可以结束、失败、恢复或被替换，但用户的目标、任务、证据、决策和验收状态必须继续存在。

### 4.2 通用性

ProjectOS Kernel 与具体工作领域解耦。

任务调度、生命周期、状态持久化、质量门禁、资源管理等属于通用能力。

Coding、Research、Data、Documents、Web、Automation 等属于可选领域能力。

### 4.3 可靠性

关键任务状态必须由确定性程序控制。

LLM 可以提出计划、执行任务、分析失败和建议修改，但不能绕开状态机、验收规则或权限系统。

### 4.4 自主性

用户确认目标与授权边界后，系统尽可能自主推进。

任务完成后可以解锁后续任务，遇到可处理的问题可以进行有界修复。

遇到不可解决的阻塞、超预算、重要风险或超出授权范围的行为时，转交用户决策。

### 4.5 高效率

系统以最终可验收成果的完成时间、质量与成本为优化目标。

尽量避免：

- 不必要的模型调用。
- 过度拆分任务。
- 过多协调 Agent。
- 不必要的多轮 Review。
- 重复阅读相同上下文。
- Agent 之间的重复劳动。
- 无收益的并发。
- 大量状态文件读写。
- 不断增长的运行日志。

### 4.6 可扩展性

执行器、能力包、验证器和模型配置可以按需扩展。

ProjectOS Core 不绑定 OpenCode、DeepSeek、Codex 或特定模型。

### 4.7 单一权威状态

每种状态都有唯一明确的权威来源和写入责任。

避免多个 Agent、多个 Runtime 或多个临时工作目录同时修改同一份权威项目状态。

### 4.8 复用成熟基础设施

优先复用经过验证的开源实现，避免重新构建模型 Provider、Agent Loop、终端模拟器、复杂会话系统和已有的跨平台基础设施。

新增代码集中在 ProjectOS 的差异化能力上。

---

## 5. Orchestrator.inc 的角色

重要参考项目：

https://github.com/OrchestratorInc/agent-orchestrator

Orchestrator.inc 是一个面向 AI 编程 Agent 的开源编排平台，采用 Apache License 2.0。

其已公开的架构包括：

- Electron + React 桌面应用
- Go Daemon
- SQLite 持久化
- REST API 与事件同步
- Agent Session 管理
- 终端与结构化 Chat
- ACP 等 Agent 通信方式
- Git Worktree 隔离
- 多 Worker 并行
- Agent 生命周期管理
- 项目协调 Agent
- 代码 Diff
- PR、CI 与 Review 工作流
- CLI 与进度监控
- OpenCode、DeepSeek Harness 等执行引擎适配

上述能力的具体可用范围取决于上游版本、Agent Adapter 和实际配置。

Orchestrator 是 ProjectOS 2.0 的主要执行基础设施候选。

它提供了大量原本需要 ProjectOS 自行开发的能力。

### 5.1 值得复用的部分

- Desktop 桌面交互基础。
- 原生聊天和工具调用展示。
- Agent Adapter。
- OpenCode / DSH Session 接入。
- Go Daemon。
- PTY / ConPTY。
- Git Worktree。
- SQLite 数据库基础设施。
- 事件流和实时状态更新。
- Session 进程管理。
- 中断与恢复机制。
- 代码审查和 CI 集成。
- 跨平台安装与运行机制。

### 5.2 ProjectOS 需要保留的独立价值

Orchestrator 主要关注 Agent Session、工作区、编码任务及代码交付流程。

ProjectOS 则关注长期目标、任务依赖、严格验收、跨任务状态、通用能力与自主推进。

关键差异包括：

- Objective / Task / Run 独立领域模型。
- 持久化且可强制执行的任务依赖 DAG。
- 独立于 Session 状态的任务验收。
- 适用于多种工作类型的 Quality Gate。
- 有界 Review 与自动修复。
- 长期项目记忆。
- 统一的资源与预算策略。
- 不依赖 PR 的本地工作流程。
- 通用 Capability Packs。
- 完整的任务与证据可追踪性。
- 更严格的端到端任务完成度度量。

ProjectOS 最终可以借鉴或 Fork Orchestrator 的现有代码，但不应让通用 Kernel 的领域模型依赖上游某个具体 Agent 或聊天协议。

技术依赖应通过清晰接口隔离。

---

## 6. 总体技术架构

目标架构由五层组成。

### Layer 1：Desktop Interaction

跨平台桌面交互层。

候选技术基础是 Orchestrator 的 Electron + React 客户端。

主要界面包括：

- Chat
- Projects
- Objectives
- Task Graph
- Workers
- Artifacts
- Reviews
- Decisions / Approvals
- Activity
- Settings

用户在此完成聊天、任务管理和监督。

### Layer 2：ProjectOS Kernel

整个产品的核心业务层。

负责：

- Objective 管理
- 任务规划与依赖 DAG
- 生命周期与状态机
- 调度决策
- 验收规则
- Quality Gate
- 审批与权限
- 预算与资源策略
- 长期项目状态
- 失败恢复策略
- 任务事件和审计记录

Kernel 对具体执行引擎保持独立。

### Layer 3：Capability Packs

按需加载的领域能力模块。

可能包括：

- Coding
- Research
- Data
- Documents
- Web
- Automation

能力包可以定义领域 Skills、Tools、Validators 和 Policies。

不同能力包可以组合使用。

### Layer 4：Execution Infrastructure

实际 Agent 执行与生命周期基础设施。

主要考虑复用 Orchestrator 的 Go Daemon、Session Manager、Adapter、工作区管理、事件机制。

它负责启动、观察、取消和恢复实际执行。

### Layer 5：Harness and Tools

具体执行引擎和工具环境。

首批重点包括：

- OpenCode 2
- DeepSeek Harness

未来可以支持：

- Codex
- Claude Code
- 其他提供兼容执行接口的 Agent
- 浏览器或外部服务执行器
- 本地命令、文件、数据及其他工具

能力支持程度以实际接口为准。

### 6.1 服务形态

ProjectOS 主要面向单机、单用户使用。

Desktop 与本地服务解耦。

关闭 Desktop 不应导致已托管任务的状态丢失。

在后台服务和执行进程仍存活时，已授权任务可以持续运行。

设备关机、休眠或执行器断线后，系统能够在恢复时核对已有状态。

当前倾向复用 Orchestrator 的 Go Daemon，并将 ProjectOS 的业务内核与执行基础设施集成。

外部 Kernel + Orchestrator API、受控 Fork 和独立 Runtime 都属于可行性待验证的技术路线。

最终需要避免两个组件同时拥有同一 Worker 的执行生命周期控制权。

---

## 7. 核心领域模型

ProjectOS 应以稳定、可验证的领域对象表达长期工作。

### Objective

代表用户希望达成的长期目标。

包含：

- ID
- 所属 Project
- 目标描述
- 范围
- 约束
- 预期成果
- 完成条件
- 批准的预算
- 当前状态
- 版本与时间戳

Objective 可以对应多个 Task。

### Task

一个有明确输入、范围、依赖和验收标准的工作单元。

包含：

- ID
- Objective ID
- Goal
- Scope
- Inputs
- Dependencies
- Acceptance Criteria
- Risk Level
- Priority
- Resource Requirements
- Allowed / Forbidden Actions
- Status
- Version

Task 之间构成有向无环依赖图。

### Run

Task 的一次具体执行尝试。

包含：

- Run ID
- Task ID
- Attempt
- Harness
- Agent / Model
- Session ID
- Workspace
- Start / End
- Execution Status
- Resource Usage
- Result References

一个 Task 可以有多次 Run。

重试不能覆盖之前的执行记录。

### Artifact

任务产生的实际成果。

例如：

- 代码
- Commit
- 文档
- 图片
- 数据集
- 分析结果
- 报告
- 可执行程序

Artifact 需要明确版本、归属、路径或引用。

### Evidence

证明任务结果符合要求的材料。

例如：

- 测试输出
- 代码检查结果
- 数据校验
- 来源引用
- 运行日志
- 实验配置
- 文件校验和
- 审查结论

Evidence 与具体 Artifact 或 Run 版本绑定。

### Verification / Review

Quality Gate 的执行记录。

包含检查类型、检查目标、结果、证据、缺陷、风险等级以及对应的产物版本。

### Decision / Approval

表示重要的用户决策、系统决策或授权。

包括决策依据、影响范围、批准内容、时间和相关任务。

审批应该绑定具体操作范围，不能笼统授予无限权限。

---

## 8. 任务生命周期

ProjectOS 使用由确定性程序控制的任务状态机。

核心状态可以包括：

```text
PENDING
READY
RUNNING
VERIFYING
REVIEWING
ACCEPTED
BLOCKED
FAILED
CANCELLED
WAITING_APPROVAL
```

典型执行过程：

```text
Objective
   ↓
Task Planning
   ↓
Dependency Resolution
   ↓
READY
   ↓
Worker Execution
   ↓
Artifact Collection
   ↓
Verification
   ↓
Risk-based Review
   ↓
ACCEPTED
   ↓
Unlock Dependent Tasks
```

状态语义：

- PENDING：存在尚未满足的前置条件。
- READY：依赖满足，可以参与调度。
- RUNNING：已有实际执行 Run。
- VERIFYING：验证产物和测试。
- REVIEWING：进行必要的独立审查。
- ACCEPTED：任务满足验收条件。
- BLOCKED：当前存在无法自主解决的阻塞。
- FAILED：任务达到失败判定条件。
- CANCELLED：用户或策略终止任务。
- WAITING_APPROVAL：执行前需要授权。

Agent 自己报告任务完成，不等于 Task 被验收。

只有满足 Quality Gate 的 Task 才能进入 ACCEPTED。

任务接续由依赖关系和状态机控制。

---

## 9. Planning 与 Scheduling

ProjectOS 区分任务规划与执行调度。

### Planning

主要负责：

- 理解用户目标。
- 分析项目现有状态。
- 明确工作边界。
- 拆分任务。
- 定义依赖。
- 定义验收标准。
- 识别风险。
- 判断哪些工作适合并行。
- 必要时调整计划。

Planning 可以按需调用 LLM。

但不应为了常规状态转换反复进行大模型推理。

### Scheduling

负责：

- 发现 READY 任务。
- 判断资源是否可用。
- 控制并发。
- 选择执行器和 Worker。
- 处理优先级。
- 管理资源锁。
- 对失败任务应用重试策略。
- 避免重复执行。
- 对关键路径进行适度优化。

Scheduler 优先采用确定性算法。

调度目标是：

**在满足质量、预算和授权要求的前提下，尽可能缩短最终可验收成果的完成时间。**

调度需要考虑：

- 任务依赖。
- 优先级。
- 预期耗时。
- 并行收益。
- 模型调用成本。
- Worker 资源占用。
- 共享文件冲突。
- CPU / GPU 资源约束。
- Review 开销。
- 工作区集成成本。

并发数量是可配置的，不追求无上限并发。

---

## 10. 多 Agent 协作模型

ProjectOS 使用角色分工与任务契约实现 Agent 协作。

现有五种角色可以继续保留为逻辑角色，不要求为每个角色配置独立常驻进程。

一个任务可以使用单一 Worker 完成。

复杂任务可以使用 Coordinator、Scout、Runner、Reviewer、Auditor 的必要组合。

协作过程中的核心要求：

- Worker 只执行被分配的任务范围。
- Task 输入和限制明确。
- 角色和模型可以动态配置。
- 多 Agent 尽量使用独立工作空间。
- Worker 不直接修改任务的最终验收状态。
- Worker 不竞争写入长期权威状态。
- 任务结果以结构化形式返回。
- 重大冲突通过明确的升级机制处理。
- 协作不应依赖无限增长的群聊历史。

对于代码任务，Git Worktree 是主要隔离方式。

对于非代码任务，可以使用独立的临时目录、产物命名空间或适合该领域的隔离机制。

工作区隔离解决并发写入冲突，最终成果仍需要合并与集成验证。

---

## 11. Quality Gate：通用质量控制

Quality Gate 是 ProjectOS 的核心特色之一。

它适用于代码、数据、文档、调研和科研等多种任务。

系统根据任务类型、风险等级和验收标准选择检查方式。

### 11.1 验证机制

优先执行确定性检查，例如：

- 单元测试。
- 静态检查。
- 文件格式检查。
- 数据完整性检查。
- Schema 验证。
- 产物存在性与版本检查。
- 输出结构检查。
- 可复现性检查。

如果确定性检查不足以覆盖关键风险，再进行独立 LLM Review。

### 11.2 风险等级

任务可以划分为：

- LOW
- MEDIUM
- HIGH
- CRITICAL

风险决定验证深度、独立审查要求和是否需要人工审批。

### 11.3 缺陷等级

沿用已有 ProjectOS Code Reviewer 的理念：

- P0：严重问题，可能破坏关键结果、数据或安全。
- P1：影响核心正确性或可信度的问题。
- P2：低影响问题、建议和可维护性改进。
- P3：纯格式、美观或偏好性建议。

P0/P1 默认阻断。

P2 一般不阻断。

P3 不应成为反复审查的原因。

### 11.4 有界审查

默认：

- 最多一次完整独立 Review。
- 最多一次针对阻断缺陷的定向修复复审。
- 复审后仍有阻断缺陷则进入 BLOCKED 或升级处理。
- 不因为 Reviewer 的风格偏好重复启动审查。
- 不对同一个版本重复运行等价审查。
- Review 结果必须绑定具体产物版本。

Review 本身也受到时间、Token 和资源预算约束。

### 11.5 通用性

不同工作可以拥有不同的 Quality Gate：

Coding：测试、Lint、行为审查。

Data：数据完整性、计算验证、统计复核。

Documents：结构、完整性、引用及要求匹配。

Web Research：来源有效性、事实交叉检查。

Research：实验设计、数据泄漏、指标和结论复核。

部分主观成果最终可能需要用户验收。

---

## 12. 长期记忆与状态管理

长期记忆是 ProjectOS 与普通聊天助手的重要区别。

系统需要稳定保存：

- 项目目标。
- 当前进度。
- 已经验证的成果。
- 重要决策。
- 被否定的方案。
- 正在执行的任务。
- 未解决的问题。
- 下一步工作。
- 相关证据的位置。

### 12.1 分层存储

**长期项目事实**

保存在 `.project/` 等权威项目状态中。

**运行状态**

保存在 SQLite 等事务型存储中。

**详细执行日志与产物**

保存在 Artifact Store 或独立日志目录中。

**临时 Agent 上下文**

由 Harness 的 Session 管理。

不同层次有不同的保留策略和权威边界。

### 12.2 防止上下文膨胀

必须避免：

- 每次任务都增加一个新的总结文件。
- 重复复制相同项目背景。
- 将所有执行日志纳入启动上下文。
- 为同一状态维护多个权威副本。
- 不断创建新的最终计划文件。
- 跨 Session 无限制传递原始聊天历史。

长期状态更新应以必要的里程碑和已验证变化为单位。

历史记录应可追溯，但无需全部进入模型上下文。

### 12.3 并行状态管理

允许多个 Worker 在受管理的临时工作空间执行。

项目级长期状态必须具有单一权威来源。

Worker 可以提交状态更新建议、证据和产物。

State Manager 根据版本与验证结果协调最终写入。

科研、编码和通用文件项目均遵循这一原则。

---

## 13. Capability Packs

ProjectOS 的通用能力通过可组合的 Capability Packs 扩展。

一个 Capability Pack 可以包含：

```text
capability/
├── manifest
├── skills/
├── tools/
├── validators/
└── policies/
```

这是概念结构，具体目录与 Manifest 协议尚未冻结。

### Coding

代码实现、测试、重构、Diff、代码审查和构建。

### Research

研究规划、文献分析、科学证据、实验验证和可复现性。

### Data

数据清洗、统计、分析、可视化、数据质量检查。

### Documents

文档生成、格式验证、引用、内容审阅和导出。

### Web

信息检索、网页交互、来源验证和信息提取。

### Automation

定时任务、事件触发、外部系统操作和状态检查。

能力包可以组合。

例如机器学习科研项目可以同时启用 Coding、Data 和 Research。

能力包不得绕开 Kernel 的权限、预算和生命周期规则。

初期能力包系统保持轻量，不需要插件市场或复杂的动态依赖管理。

---

## 14. Desktop 产品体验

ProjectOS Desktop 是主要用户入口。

优先支持 macOS，同时兼容 Windows 和 Linux。

期望具备以下界面。

### 14.1 Chat

普通聊天、任务托管、工具调用、流式输出和模型选择。

### 14.2 Project Explorer

查看项目列表、目标、近期活动、状态和重要事项。

### 14.3 Task Graph

显示任务依赖、当前状态、关键路径和阻塞原因。

### 14.4 Workers

显示运行中的 Agent、所属任务、执行引擎、工作空间与运行状态。

### 14.5 Artifact / Diff

查看代码修改、文件、数据、报告及其他产物。

### 14.6 Quality / Review

展示自动检查、Reviewer 发现、修复状态和验收结论。

### 14.7 Approvals

统一展示待用户处理的权限、预算、外部操作和关键决策。

### 14.8 Activity / Cost

展示执行时间、Token、模型使用、失败重试和相关资源消耗。

界面应该支持从项目级概览逐步进入具体任务和 Worker。

### 14.9 进度真实性

任务进度应基于验收状态。

例如：

- 已验收 Task 数。
- 正在执行 Task 数。
- 等待依赖的 Task 数。
- 阻塞 Task 数。
- 待审批操作数。
- 当前资源使用情况。

对于科研探索等难以量化的工作，不强制生成虚假的精确完成百分比。

---

## 15. 资源管理与效率

ProjectOS 的并发设计以效率为核心。

系统需要分别管理：

- Agent Worker 并发。
- Reviewer 并发。
- 本地 CPU 资源。
- GPU 资源。
- 模型 API 预算。
- 运行时间预算。
- 外部工具执行配额。
- 高成本操作授权。

某些任务适合并行，例如独立模块的代码实现或不同资料的检索。

某些任务需要串行，例如依赖前一步输出的集成与验证。

某些任务必须独占资源，例如特定 GPU 实验、同一数据库写入或不可并发操作的外部系统。

资源锁与调度策略应当显式定义。

系统也需要记录协调开销。

衡量多 Agent 是否有效，不仅取决于 Worker 数量，还取决于：

- 端到端完成时间。
- 经过验收的成果数量。
- 总 Token 使用。
- 修复和 Review 轮次。
- 任务重试率。
- 人工干预次数。
- 集成失败率。
- 资源利用率。

---

## 16. 可靠性、持久化与恢复

ProjectOS 面向持续运行，可靠性是基础要求。

重要设计包括：

- 任务状态持久化。
- Run 与 Task 分离。
- 有限重试策略。
- 请求幂等性。
- 数据库事务。
- 版本号或 CAS 更新。
- Worker 租约。
- 过期 Run 的 fencing。
- 执行状态重建。
- 进程清理。
- 资源锁恢复。
- 日志容量限制。
- Schema Migration。
- 数据库备份与恢复。
- 事件断线后的状态核对。

不能因为没有收到消息，就假定 Worker 已经死亡。

不能因为进程结束，就假定任务成功。

不能因为 SSE 重新连接，就假定所有历史事件都已经完整接收。

不能因为任务超时，就立即重复执行可能存在外部副作用的操作。

恢复流程需要结合持久化状态、实际进程、Session 信息和产物进行核对。

---

## 17. 权限与安全

ProjectOS 允许 Agent 操作实际项目文件和工具，因此需要明确权限边界。

主要原则：

- 默认只允许在授权工作区执行。
- 区分只读、普通修改和危险操作。
- 不主动读取或泄露凭据。
- 保护 API Key、Token、私钥与敏感配置。
- 高风险外部操作需要明确授权。
- 重要删除、覆盖和不可逆操作需要单独处理。
- 不允许通过 Planner 或 Reviewer 绕开权限。
- 不依赖纯 Prompt 作为唯一安全边界。
- 本地 API 需要访问控制。
- 审批绑定操作与范围。
- 执行日志与任务上下文需要脱敏。
- 来自网页、仓库或外部文件的指令内容视为不可信输入。

对于科研项目，正式实验、关键指标变更或结果覆盖可以由 Research Capability 提供额外保护。

对于其他领域，可以使用对应的权限与风险规则。

---

## 18. 现有资产与最终技术选择的关系

现有 Research Agent OS 是 ProjectOS 的规范与领域知识基础。

Orchestrator 是候选执行基础设施与桌面产品基础。

OpenCode / DSH 是具体执行引擎。

ProjectOS Kernel 是未来的核心差异化实现。

它们的关系可以概括为：

```text
ProjectOS Desktop
    |
    v
ProjectOS Kernel
    |
    +-- Objective / Task DAG
    +-- Planning / Scheduling
    +-- Quality Gate
    +-- Memory / Decisions
    +-- Budget / Approvals
    +-- Recovery
    |
    v
Execution Infrastructure
    |
    +-- Orchestrator Session Management
    +-- Worktree / Workspace Management
    +-- Agent Adapters
    +-- Events / Process Lifecycle
    |
    v
OpenCode / DSH / Other Harnesses

Cross-cutting:
    Capability Packs
    ProjectOS Core Policies
    SQLite
    .project/
    Artifact Store
```

这是一份目标逻辑架构。

Orchestrator 外部扩展、受控 Fork、统一 Go Daemon 或独立 Kernel 的最终物理实现方式尚未完全确定。

原则上应优先减少重复 Runtime 和双重控制权问题，同时保留 ProjectOS Kernel 的独立领域契约。

---

## 19. 工程组织与长期维护

ProjectOS 是长期维护的工程项目。

重要工程原则：

- 尽量复用现有代码。
- 保持清晰的模块边界。
- 通用 Core 不引用特定领域或 Harness 的实现细节。
- 执行器适配逻辑与任务策略分离。
- 数据模型具有明确的版本与迁移策略。
- 有稳定的 API 契约。
- 每个关键模块有自动化测试。
- 涉及并发、恢复和权限的功能有集成测试。
- 上游依赖需要版本锁定与兼容性验证。
- 尽可能保留上游开源项目的原有模块划分。
- 避免为了架构美观而大规模重构。
- 保留必要的原始许可证和版权信息。
- 初始部署优先单机、单用户、低依赖。
- 不预先引入 Redis、向量数据库、分布式任务队列或 Kubernetes。
- 不重复开发现有成熟的模型 API 调用框架。
- 不为普通状态更新频繁调用 LLM。

成熟度取决于真实使用中的可靠性、可维护性和任务成果质量。

---

## 20. ProjectOS 的长期愿景

ProjectOS 希望逐步成为一个独立、稳定、强大、可扩展的个人 AI 工作平台。

用户可以同时维护多个项目。

每个项目具有明确目标、状态、计划、历史决策、执行记录和成果。

不同项目可以使用不同模型、不同能力包和不同执行器。

用户既可以进行简单的一次性聊天，也可以把复杂目标交给 ProjectOS 长期托管。

系统能够在合理的授权与预算范围内，自主拆解工作、调度多个 Agent、验证结果、处理失败、推进依赖，并保留必要的长期上下文。

当用户重新打开项目时，系统能够准确知道：

- 这个项目的目标是什么。
- 已有哪些经过验证的成果。
- 当前有哪些任务正在执行。
- 哪些任务被阻塞。
- 为什么做过某些重要决策。
- 哪些方法已经失败。
- 哪些成果尚未通过验收。
- 下一步有哪些可执行工作。
- 当前需要用户处理什么问题。

ProjectOS 最终的核心价值体现在五个方面：

**Autonomy — 自主推进**

明确授权范围后，减少用户持续指挥的需求。

**Reliability — 可靠执行**

真实状态、正确验收、可控重试、异常恢复。

**Efficiency — 高效协作**

在给定预算下，通过合理并发与低协调开销完成更多有效工作。

**Continuity — 长期连续性**

跨 Agent、跨 Session、跨模型持续维护项目状态。

**Extensibility — 通用扩展**

通过执行器适配和 Capability Packs 覆盖不同工作领域。

最终，ProjectOS 希望实现的是：

> 一个用户可以长期信任的 AI 工作系统。它能够理解并持续推进目标，组织合适的 Agent 工作，以证据确认成果，在成本和权限边界内自主运行，并在真正需要人类决策时请求介入。

这是 ProjectOS 2.0 的产品定位、目标能力、架构原则和长期设计背景。

**当前项目状态：现有 Research Agent OS Core 已有实际仓库和实现；ProjectOS 2.0 仍处于架构设计与技术选型阶段；Orchestrator 是重点考虑复用的开源基础设施，尚未确定最终集成方式。**
