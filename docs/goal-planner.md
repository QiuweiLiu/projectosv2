# Goal Manager 与 Planner（P1.1）

本轮把入口从"手写 objective.json"升级为"自然语言目标 → 校验过的计划 → 人工确认 → 执行"。

## 数据模型

```
objective（目标）
  id · goal（自然语言） · title · constraints · expected · acceptance
  status: draft → planned → active → completed / failed / cancelled
  plan_version：当前生效的计划版本

plan（计划，版本化）
  objective_id + version（唯一）
  status: draft → confirmed → superseded
  spec：goal.Plan（JSON）

task/run/evidence/review（既有 Kernel）
```

一个 state dir 只允许一个 objective（P1 约束）。需要并行目标就开新目录。

## 计划 Schema

```json
{
  "objectiveAcceptance": ["..."],
  "tasks": [{
    "id": "T1", "goal": "...", "scope": "...", "inputs": [],
    "deps": [], "acceptance": ["..."],
    "validators": [{"type": "exact", "expect": "42"}],
    "artifacts": ["a.txt"],
    "risk": "low|medium|high", "maxAttempts": 1,
    "allowed": [], "forbidden": []
  }]
}
```

Kernel 侧校验（`goal.Plan.Validate`）：非空、任务数 ≤ 20、ID 唯一、依赖存在且无环、深度 ≤ 6、
每个任务至少一个可执行 validator、artifact 必须是仓库内相对路径、risk 合法、maxAttempts ≤ 3。
Planner 的自由文本永远不会直接变成任务。

## Planner

- `planner.Fake`：确定性测试 / `--planner fake --plan-file x.json`。
- `planner.Model`：复用 AO 会话 + OpenCode 2，要求只输出 JSON；`planner.Generate` 最多 2 次尝试，
  每次把上一次的校验错误作为反馈塞回提示词。

**Planner 只提议，Kernel 校验，人确认。**

## CLI

```sh
kernel goal create --dir D --goal "自然语言目标"
kernel goal plan   --dir D [--planner fake|model] [--max-tasks 8]
kernel goal show   --dir D
kernel goal confirm --dir D [--version N]
kernel goal run    --dir D [--executor ao|fake] [--reviewer] [--max-runs N ...]
kernel goal status --dir D
```

`goal run` 会：编译计划为 Kernel Task → 自动执行 → 验收/审查 → 全部 accepted 则 objective completed
并写出 `delivery.md` / `delivery.json`。

## 真实证据（2026-11-03）

```
goal create  → obj-…
goal plan --planner model → plan v1: 1 task, artifact hello.txt
goal confirm → active
goal run --executor ao --reviewer → T1 accepted (attempts=1)
             → delivery written, objective completed
产物内容：hello from projectos（字节精确，无换行）
```

## Replanner（P1.2）

`kernel goal run --replan` 启用有界重规划：已接受任务不可改，新增 ≤5，单次运行最多 1 次；
旧计划未完成任务标记 `superseded`，Objective 完成只看当前计划版本。详见 [replanning.md](replanning.md)。

## 尚未实现
- **多目标**：一个 state dir 一个 objective。
- **Context Manager**：`.project/` 与 SQLite 的映射尚未接入目标。
- **Desktop 集成**：仍只有只读看板 + CLI。
- 计划变更只允许在任务尚未执行时创建新版本；执行后需要新的取消/替代流程。
