# Handoff
Updated: 2026-11-05T20:15:00+08:00
Status: p3_0_2（验收缓存 / 消息同步 / SSE / Handoff / Context）

## Goal
本目录负责产品设计、上游证据与控制面；可运行代码在 `~/dev/projectos-kernel`。

## Done
- P1/L0/L1/L2：目标 → 计划 → 人工确认 → 并行执行 → 三层验收 → 交付 → 恢复；
  中文驾驶舱、汇报人、成果区、继续改进。
- 默认 OpenCode CLI 执行器；按角色配置模型；AO 保留但当前不可用于已注册模型。
- **M1/M2**：Conversation SQLite、OpenCode 2.2.5 私有 v2 Server、SSE、工具 ask 审批、
  `/chat` 工作台、历史恢复；普通聊天不创建 Objective。
- **P2**：
  - Workspace 多目标：`projects/<id>/` 独立 DB/锁/work/out，一个 serve 聚合，可并行。
  - 执行前审批：高风险任务 / command 校验必须 `--approve-risk`，驾驶舱弹窗展示风险、命令、预算。
  - 文本产物 diff：最新尝试 vs 上次交付/上一尝试，任务详情查看。
  - 会话续跑：捕获 session id，重试传 `--session`；仍用新工作目录。
  - work/ 保留最新 20，`goal prune --keep N` 可调。
  - token 读取实测值，缺失才标注估算。
  - macOS 原生外壳：Objective-C + WKWebView，`desktop/build.sh`。

## Verified
- 213 项 Go 测试 + `-race` + `vet` 全绿；kernel 审计 0 fail / 1 warn（根目录布局）。
- 同一 Workspace 创建两个目标、切换、分别读取详情/汇报/事件；浏览器内完成。
- 真实目标 `hello world`：规划 → 执行 → reviewer → acceptance → 交付，token 为实测值。
- 审批弹窗在含 high risk + `python3 -m pytest` 的计划上正确列出并阻止未勾选执行。
- diff 与桌面 `--check` 均验证；`/files/{objective}/{path}` 正常。
- 已有证据：场景 A/B/C/D、杀 serve 后任务继续、取消 14s 生效、三个真实目标交付。
- 第三轮审查 5 条、6 类失败注入均保留；测试从 152 增至 213。

## Open
- SSE 推送；Windows/Linux；逐工具调用审批；二进制行级 diff。
- 桌面壳未签名/未公证；端口固定 8799。
- 会话续跑不续工作目录；AO + 已注册模型仍不可用。
- 未注入：取消与调度器完成并发交错、磁盘满/只读挂载、运行中 SQLite 失效。
- reviewer 首次阻断率偏高；日志无轮转。

## Active
无 daemon/worker 进程。工作区：`~/dev/projectos-kernel`（产品代码）、本目录（设计）。
演示服务如仍在：`kernel serve --dir <D> --stop`。

## Next
用 Workspace 并行跑 2–3 个真实目标，观察审批摩擦与 diff 可读性；优先修计划质量。
