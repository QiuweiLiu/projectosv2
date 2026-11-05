# Kernel 本地 API 与后台服务（L0 + L1）

本文件记录 `kernel serve` 的后台模式与只读/写入 API。写操作全部通过
`kernel goal ...` 子进程完成，Kernel 仍是状态目录的唯一写者。

一个 `--dir` 现在是一个 **Workspace**：根目录可以继续放一个旧式单目标数据库，
每个新目标存放在 `D/projects/<objective>/`，各自拥有独立数据库、锁、`work/` 和 `out/`。
Kernel 仍然遵守“一目录一目标”，多目标由 Workspace 层组合，因此调度、交付和 replan
的单写者假设不变。

## 1. 运行模式

```sh
kernel serve --dir D                        # 前台，只读看板
kernel serve --dir D --control --daemon     # 后台，带写入（token 保护）
kernel serve --dir D --status               # pid / addr / control / alive
kernel serve --dir D --stop                 # 停掉 HTTP 服务
```

会话文件 `D/serve.json` 记录 `pid / addr / control / started`；日志在
`D/serve.log`。token 在 `D/control-token`（0600，重启后保持不变）。

`--stop` **只停客户端**：已经由它启动的 `goal run` 在独立会话中继续执行，
不会被终止；要中止任务必须用 `kernel goal cancel`。这是
「关闭客户端不影响已托管任务」的实现方式，也是它的验收方式。

## 2. 读接口（不需要 token）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/status` | 兼容旧看板的状态摘要 |
| GET | `/api/tasks/{id}?objective=…` | 单个任务的 evidence / review / 错误；若已有中文解释则带 `explanation` |
| GET | `/api/events?objective=…&after=N&limit=K` | 事件页，返回 `{events, next}`；用 `next` 续传，不重复 |
| GET | `/api/objectives` | 聚合 Workspace 内全部目标 + 任务计数 |
| GET | `/api/objectives/{id}` | 目标详情：当前计划、任务行、审批原因、交付状态、是否请求取消 |
| GET | `/api/diff?objective=…&task=…` | 最新尝试 vs 上次交付/上一尝试的文本 diff |
| GET | `/api/models` | 各角色模型设置 + 缓存的模型列表 |
| GET | `/api/report?objective=…` | 中文汇报（模型版优先）+ 成果文件列表 |
| GET | `/files/{objective}/{path}` | 只读读取该目标 `out/` 下的文件；支持 Range |

计数来自任务状态，不来自 Agent 自报。

## 3. 写接口（需要 `--control` + `X-Kernel-Token`）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/goals` | `{goal,title,runNow,reviewer}`，创建目标；`runNow` 会串行执行 plan→confirm→run |
| POST | `/api/objectives/{id}/confirm` | 确认当前草案计划 |
| POST | `/api/objectives/{id}/run` | `{reviewer,approveRisk,maxRuns,maxReviews,maxTokens,runTimeout}`；高风险任务或命令校验需要 `approveRisk=true` |
| POST | `/api/objectives/{id}/cancel` | `{reason}`，协作式取消 |
| POST | `/api/models` | `{role,model}` 保存一个角色；`{refresh:true}` 刷新模型列表（需 token） |
| POST | `/api/objectives/{id}/report` | 让模型重写汇报（写入 `report.md`） |
| POST | `/api/objectives/{id}/continue` | `{goal}`：在已完成/失败的目标上追加任务，生成新计划版本 |
| POST | `/api/tasks/{id}/explain?objective=…` | 让汇报人把该任务的审查意见翻成中文（按 findings 哈希缓存） |
| GET | `/api/jobs` | 各目标最近一次动作的状态（内存态，非权威） |

未带 token → 401；`--control` 未开启 → 403。

## 4. 多目标 Workspace

```
D/                           # Workspace 根
  serve.json / control-token
  models.json                # 全 Workspace 共用的角色模型
  kernel.db                  # 可选：旧式单目标（兼容）
  projects/
    obj-.../kernel.db        # 每个目标独立数据库与锁
    obj-.../work/            # 每个目标独立的尝试目录
    obj-.../out/             # 每个目标独立的交付
```

- `GET /api/objectives` 扫描根目录与 `projects/*`；同一目标 ID 只出现一次。
- 除列表外，读接口都要求 `?objective=<id>`；只有一个目标时可省略，兼容旧链接。
- 每个项目目录仍有自己的 `kernel.lock`，因此两个目标可以**并行执行**，互不抢预算。
- `goal create` 的 ID 使用纳秒时间戳，同一秒创建多个目标不会撞 ID。
- `docs/workspace.md` 记录设计取舍与 CLI 用法。

## 5. 验收的三层

| 层 | 谁做 | 判断什么 |
|---|---|---|
| 确定性验收 | Kernel（`verify`） | 产物内容、命令退出码 |
| 审查 | reviewer 会话 | 有没有缺陷（P0/P1 阻断） |
| 验收判断 | acceptance 会话 | 是否真的完成了任务（缺需求即阻断） |

acceptance 与 reviewer 是两次独立会话，结果都写 `reviews` 表（kind=`acceptance`），
同版本各限一次。`--acceptance=false` 可关闭。

## 6. 继续改进（continue）

已完成或失败的目标可以继续，但规则很硬：

- 已通过的任务**原样保留**，不会重跑，也不允许被改写（模型改写会被 `NormalizeAccepted` 还原）；
- 只能新增最多 5 个任务，产物必须与已有产物不同（一个任务一个产物、全局唯一）；
- 新计划立刻成为当前版本，但**不会自动执行**；人在界面上确认后再点执行。

原因：已验收的成果是证据，继续改进意味着“在证据之上追加”，不是“重做一遍”。

## 7. 取消的语义

取消是**请求**，不是写：

1. 调用方在状态目录写 `cancel` 文件并立即返回 `cancelling`；
2. 谁持有状态目录（scheduler）在下一次循环读取并删除该文件；
3. 它调用 `engine.Abort` 取消 AO 会话，把未完成任务置为 `blocked`，目标置为 `cancelled`。

没有 scheduler 在跑时，`kernel goal cancel` 直接完成上述最后一步。

已经 `accepted` 的任务保持 accepted；已经 `completed` / `failed` 的目标不会被
改写成 `cancelled`——取消不能重写已有结论。

## 8. 单写者与恢复

`store.Open` 在打开数据库的同时获取**该目标目录**的 `kernel.lock`。打开会执行恢复
（running Run → unknown，running Task → blocked）。Workspace 的多个项目各有一把锁，
因此可以并行；同一个项目仍然只有一个写者。因此：

- 读命令（`status/show/export/resume/serve`）使用只读打开，不触发恢复；
- 第二个写进程会得到 `ErrLocked`，而不是在别人运行时改写状态。

## 9. 界面（Cockpit）

`GET /` 是 `cmd/kernel/ui.html`（`//go:embed`，无框架、无构建）。页面每 3s 轮询：

- 左栏：目标列表（状态、已验收/总数）、新建目标、token；
- 头部：状态、plan 版本、是否已交付、按状态显示 confirm / run / cancel；
- 进度：按任务状态计数，不做估算百分比；
- 任务图：按依赖深度分层 + SVG 连线；节点颜色即状态；
- 任务详情：`/api/tasks/{id}?objective=…` 的证据、findings 与“对比改动”；
- 时间线：`/api/events?objective=…&limit=100`；
- 执行前审批：`/api/objectives/{id}` 带 `requiresApproval` / `approvalReasons`，前端弹窗。

页面可以从 `#token=...` 读取控制令牌（fragment 不会发给服务器，仅用于本地打开演示链接），
读入后存入 localStorage 并从地址栏移除。目标结束时若还没有汇报，页面会自动请求一次模型汇报。

成果文件按扩展名分类：图片/视频/音频/PDF 直接渲染，文本显示预览与「打开」，其他给下载链接。
`/files/` 只允许访问 `out/` 内的普通文件，符号链接与 `..` 一律拒绝。

未确认的计划也会显示：detail 回退到最新 plan，并用 plan spec 合成 pending 节点做预览。
写操作在 `--control` 关闭时不渲染。`serve` 在 `kernel.db` 不存在时先建空库，避免只读连接报 500。

## 10. 尚未提供

- 事件推送（SSE）；当前是轮询 + 游标。
- Windows / Linux 桌面外壳（当前只有 macOS 原生壳）。
- 逐工具调用的审批拦截；当前是执行前审批（风险、命令、预算），不是沙箱。
- 产物二进制 diff；二进制只标记“有变化”，不给行级差异。
- 取消与调度器完成的进程级并发交错、磁盘满/只读挂载、运行中 SQLite 失效没有专项注入。
