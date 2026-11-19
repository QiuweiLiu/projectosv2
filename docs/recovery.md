# 有界自主恢复（P5.1）

Recovery 不是第二套 Scheduler。它读取既有 Task/Run/Review 事实，给出一个决定，然后只
调用 Kernel 已认可的状态转换：`Store.RequeueBlocked` 或现有 bounded replanner。

## Failure 分类

`progress.Assess` 先看结构化记录（Run 状态、Review findings），不足时才看持久化的
`task.last_error`：

| Category | 依据 | 默认动作 |
|---|---|---|
| `unknown_execution_state` | Run `unknown` / interrupted | 人工检查，绝不自动重试 |
| `permission_denied` | permission/approval 文本 | 等待批准 |
| `resource_exhaustion` | `budget:` / usage unknown | 等待批准或提高预算 |
| `provider_transient` | 429/overloaded/ECONNRESET/timeout | 有界重试 |
| `artifact_mismatch` | `verification:` | 带原错误重试 |
| `review_blocked` | blocking review / `review:` / `acceptance:` | 带 findings 定向重试 |
| `dependency_failure` | `dependency` | 有界 replan（需 `goal run --replan`） |
| `environment_failure` | start/prompt/collect/observe | 人工检查执行器 |
| `cancelled` | cancel | 不重试 |
| `unknown` | 无法归类 | 人工检查 |

无法分类时不猜测；`Failure.Recoverable` 保持 false。

## Policy 与预算

`recovery.Decide` 的边界：

- 自动恢复最多 `--max-recoveries`（默认 2），计数持久化在 `meta.recovery_count`；
- `--max-recoveries` 写入 `meta.recovery_budget`，Progress/API 读取同一值；
- Task 总尝试次数上限 3（`AttemptCap`），`RequeueBlocked` 每次只把 `max_attempts` 加一；
- 只处理 `blocked`/`rejected` Task；accepted/running/superseded 永不被改写；
- Token/墙钟预算仍由现有 engine `--max-tokens`/`--timeout` 控制，Recovery 不另建预算；
- 外部副作用未知（Run `unknown`）时不重试；
- 权限/预算问题只生成 pending approval，不自动放行。

恢复后重新打开仅因上游阻塞的 pending Task（`last_error LIKE 'dependency blocked%'`），
已验收 Task 保持不变，Engine 重新计算 READY。

## 可选模型辅助分类

默认关闭，保持确定性、零额外模型调用。设置：

```sh
kernel goal run --dir D --recovery-model provider/model
```

只有在确定性诊断落在 `unknown`（不是 `unknown_execution_state`）且该 Objective 尚未
用过模型诊断时，才发起一次短会话；结果只能改变 **category**，动作、预算、attempt cap
仍由 `recovery.Decide` 校验。模型调用次数记录在 `meta.recovery_model_calls`，最多 1 次，
并写 `recovery_model_suggestion` / `recovery_model_error` 事件。

## 操作

```sh
# 查看当前诊断与预算
kernel goal progress --dir D
kernel goal recover --dir D

# 人工批准一个 retry/approval，或显式恢复一个 unknown（检查副作用后）
kernel goal recover --dir D --approve
kernel goal recover --dir D --approve --task T1

# 重新运行；failed Objective 在批准后会被安全地重新激活
kernel goal run --dir D --recover --max-recoveries 2
```

HTTP：

- `GET /api/objectives/{id}/progress`：状态、failure、recovery budget；
- `POST /api/objectives/{id}/recover`：`{"approve":false}` 查看决定；
  `{"approve":true}` 执行已批准的重排队。

`unknown_execution_state` 不会自动重试；`goal run` 会保留 active 状态并输出人工检查提示，
避免把可能已经发生的外部副作用再执行一次。

## 驾驶舱状态与动作

驾驶舱不再直接显示底层状态字符串，而是派生一个用户状态：

- active + running → **执行中**；active + blocked → **已卡住**；
- active + pending 且无 blocked/running → **待执行**；全部通过 → **收尾中**。

动作按状态收敛：运行中只显示暂停/取消；有 blocked 时隐藏“开始执行”，顶部显示
“需要你处理”卡片，列出每个卡住任务的原因（审查预算/上游阻塞/预算用量未知）以及
“批准恢复并重试”“重新规划并继续”“查看卡住的任务”。原始 CLI 错误行不再直接显示在
标题下；历史汇报会标注“生成于上次运行，当前状态以上方提示为准”。

执行前确认弹窗会说明：只启动调度、任务仍逐项审查与验收、命令以当前用户身份执行；
并列出需要确认的任务/命令、风险级别和预算。只有存在命令校验/高风险时才要求勾选。

## 显式重新规划（失败后）

`goal run --replan` 只在运行循环内、且失败类别允许 replan 时才生效；如果 Objective
在启动时已经有 blocked 任务，recovery 会先要求人工批准并直接退出，此时 `--replan`
什么也不会做。界面上的“重新规划”因此走独立命令：

```text
kernel goal replan --dir D [--planner model|fake] [--plan-file P] [--context ...]
POST /api/objectives/{id}/replan
```

语义：

- 只接受 active / failed 且存在 blocked/rejected 任务的 Objective；没有 blocked 时是
  无操作，不产生新计划；
- 保留所有已通过任务（逐字复制，不允许改写）；未完成任务由新计划替换，被丢弃的
  pending/blocked 任务标记为 superseded；
- 新任务必须使用新 id（不能复用历史 id），由计划校验与历史 id 检查强制；
- 生成失败或被拒绝时保留原计划，Objective 状态不变；
- 成功后写入 plan vN+1 并置为 confirmed，Objective 回到 active，清除过期的
  recovery pending；**不自动执行任务**，仍需用户在驾驶舱确认后点“开始执行”。

HTTP 端点异步执行并返回 `{"status":"replanning"}`；`GET /api/jobs` 依次出现
`replanning` → `replanned` 或 `replan failed: ...`。驾驶舱轮询该状态，完成后刷新任务图。

## 尚未实现

- 逐工具审批与跨进程恢复预算共享：当前恢复预算按 Objective 持久化，工具级审批仍由
  OpenCode 权限流程处理。
