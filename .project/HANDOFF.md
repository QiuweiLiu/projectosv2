# Handoff
Status: p2_usability（多目标 / 审批 / diff / 原生壳）

## Goal
单机单用户的目标驱动系统：目标 → 计划 → 确认 → 执行 → 三层验收 → 交付 → 继续改进；
Kernel 是唯一写者，人在关键处确认。默认用 OpenCode CLI 执行，可用任意已注册模型。

## Done
- **P1.1–P1.3**：objective 状态机、plan 版本、并行 DAG、有界重规划、崩溃恢复、`out/` 交付。
- **L0/L1/L2**：`serve --daemon` + `serve.lock` + instance 身份、读写 API、事件游标；中文 cockpit
  （汇报卡片、成果区、任务图、证据/审查、时间线、confirmation/run/cancel、继续改进）。
- **验收三层**：确定性检查 → reviewer（查缺陷）→ acceptance（查完成度）。
- **汇报人**：模型版中文汇报（状态哈希失效）、审查意见中文解释（findings 哈希缓存）。
- **CLI 执行器**：隔离目录、进程组取消、崩溃清理、权限模式；AO 仅作 `--executor ao` 备用。
- **按角色模型**：`goal models` + 界面五个下拉框；列表缓存自 harness；flag > models.json > 默认。
- **P2（2026-11-05）**：Workspace 多目标（独立 DB/锁/out，可并行）、执行前审批
  （高风险/命令/预算）、文本产物 diff、`--session` 会话续跑、work 保留 20 + `goal prune`、
  token 读取实测值、macOS 原生外壳（Objective-C + WKWebView）。

## Verified
- **174 测试 + `-race` + `vet` 全绿**；真实运行后无残留进程。
- 场景 A 待办应用、B CSV（总额 60）、C 真实重规划、D SIGKILL 恢复均通过。
- CLI 执行器在 `opencode-go/minimax-m3.1-flash` 上跑通：计划 → worker → reviewer →
  acceptance → 交付验收。
- 继续改进：CSV 目标追加 `total.json`，一次通过，交付 4 个文件。
- 全量审查 34 条：已修 25，未修 9（见 Open）。
- 第二轮审查 10 条、第三轮 5 条全部修复；第三轮的三个故障场景都有测试：
  关库后 quarantine 走文件兜底、`.project` 不可写时目标保持 active、交付报告不可写时保持 active、
  SQLite 损坏被拒绝、陈旧 cancel 不影响下一轮、空闲取消不留标记。
- P2 真实验证：同一 Workspace 创建两个目标并切换；审批弹窗列出高风险任务与命令；
  任务详情 diff；桌面 `--check` 启动/请求/停止。
- 证据：`reports/` 与 `docs/{workspace,approval,desktop,executor}.md`。

## Open
- **命令验收不是沙箱**：允许列表只防注入，不防合法命令读全盘/联网（`docs/security.md`）。
- 计划字段 `scope/inputs/allowed/forbidden/acceptance/priority` 不参与判定（`docs/plan-semantics.md`）。
- 会话续跑只续对话上下文，不续工作目录；重试仍在新目录。
- 审查首次阻断成本高；AO 的 `opencode-v2` harness 在真实 HOME 下认证检查失败，
  v1 需要 tmux——所以“AO + 已注册模型”目前不可用。
- 未做：SSE、Windows/Linux、逐工具调用审批、二进制行级 diff；桌面壳未签名/未公证。
- 未注入：取消与调度器完成的进程级交错、磁盘满/只读挂载、运行中 SQLite 失效。

## Active
无 AO/opencode 任务进程。演示服务可能开着：8787（CSV 样例）、8789（成果区演示），
用 `kernel serve --dir <D> --stop` 关闭。未修改其他项目或全局配置。

## Next
1. 用 Workspace 并行跑 2–3 个真实目标，观察审批摩擦与 diff 可读性。
2. 决定 AO 的去留：装 tmux 试 v1，或彻底移除 AO 依赖。
3. 视需要做 SSE；处理不可信目标前先做 OS 级隔离。
