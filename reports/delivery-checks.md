# 交付整体验收（2026-11-04）

## 问题

在此之前，“通过”是**逐任务**的：每个任务在自己的 sandbox 里跑命令验收。
但交付物是 `out/` 里的**一组文件**，从来没有作为一个整体被执行过。
三个文件各自合法、组合起来不可用（缺文件、路径错、互相覆盖）不会被发现。

## 做法

- 新增 `delivery.VerifyAssembled(st, dir)`：把 `out/` 复制到临时目录，
  对每个 accepted 任务重跑它的 `command` 验收，返回逐条结果。
- 复制而不是原地运行：验收命令可能写文件（例如 `node summarize.js` 会写 `summary.md`），
  不能让它改动交付物。
- `finishGoal` 改为：组装 → 整体验收 → 有失败则 objective 置 `failed`，不生成 `.project` 快照；
  全通过才 `completed`。
- 结果写入 `delivery.md` 的「交付验收」表，`delivery.json` 带 `checks[]`。
- 命令执行与逐任务验收共用 `verify.RunCommand`（同一份白名单、同一套 PATH 处理），
  避免两套规则漂移。

## 真实运行

待办应用（`/private/tmp/l2-delivery-140744`，3 任务，reviewer 开启）：

```
final: completed
T1 accepted / T2 accepted / T3 accepted   （全部 attempts=1）

## 交付验收（在 out/ 的副本上运行）
| Task | 命令 | 结果 |
| T3 | `node test.js` | 通过 |

out: app.js index.html test.js
独立复核：cd out && node test.js → 11/11 tests passed
```

## 失败路径

单元测试 `TestVerifyAssembledReportsFailingCommand`：交付树里没有 Go 源码时
`go test` 失败，check 必须报 `OK=false` 并带上命令输出，而不是沿用逐任务验收的结论。

## 边界

- 只重跑 `command` 类型验收；`exact/contains` 是对**单个产物内容**的检查，已经在任务验收时做过，
  在组装树上重复没有意义。
- 命令在副本上运行，副作用不会进入 `out/`。
- 仍然没有：跨任务的集成测试声明（例如“整个应用端到端可用”），
  现在只能复用各任务已有的命令验收。
