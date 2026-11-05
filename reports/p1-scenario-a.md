# 场景 A：待办应用（2026-11-03，最终结果：通过）

## 目标

> 创建一个简单的待办事项 Web 应用：index.html 与 app.js，支持新增、完成、删除，数据存 localStorage；
> 再写 test.js 用 Node 断言验证核心函数。

## Planner（真实模型，`deepseek-flash`）

| Task | 依赖 | 产物 | 验收 |
|---|---|---|---|
| T1 | — | app.js | contains(module.exports) + contains(localStorage) |
| T2 | T1 | index.html | contains(app.js) + contains(<script) |
| T3 | T1 | test.js | **command: node test.js** |

## 执行（真实 AO + OpenCode 2 + reviewer）

```
T1 accepted (attempts=1)
T2 accepted (attempts=1)
T3 accepted (attempts=1)
objective completed
delivery.md + out/ 三个文件
```

## 独立复核

```
$ cd <state>/out && node test.js
14/14 tests passed
exit=0
$ grep 'src="app.js"' index.html
src="app.js"
```

`out/` 由 Kernel 从**已验收产物**重新组装，不是某个 worker 的 worktree 原样拷贝。

## 走向通过过程中修掉的问题

| 问题 | 修复 |
|---|---|
| 下游 worktree 没有上游产物，`node test.js` 报 Cannot find module | 验收时在临时 sandbox 中materialize 本任务产物 + 依赖产物 |
| validator 只查文件内容，测试跑不起来也算通过 | 新增 `command` validator（白名单，无 shell），检查 exit code |
| CLI 的 PATH 过窄，找不到 `node` | 命令解析会补 `/opt/homebrew/bin`、`/usr/local/bin` |
| AO 限制 prompt ≤16KiB，reviewer 带大产物被拒 | 依赖注入与 reviewer 内容都做 4–6KB 截断 |
| reviewer 把依赖文件当"额外文件" | reviewer 与 worker 提示词都声明依赖文件已验收、不要重建 |
| planner 把 index.html 折叠进 app.js 任务 | 提示词要求"目标里点名的每个文件都必须是 task artifact" |

## 结论

场景 A 通过：计划可执行、依赖正确、产物可组合、测试真实运行通过。
