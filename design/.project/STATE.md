# State

## Current Stage
Kernel 已到 P3.0–P3.2：P2 任务内核 + 持久化聊天 + Goal Handoff + Project Context。
单机单用户 pilot 级；边界见 `docs/readiness.md`、`docs/chat.md`。

## Verified Facts
- 项目根：/Users/liuqiuwei/Documents/opencode/projectos-2.0（独立 git main @ dbc449f）；产品代码在 `~/dev/projectos-kernel`，验证工作区在 `~/dev/projectos-spike`，均不在 iCloud。
- 上游固定版本：Research Agent OS `b7ac8a9a`；Orchestrator `d2c997ba`；OpenCode 2.2.5（桌面 CLI `opencode v2.2.5`）。
- 真实执行：Agent Orchestrator daemon + `opencode-v2` + `deepseek/deepseek-flash`；A∥B→C、P1 阻断、定向复审均实测通过。
- Kernel 能力：SQLite 原子结算（绑 run/attempt）、DAG、产物级验收、预算/超时 fail-closed、evidence、独立 reviewer（`approvalMode=default`）、能力包（simple/research）、CLI（run/status/serve）。
- 质量：272 项 Go 测试 + `-race` + `vet` 全绿；真实目标、双目标切换、审批、diff、桌面 `--check` 已验证。
- 独立审查：0 P0 / 14 P1 / 9 P2，已修 17 项，其余 6 项有明确处置记录。

## Boundaries
- 不修改 research-agent-os 与上游工作副本；不改全局 OpenCode/DSH 配置。
- 多机明确不做（单 writer SQLite + 本机 daemon）。
- token 优先用 provider 实测值；只有后端缺失时才 chars/4 估算并标注。
- 审批是执行前门禁，不是逐工具拦截；command 校验仍不是沙箱。
- Reviewer 不是内核级隔离，只是 `approvalMode=default`。

## Open
- reviewer 稳定性无量化基线（有漏报/误报各一次）。
- 网络中断、provider 5xx、host 崩溃未注入；磁盘满/只读挂载、运行中 SQLite 失效未注入。
- 跨平台（Windows/Linux）未验证；桌面壳未签名/未公证。
- 未做 SSE、逐工具审批、二进制行级 diff。

## Evidence
`~/dev/projectos-kernel/reports/{real-runs,review-2026-11-03}.md`、`docs/readiness.md`。
