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

## 尚未实现

- 逐工具审批与跨进程恢复预算共享：当前恢复预算按 Objective 持久化，工具级审批仍由
  OpenCode 权限流程处理。
