# Plan

## Now
UI/UX 极简重构与复核修复已提交；随后恢复 P6 可靠性加固。

## Steps
1. 已提交 UI/UX 重构；真机确认 900/1440 布局与远程鉴权。
2. 跑 Task B（数据分析）与 Task C（多阶段研究）真实对照，记录 hidden test、wall/tokens。
3. 按样本评估 Planner/Reviewer/Acceptance 成本；只做证据支持的默认策略调整。
4. 补磁盘满、只读挂载、WAL、OpenCode Server 退出重连故障注入。
5. 维持 Manifest/Evidence/Recovery 的现有正确性门禁，不新增第二套状态机。

## Acceptance
- 异常不产生虚假完成、重复副作用或越权继续。
- Manifest 并发读写不跨版本组合；预算内不产生未闭合证据块。
- 已完成 Objective 的 Task 不被误标为已发布。
- 现有 CLI、Workspace、298 测试保持兼容；B/C 实验条件可复现。

## Not now
Windows/Linux、逐工具托管审批、分布式组件、向量数据库、大规模付费模型实验。
