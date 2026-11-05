# 验收 agent · 汇报人 · 界面重做（2026-11-05）

## 1. 验收 agent（acceptance judge）

**动机**：reviewer 回答“有没有缺陷”，不回答“任务到底完成没有”。两者会不一致：
一个文件可以没有缺陷，却没有做到任务要求的事。

**实现**：

- 新增 `domain.ReviewAcceptance`（`reviews.kind=acceptance`），与 full/targeted 并列。
- `ao.Acceptance` 是独立会话，prompt 只问一件事：这个产物是否真的完成了任务；
  缺少任务要求的内容记 P0/P1，风格问题不阻断，不得发明新要求。
- 顺序：确定性验收（内容/命令）→ reviewer → acceptance。三者都过才 `accepted`。
- 失败同样走重试与定向复审；同版本各限一次。
- `--acceptance=false` 可关闭（成本是每任务多一次模型会话）。

真实运行（`/private/tmp/accept-114042`，1 个任务）：

```
final: completed
reviews: full blocking=0 / acceptance blocking=0
T1 accepted attempts=1
```

引擎回归：`TestAcceptanceFailureBlocksEvenWhenReviewPasses`——reviewer 放行、
acceptance 判失败时，任务不得 accepted，且 `last_error` 必须点名 acceptance。

## 2. 汇报人（reporter）

**动机**：界面里全是英文状态、哈希、原始产物，读起来像调试面板。

**两层实现**：

| 层 | 何时用 | 特点 |
|---|---|---|
| 规则版 `report.Build` | 每次轮询 | 纯 Go，免费即时；状态/原因翻成中文，屏蔽哈希与目录细节 |
| 模型版 `report.Polish` | 点「让模型重写」或 `goal report --llm` | 一次只读会话，把事实改写成 3–6 句中文；不允许补充事实 |

- 规则版把机器原因映射成人话，例如 `review: blocking finding` → “审查发现了需要修的问题”，
  `interrupted: outcome unknown` → “执行进程中断，结果未知”。
- 模型版只写 `<state>/report.md`，不改任何状态；有 scheduler 占用目录时拒绝执行
  （`aoenv.Setup` 会清理遗留会话）。
- 接口：`GET /api/report`（规则版 + 上次模型版）、`POST /api/objectives/{id}/report`。

真实输出（同一目录）：

```
本次共安排 1 个任务，目前已经全部处理完毕：其中 1 个通过验收，0 个正在执行，
0 个等待执行，也没有被卡住的任务。该任务已完成并通过验收，相应的交付内容也已生成。
后续可以查看交付说明和交付文件，了解本次的具体内容。没有其他需要处理的事项。
```

## 3. 界面

- 视觉：暖中性底色、白色卡片、12px 圆角、轻阴影；主区域限宽 52rem。
- 文案：全部中文状态（已完成/执行中/待执行/被卡住/已替换），不再出现 `accepted/running`。
- 顶部新增「汇报」卡片，显示规则版或模型版，附生成时间。
- 任务节点只保留 ID、产物名、状态；尝试次数、摘要哈希收进「技术细节」。
- 时间线改成中文动作（开始执行/通过验收/审查未通过…），不再整段展示产物内容。
- 任务图：连线放到节点下层，跨列依赖从上方通道绕行，不再穿过方框。

## 4. 验证

```
128 项测试 PASS（+3：acceptance 阻断、汇报规则版、原因翻译）
go test -race   PASS
go vet          exit 0
```

界面在 1440px 与 800px 下均正常；浏览器控制台无报错。
