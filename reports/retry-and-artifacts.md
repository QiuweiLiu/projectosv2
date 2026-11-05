# 重试反馈与产物约束（2026-11-04）

两个改动都来自真实失败，不是设计推演。

## 1. 重试时把 reviewer 的结论交给 worker

**现象**：review 阻断后，`tasks.last_error` 只有 `review: blocking finding (P0/P1)`。
worker 第二次尝试只知道“被拒了”，不知道**为什么**，只能重新赌一次。

**改动**：`expandPrompt` 在 attempt > 1 时读取最近一次 full review 的全部 findings：

```
The reviewer rejected your previous attempt. Fix exactly these findings:
- [P1] csv_columns_ignored: parse the amount column explicitly
The same output will be rejected again.

Non-blocking review notes (fix them if cheap):
- [P2] ...
```

- P0/P1 是必须修复项；P2/P3 是建议，不阻断；
- 单条截断 400 字符；
- 没有 findings（例如验收命令失败）时仍回退到 `last_error` 文本。

回归：`TestRetryPromptCarriesReviewerFindings`。

## 2. 一个任务只能有一个产物

**现象**：CSV 目标的一轮计划把任务写成 `artifacts: ["summarize.js","summary.md"]`。
Kernel 实际只跟踪、验收、组装第一个产物，reviewer 的审查范围也只有第一个，
于是把 worker 按指示产出的 `summary.md` 判成“多余文件”，连续两轮阻断：

```
T2 P1 unexpected_output_file: The run left summary.md in the workspace;
   the expected deliverable is only summarize.js
```

这不是 reviewer 太严，而是**声明与实现不一致**：`compile` 会告诉 worker “Also produce summary.md”，
但证据、验收、交付都只认第一个文件。

**改动**：

- `Plan.Validate` 拒绝多产物任务：`task T2 declares 2 artifacts; one task produces exactly one artifact — split it`；
- `compile` 删掉 “Also produce …” 的指示；
- 规划器提示词：一个任务一个产物；需要两个文件就拆成两个任务并连依赖。

同类修正：**`exact` 只用于目标里写死的内容**（如 42、固定字符串）。
之前规划器给生成的 CSV 编了一个期望内容 `amount\n120\n80\n100\n`，worker 不可能知道，第一次必然失败。

## 3. 复跑结果

```
计划：T1 sales.csv → T2 summarize.js → T3 summary.md   （每个任务一个产物）
final: completed
T1/T2/T3 全部 accepted，attempts=1
blocking reviews: 0
summary.md: Total amount: 600
交付验收：T2 `node summarize.js` 在 out/ 副本上通过
```

对比同一目标的前两轮：一次因传递依赖误判失败，一次因多产物声明失败并消耗两轮重试。
现在一次通过，且没有阻断。

## 4. 结论

三次失败都不是 Kernel 的调度或结算错误，而是**计划声明的表达力与实际执行模型不匹配**：

| 失败 | 声明 | 实际 |
|---|---|---|
| reviewer 误判依赖 | 只声明直接依赖 | 执行时注入祖先闭包 |
| reviewer 误判多产物 | 一个任务多个产物 | 只跟踪第一个 |
| 重试无效 | 只说“被拒” | 有具体 findings 可操作 |

修法统一是：**让声明与执行模型一致**，而不是放宽检查。
