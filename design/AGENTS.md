# ProjectOS 2.0 — 项目规则

用户指令与全局 Codex / Research Agent OS Core 优先；本文件仅补充项目边界。

- 启动读取 `.project/PROJECT.md`、`HANDOFF.md`、`STATE.md`；改计划前读 `PLAN.md`，决策按需读 `DECISIONS.md`。
- 使用 codex-workflow-optimizer 与 agent-workflow-optimizer；先 inspect，再在明确授权范围内执行、验证、审查、持久化、交付。
- 修改代码前说明范围并请示；已有任务级授权可在边界内持续执行。初始化授权不等于实现、安装或运行授权。
- 本项目为通用长期 Agent 工作系统；不将科研、Harness 或模型供应商固化到 Kernel。
- `docs/project-background.md` 是用户提供的背景原文，含目标、候选架构及未验证的上游能力，不是实现证据。
- 优先查真实接口、复用成熟基础设施；集成方式、状态机和 Manifest 协议未冻结，不凭空创造接口。
- 不改相邻项目、认证、MCP、模型或运行中的应用；联网、GUI、依赖安装、远程副作用遵守用户授权边界。
- 每种状态保持单一权威与写入责任；Agent 自报完成不能替代产物验证与独立验收。
- `.project/` 保存精简权威状态；长期设计放 docs/，代码 src/，测试 tests/，配置 configs/，数据 data/，正式实验 experiments/；临时内容 outputs/ 或 .scratch/。
- HANDOFF 每次项目回合交还前覆盖，使用 Goal / Done / Verified / Open / Active / Next；失败路径才加 Rejected。
- 遵守 Core 控制面预算；交付前运行两种本机只读审计。门禁 JSON 仅登记契约，不代表实际执行拦截。
