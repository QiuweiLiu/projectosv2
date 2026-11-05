# ProjectOS Kernel (Go)

## Goal
把 P0/P3 验证过的任务控制语义落成可运行、可测试的 Go Kernel：Task/Run 状态机、依赖 DAG、Quality Gate、有限失败策略。

## Scope
`~/dev/projectos-kernel` 本地 APFS，独立于 iCloud。只做控制面，不做执行。

## Boundaries
- Kernel 拥有 Task/Run 状态；Backend 只产出 Run 结果与产物。
- 不直接 spawn 进程；一切执行经 `internal/exec.Backend`。
- 不引入 UI、能力包、队列中间件。

## Deliverables
`internal/{domain,store,engine,exec}`、`cmd/demo`、`evidence/`。
