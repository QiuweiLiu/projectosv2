# 成果展示与继续改进（2026-11-05）

## 1. 成果（outcome）

界面上「进度 → 任务 → 任务详情 → 最近发生」之后新增「成果」：

- 列出 `out/` 下每个文件：路径、大小、内容预览（文本前 400 字，二进制不预览）；
- 汇报人的模型版现在会拿到这份文件清单（含预览），所以它说的是“交付了什么”，
  而不只是“几个任务通过了”。

实现：`delivery.List` 读 `out/`，`GET /api/report` 附带 `deliverables`，
`goal report --llm` 把文件清单拼进模型输入。

真实样例（`/private/tmp/final-csv-182545`，4 个任务）：

```
sales.csv     16 B    amount 10 20 30
summarize.js  1.2 KB  #!/usr/bin/env node ...
summary.md    34 B    # Sales Summary / Total amount: 60
total.json    12 B    {"total":60}
```

## 2. 继续改进（continue）

已完成或失败的目标现在可以继续：

```
kernel goal continue --dir D --goal "额外生成 total.json，包含总额 60"
→ plan v2 ready (3 tasks kept, 1 new); objective is active
→ kernel goal run ...   （或界面上点执行）
```

规则：

- 已通过的任务**原样保留**：模型改写会被 `NormalizeAccepted` 还原，漏写会被补回；
- 只能新增（≤5 个），新产物必须与已有产物不同；
- 新计划立即生效，但不会自动执行——人确认后再点执行；
- 旧计划标为 superseded，历史仍可见。

真实复跑：T4 `total.json` 一次通过，`out/` 4 个文件，交付验收通过，objective completed v2。

## 3. 这轮修掉的两个真 bug

| 问题 | 原因 | 修复 |
|---|---|---|
| 继续改进后界面把保留的任务显示为“待执行” | `TasksForPlan` 按 `plan_version` 过滤，保留的已验收任务仍是 v1 | 改为按计划成员（plan spec 的任务 ID）判定；回归 `TestTasksForPlanIncludesCarriedAcceptedTasks` |
| 模型汇报在状态变化后仍显示旧内容 | 汇报文件按存在与否读取，没有绑定状态 | 写入时带状态哈希（目标状态/计划版本/任务状态/尝试次数），不匹配即视为无汇报，界面会自动重生成 |

另外修了 `goal report` 在关闭 store 之后才读任务列表、导致哈希按空集合计算的问题。

## 4. 验证

```
132 项测试 PASS（+3）
go test -race   PASS
go vet          exit 0
```

界面：4 个任务全部“已通过”，成果 4 个文件，汇报为模型版且内容是 4 个任务的新版本；
「继续改进」按钮在已完成状态下可见。
