# 有界重规划（P1.2）

失败后的计划调整必须能收紧，而不是无限自我分解。

## 触发

只有 `kernel goal run --replan` 才启用，且满足：

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
| 单次新增任务 | ≤ 5 |
| 计划总任务 | ≤ 20 |
| 已接受任务 | 不可删除、不可改 artifact、不可改 validator |
| 运行中的任务 | 不会被 supersede（只处理 pending/blocked） |

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
