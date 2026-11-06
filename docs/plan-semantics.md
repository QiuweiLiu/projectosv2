# 计划字段：哪些真正被执行（2026-11-04 审计结论）

审查发现计划 schema 里有一批字段只是被写进提示词。这张表是对读者的承诺边界：
**没被执行的字段不能当作验收依据。**

| 字段 | 是否被执行 | 实际行为 |
|---|---|---|
| `artifacts[0]` | ✅ | 收集内容、写 evidence、命令验收 sandbox、组装 `out/`；必须是一个文件，且全局唯一 |
| `validators` | ✅ | `exact` / `contains` / `nonempty` / `command` 决定接受或拒绝；另有 acceptance 会话做完成度判断 |
| `deps` | ✅ | 决定就绪顺序、依赖产物注入、reviewer 依赖清单、验收 sandbox 内容 |
| `maxAttempts` | ✅ | 结算时判断是否允许重试；`0` 视为 1，上限 3 |
| `id` | ✅ | 调度与证据的主键；重规划不得复用历史 ID |
| `risk` | ✅（审批） | `risk=high` 会触发执行前审批，缺少 `--approve-risk` 时 `goal run` 拒绝启动 |
| `scope` | ⚠️ 仅提示词 | 写明“只改 X”不会阻止 worker 改 Y |
| `inputs` | ⚠️ 仅提示词 | 实际注入依据是 `deps`，不是这个字段 |
| `allowed` | ⚠️ 仅提示词 | 没有能力/工具级强制 |
| `forbidden` | ⚠️ 仅提示词 | 同上，只能靠 reviewer 事后发现 |
| `acceptance` | ⚠️ 仅提示词 | 机器验收只看 `validators`；验收文字不参与判定 |
| `priority` | ❌ 未使用 | 调度按 DAG 就绪，不排序 |
| `objectiveAcceptance` | ✅（模型门禁） | 任务全 accepted 后，独立的 objective acceptance 会话判断原始目标；P0/P1 阻断则不交付、不标 completed |

## Objective 级完成判定

`objectiveAcceptance` 不参与确定性文件校验；它进入**目标级 acceptance 会话**的提示词。
该会话在任务全部 accepted 后运行一次，输入是原始 goal/expected outcome、验收文字、
已验收任务清单和产物内容。结果写入 `reviews`（`kind=objective`），同一内容哈希只判一次。

确定性部分仍然是：任务产物存在、`validators`、交付组装后的 command 复验。
模型判断不能替代这些硬检查；反过来，硬检查通过也不自动等于原始目标完成。

`--acceptance=false` 会关闭任务级和 Objective 级模型判断，回到“任务全 accepted 即完成”的
旧行为；它只应作为明确的运维逃生口，不应作为默认配置。

## 使用建议

- 想让某个条件真正阻断，写成 `validators`（内容或命令），不要只写 `acceptance`。
- 想限制产物范围，把文件写进 `artifacts`；`forbidden` 只能提醒 worker 和 reviewer。
- 需要“必须跑起来”的证据，用 `command`，并确保命令在目标环境可执行（例如优先 `python3 -m unittest`）。


## 暂停

`goal pause` / `POST /api/objectives/{id}/pause` 只对**正在运行的调度器**生效：
它停止启动新任务，当前 Run 允许完成；`unpause` 移除标记。没有调度器时暂停请求不会
留下陈旧标记，`goal run` 启动时也会清理旧标记。


## 机器检查进入 worker prompt

`exact` / `contains` / `command` 不再只存在于 Kernel 校验器：`compile.prompt` 会把
“必须等于/必须包含/命令必须退出 0”写进 worker 提示词。这样可以避免 Planner 要求
`def add_task`、worker 却完全不知道这个约束。

Planner 也被要求：除非目标明确点名，不要用 exact/contains 断言内部函数/类名；优先用
command 验证行为。Prompt 规则不替代校验器，只是减少计划与产物语义错位。
