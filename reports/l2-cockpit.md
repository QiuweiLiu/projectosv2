# L2 界面（Cockpit）验收记录（2026-11-04）

范围：目标列表 → 计划预览 → 任务图 → 证据/审查 → 确认/执行/取消。
不 fork AO、不重建 Chat；界面是 `serve` 内嵌的一页，无前端构建步骤。

## 1. 实现

| 区域 | 内容 |
|---|---|
| 左栏 | 目标列表（状态、已验收/总数）、新建目标、control token |
| 头部 | 标题、状态、plan 版本与状态、是否已交付、按状态显示的操作按钮 |
| 进度 | 已验收 / 运行中 / 待执行 / 阻塞 / 已取代 的计数 + 验收比例条 |
| 任务图 | 按依赖深度分列，SVG 曲线连边；节点按状态着色；点节点看详情 |
| 任务详情 | 产物、依赖、尝试次数、阻塞原因、证据（digest/时间）、审查 findings（P0–P3） |
| 时间线 | 最近 100 条事件 |
| 写操作 | `confirm` / `run` / `cancel`；未开 `--control` 时全部隐藏 |

技术选择：`cmd/kernel/ui.html` 单文件 + `//go:embed`；无框架、无构建、无额外依赖。
轮询 3s，不做 SSE（未实现，见下）。

## 2. 真实流程（在浏览器里点完的）

状态目录 `/private/tmp/l2-ui-105233`：

```
新目标：创建 hello.txt，内容为 hello world
  → 创建并生成计划（模型，约 20s）
  → 计划预览：T1 / hello.txt / pending（1 节点）
  → 确认计划 → active
  → 执行 → T1 accepted（attempts=1）
  → objective completed，已交付 out/

cat out/hello.txt  →  hello world
```

截图核对：列表 1/1 已验收、进度条全绿、节点 accepted、证据 digest 与时间、
审查“无阻断性发现”、时间线 `T1 started` / `T1 accepted`。

## 3. 取消流程（浏览器）

目录 `/private/tmp/l2-cancel-105937`（2 任务，T1→T2）：

```
执行中（T1 running，T2 pending）
→ 点“取消”（确认框）
→ objective cancelled
→ T1 blocked / T2 blocked，原因 "cancelled: cancelled from the cockpit"
→ 时间线记录 objective_cancelled
```

## 4. 只读模式

不加 `--control` 启动：`mode=read-only`，新建按钮隐藏、token 输入隐藏、
操作按钮不渲染，读接口正常。

## 5. 这一轮发现并修掉的问题

| 问题 | 原因 | 修复 |
|---|---|---|
| 空目录打开页面报 `unable to open database file (500)` | 只读打开一个不存在的 DB，连接进入坏状态 | `serve` 启动时若 `kernel.db` 不存在则先建空库（只建 schema，不写任务状态） |
| 计划生成后看不到任务图 | detail 用 `objective.planVersion`（确认后才递增）查计划 | 回退到最新 plan；未执行时用 plan spec 合成 pending 节点做预览 |
| 顶部显示 `plan v0` | 同上，用的是 objective 版本 | detail 暴露 plan 记录自身的 version |
| 失败只显示 `plan failed: exit status 1` | `runGoal` 丢了子进程错误文本 | 错误里附带 `goals.log` 尾部 |
| 取消后 job 状态显示 `run failed` | 只在 create 路径判断了取消，`/run` 路径没判断 | 抽出 `settleRun`，两条路径共用 |
| favicon 404 / 密码框警告 | 细节 | 内联空 favicon；token 放进 form |

## 6. 测试

118 项测试 PASS；`-race`、`vet` 见 `evidence/`。

## 7. 仍未做

- 事件推送（SSE）；现在是 3s 轮询 + 游标。
- 多目标并存（一个状态目录一个 objective）。
- 产物内容预览 / diff（只有路径与 digest）。
- 审批类操作（权限、预算、外部副作用）尚无界面；当前只有目标级 confirm/run/cancel。
- Desktop 原生外壳：现在就是本地网页，没有 Electron/Tauri。
