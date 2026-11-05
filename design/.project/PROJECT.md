# ProjectOS 2.0

## 目标与定位
构建通用、长期运行、可验证、可扩展的个人自主多 Agent 工作系统。以 Objective / Task / Run、证据验收、持续接续、预算权限和恢复支撑开发、科研、数据、文档等工作。

## 来源与范围
- 用户完整背景：`docs/project-background.md`；保存原文供追溯，不将愿景写成已实现能力。
- 本阶段获用户“开始吧”授权：编写单 Worker、两个依赖任务的 MVP 验收规格，核验公开上游固定版本源码，形成能力证据表与路线建议。
- 产品目标：单机单用户，macOS 优先；普通聊天与长期托管共存；Desktop 与后台服务解耦。
- 当前处于架构设计与技术选型阶段。未授权业务实现、依赖安装、Fork、运行上游应用、远程操作或正式实验。

## 目标逻辑架构（未冻结）
Desktop Interaction → ProjectOS Kernel → Capability Packs → Execution Infrastructure → Harness / Tools。
Kernel 负责目标、Task DAG、确定性生命周期与调度、验收、权限预算、长期状态与恢复。执行器负责实际 Session / 进程生命周期；同一 Worker 不得存在双重控制权。
核心对象候选：Objective、Task、Run、Artifact、Evidence、Verification/Review、Decision/Approval。

## 候选依赖与待验证事项
- Research Agent OS：规范与领域知识来源；已按固定版本静态核验 Core、许可文件及选定插件，证据见 docs/upstream-capabilities.md。
- Orchestrator：桌面和执行基础设施候选；已按固定版本静态核验选定 API、许可文件、恢复与 Adapter 源码，实际运行能力仍待验证，证据见 docs/upstream-capabilities.md。
- OpenCode 2 / DeepSeek Harness 为首批执行器候选，不绑定 Kernel。
- 外部 Kernel + API、受控 Fork、统一 Go Daemon 或独立 Runtime 尚未选定。
- 背景中的状态、字段与 Capability Manifest 均为设计输入，不等于接口契约已冻结。

## 不变量与约束
- 复用成熟基础设施；不预先引入分布式队列、Redis、向量数据库或 Kubernetes。
- 实现状态、实验证据、科学 claim 分离；任务只有通过适配风险的 Quality Gate 才能验收。
- Task 与 Run 分离，重试保留历史；关键转换由确定性程序约束。
- 单一权威：项目事实 `.project/`；运行态未来由事务存储承载；详细日志/产物独立；Harness Session 不承担最终任务真相。
- 一次完整独立审查，必要时一次阻断缺陷定向复审；不因风格反复审查。
- 权限与预算绑定具体范围；不以 Prompt 或 JSON 登记代替实际安全机制。
- 当前保留 Core 的实验登记文件，仅占位且无运行授权；产品中的科研专属门禁仍是可选 Research 能力。

## 位置
长期设计 docs/；代码 src/；测试 tests/；配置 configs/；脚本 scripts/；数据 data/（raw/ 只读）；实验 experiments/；临时输出 outputs/；草稿 .scratch/。本轮不创建 paper/。
