# P1 验收记录（2026-11-03）

## 场景结果

| 场景 | 方式 | 结果 |
|---|---|---|
| A 待办应用 | 真实模型 + 真实 Worker + reviewer | **通过**（14/14 测试在 out/ 通过） |
| B 非科研任务 | 真实模型：sales.csv → summarize.js → summary.md | **通过**（总额 400，脚本可重跑） |
| C 失败与重规划 | 真实模型重规划：T1(41≠42) 失败 → v2 生成 T1-fix → 通过 | **通过**（真实） |
| D 中断恢复 | 真实：跑长任务时 SIGKILL CLI，重跑 | **通过**：Run→unknown、Task→blocked，未重复执行；复用了幸存 daemon |
| E Quality Gate | 单测：P1 阻断 / P2 放行 / 1+1 限额 / 版本绑定 | 通过（单元级） |

## 关键能力

- Objective 持久化 + 状态机；Plan 版本化（draft/confirmed/superseded）。
- Planner：Fake/Model；Kernel 侧 Schema/DAG/上限/路径/validator 校验。
- 执行：A∥B 并行；依赖产物注入；命令验收在 sandbox 中运行。
- 失败：有界重规划（已接受不可改、+5、1 次）；未决结局不重试。
- 交付：`delivery.md/json` + `out/` 组装 + `.project/` 快照。
- 交互：CLI + 只读看板 + 可选 token 保护的网页目标输入（`serve --control`）。

## L0 / L1（2026-11-04）

| 项 | 方式 | 结果 |
|---|---|---|
| 杀客户端任务继续 | `kill -9` serve，期间只读 CLI 查状态 | **通过**：E1 保持 running → completed，`out/essay.md` 3.9KB |
| 运行中取消 | `goal cancel` → scheduler 消费 flag | **通过**：14s 内 cancelled，任务 blocked，0 残留 |
| 取消不改写结论 | 对 completed 目标 cancel | 拒绝，结论保持 completed |
| 事件游标 | `after=` 续传 | 无重复、无遗漏 |
| 服务生命周期 | `--daemon / --status / --stop`、token 跨重启 | 通过，401 生效 |

详见 `reports/p1-l0-l1.md`。

## 未覆盖

- **L2 界面**：目标列表 / 任务图 / 证据面板（当前只有只读看板 + 提交框）。
- Desktop 原生客户端（Electron / AO Chat 集成）。
- 多机、Windows/Linux、token 精确计量。
