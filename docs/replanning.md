# 有界重规划（P1.2）

失败后的计划调整必须能收紧，而不是无限自我分解。

## 触发

自动重规划只在 `kernel goal run --replan` 中启用，且满足：

- 当前计划版本存在任务未接受（`blocked`）；
- 本进程已重规划次数 < `--max-replans`（默认 1）；
- 失败上下文可构造（能读到 blocked 原因）。

## 流程

```
T1 blocked
   ↓ FailureContext：列出 blocked 原因 + 已接受任务
Planner（model 或 fake）
   ↓ 新计划
ValidateReplacement
   ├─ 必须包含所有已接受任务，且 artifact/validators 不变
   ├─ 新增任务 ≤ 5，总数 ≤ 20
   └─ schema/DAG 校验（同首次计划）
Apply
   ├─ 新计划中不再出现的未完成任务 → superseded
   ├─ 新版本 draft → confirmed（旧版本 superseded）
   └─ 新任务以 plan_version=N+1 落库
继续执行；再次失败且额度用尽 → objective failed
```

## 硬约束

| 约束 | 值 |
|---|---|
| 每次运行自动重规划次数 | 1（`--max-replans`） |
| 单次新增任务（自动） | ≤ 5 |
| 单次新增任务（显式 `goal replan`） | ≤ 当前计划任务数（总任务上限 20） |
| 计划总任务 | ≤ 20 |
| 已接受任务 | 不可删除、不可改 artifact、不可改 validator |
| 运行中的任务 | 不会被 supersede（只处理 pending/blocked） |

## 聊天内调整

`kernel goal replan --instruction "..."`（HTTP `POST /api/objectives/{id}/replan`
`{"instruction":"..."}`）会把操作者在聊天里写的要求附加到 planner 的 feedback，用于
“已执行/被卡住”的目标。尚未执行的目标改动走 `goal plan --instruction` 或
`POST /api/objectives/{id}/amend` + `/plan`，不改写已验收任务的证据。

## 显式重新规划

`goal run --replan` 在启动时已有 blocked 任务的情况下不会生效：recovery 先要求人工
批准并退出。界面上的“重新规划”调用独立命令 `kernel goal replan`（HTTP
`POST /api/objectives/{id}/replan`），语义见 `docs/recovery.md`：保留已通过任务、
替换未完成任务、生成新版本并回到 active，但**不自动执行**。

## 产物未变化时的审查

同一个任务重试后可能产出与上次字节相同的产物（同一 `reviewFingerprint`）。此时
reviewer 对同一版本的结论不会变化，重复调用只会浪费 token，并触发“每个版本最多一次
full + 一次 targeted”的上限。Engine 现在直接复用已记录的同版本审查结论：

- 结论为 blocking → 该任务 blocked，原因写“artifact unchanged since the last review”；
- 结论为 clean → 跳过 reviewer，但仍运行 acceptance；
- 审查未能落库且确有未知用量时才全局停止；已知用量的策略性拒绝（如版本审查上限）
  只影响当前任务，不再把无关 pending 任务标成“usage unknown”。

## 语义

- `superseded` 是终态，但不是成功：它不计入完成，也不会被执行或重试。
- Objective 完成 = **当前 plan version 的所有任务 accepted**；旧版本任务被忽略。
- 交付报告列出所有任务，包括 superseded 及其原因。

## 已验证（2026-11-03）

确定性场景（`--executor fake --fake-fail T1 --replan`）：

```
plan v1: T1 (fails), T2 depends T1
→ replan v2: T1/T2 superseded, T3 added
→ T3 accepted → objective completed (plan v2)
delivery.md 记录 T1 superseded、T3 accepted
```

真实模型重规划未跑：需要构造一个真实失败并等待模型产出修复计划，留待后续。
