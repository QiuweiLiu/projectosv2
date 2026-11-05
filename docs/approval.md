# 执行前审批

## 目标

模型可以提出计划，但高风险动作不能在无人看一眼的情况下执行。当前实现是**执行前审批**：
在 `goal run` 启动前，把会执行的东西摊开给人看，并要求显式确认。

## 什么会触发审批

满足任一条件即需要 `--approve-risk`：

1. 计划里某个任务的 `risk` 是 `high`；
2. 计划里某个任务带 `command` 校验器（会在当前用户权限下执行命令）。

普通任务、纯内容校验（`exact` / `nonempty` / `contains` / `regex`）不需要额外确认。

## CLI

```sh
kernel goal run --dir D/projects/obj-123 --reviewer
# 输出：
# this plan needs explicit approval before it can run:
#   - T1 is marked high risk
#   - T1 runs: python3 -m pytest
# review the actions, then rerun with --approve-risk

kernel goal run --dir D/projects/obj-123 --reviewer --approve-risk
```

## 驾驶舱

点“开始执行”会先弹出确认框：

- 列出每个高风险任务和它要执行的命令；
- 显示预算输入（最多运行次数、最多审查次数、token 预算）；
- 勾选“我已确认上述高风险任务与命令”后，“批准并执行”才可用。

请求体对应：

```json
{
  "reviewer": true,
  "approveRisk": true,
  "maxRuns": 8,
  "maxReviews": 8,
  "maxTokens": 600000
}
```

## 这不是什么

- **不是逐工具调用审批**：OpenCode CLI 内部读了哪个文件、调了哪个工具，Kernel 不拦截。
- **不是沙箱**：`command` 校验器有白名单（`node <script>`、`npm test`、`pytest` 等），
  但运行身份就是当前用户，能碰到的文件它都能碰到。
- **不是权限系统**：worker 仍以 `--auto` 运行；planner / reviewer / acceptance / reporter
  仍是只读会话。审批只决定“这次 run 是否放行”，不改变角色权限。

真正的逐工具拦截需要执行器暴露工具调用回调，或者把 worker 放进独立沙箱；当前版本没有做。
