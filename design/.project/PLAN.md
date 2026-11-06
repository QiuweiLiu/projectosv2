# Plan

## Current Objective
验证 P2 可用性：多目标 Workspace、执行前审批、文本 diff、会话续跑、macOS 原生壳。

## Authorized Boundary
用户已授权：Kernel P1/P2 开发、真实低风险目标、桌面壳、文档与测试。
仍禁止：改 research-agent-os、改全局配置、不可逆操作、未批准的付费/远程副作用。

## Verified Baseline
- 188 项 Go 测试 + race + vet 全绿；kernel 审计 0 fail / 1 warn（根目录布局）。
- Workspace 双目标创建/切换/详情/汇报/事件已验证；真实目标完成全流程。
- 审批弹窗、diff、桌面 `--check` 已验证；token 为 provider 实测值。

## Next Experiments
1. 同一 Workspace 并行跑 2–3 个真实目标，记录审批摩擦与 diff 可读性。
2. 观察 reviewer 首次阻断率与 acceptance 误伤率，优先修计划质量。
3. 评估 SSE 是否值得做；其余边界（Windows/Linux、逐工具审批）暂不做。

## 升级判据
连续 20 次无自身缺陷；reviewer 有量化基线；失败路径可复现；成本可对账。
