# 可用性评估（2026-11-03）

结论：**可用，但只在受控范围内可用。** 不是"能上线"，是"能拿真实小任务试"。

## 证据

真实运行共 26 次（`~/dev/p3/*/kernel.db` 可复查）：

| 类别 | 次数 | 说明 |
|---|---|---|
| 全绿 | 9 | 三个任务全部 accepted |
| 故意失败 | 4 | `fail-a`、`max-runs`、注入 P1 等演示 |
| 正确阻断 | 3 | 模型真的写了额外文件 / 产物带换行被严格比较拒绝 |
| 自身缺陷 | 10 | reviewer 提示词、幂等键、只读误用等，已修 |

**最近 5 次连续全绿**（real-run-13/16、kernel-run-11/12/13），出现在最后一轮修复之后。
之前的失败几乎都是我们自己的 bug，不是架构问题。

## 现在能做什么

- 用 JSON 定义 3–10 个任务、带依赖的 DAG
- 真实 Worker（OpenCode 2 + DeepSeek）执行，Kernel 独立验收
- 产物级验收（读工作区文件，不信 Agent 自述）
- 独立 reviewer 阻断 P0/P1，P1 → 修复 → 定向复审闭环
- 预算上限（Run 数 / Review 数 / 字符估算 token）
- 崩溃后不重复执行：在飞 Run 标 unknown、任务 blocked
- 只读看板：`kernel serve`

## 现在不能做什么

| 不能 | 原因 |
|---|---|
| 无人值守跑 | reviewer 行为不稳定；失败会停在 blocked，需要人看 |
| 关键交付 | token 是估算、无成本保证；跨平台未测 |
| 多机 / 团队共享 | 单 writer SQLite + 本机 daemon |
| 大规模 DAG | 同步轮询、单进程调度；没有队列与水平扩展 |
| 保证审查质量 | reviewer 出现过漏报（run-9）与误报（run-15） |

## 前置条件（缺一不可）

1. AO daemon 二进制（固定提交 `d2c997ba` 编译）
2. OpenCode 2 CLI（`/Applications/OpenCode.app/.../opencode-cli`）
3. DeepSeek API key（仅环境变量，不落盘）
4. 隔离的 HOME/XDG + 一个带 remote 的沙箱 git 仓库

## 建议的试用方式（pilot）

- 选一个**低风险、可回滚**的小仓库
- 每次只跑 3 个任务，`pack=research`（强制 reviewer）
- 预算设死：`--max-runs 6 --max-reviews 6 --max-tokens 200000`
- 每天人工复盘一次 blocked 原因，区分"模型问题"与"我们的 bug"

## 判定升级为"可依赖"的条件

1. 连续 20 次真实运行无自身缺陷（不是无失败，是无"我们的 bug"）
2. reviewer 漏报/误报有量化基线（例如 20 个已知缺陷样本）
3. 失败路径全部有可复现测试（网络、spawn 超时、host 崩溃）
4. 成本口径从"字符估算"升级为可对账
