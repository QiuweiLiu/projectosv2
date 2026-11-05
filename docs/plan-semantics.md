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
| `risk` | ⚠️ 仅校验与展示 | 不改变调度、并发、预算或审批 |
| `scope` | ⚠️ 仅提示词 | 写明“只改 X”不会阻止 worker 改 Y |
| `inputs` | ⚠️ 仅提示词 | 实际注入依据是 `deps`，不是这个字段 |
| `allowed` | ⚠️ 仅提示词 | 没有能力/工具级强制 |
| `forbidden` | ⚠️ 仅提示词 | 同上，只能靠 reviewer 事后发现 |
| `acceptance` | ⚠️ 仅提示词 | 机器验收只看 `validators`；验收文字不参与判定 |
| `priority` | ❌ 未使用 | 调度按 DAG 就绪，不排序 |
| `objectiveAcceptance` | ❌ 未作为完成门槛 | 存储并参与 hash，但完成判定看任务状态 |

## 使用建议

- 想让某个条件真正阻断，写成 `validators`（内容或命令），不要只写 `acceptance`。
- 想限制产物范围，把文件写进 `artifacts`；`forbidden` 只能提醒 worker 和 reviewer。
- 需要“必须跑起来”的证据，用 `command`，并确保命令在目标环境可执行（例如优先 `python3 -m unittest`）。
