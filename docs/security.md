# 安全边界（当前实现）

这份文件写清楚 Kernel **保护什么、不保护什么**。不要把它当成一个沙箱。

## 信任模型

- 单机、单用户。Kernel 以启动它的那个用户身份运行。
- 计划由模型提出，**必须由人确认**才会执行；`createGoal` 的自动执行只是把
  plan → confirm → run 串起来，不是取消确认。
- 目标文本和计划字段都来自模型输出，按不可信输入处理：路径校验、允许列表、
  上限、DAG 校验都在 Kernel 侧做，不依赖模型自觉。

## 命令验收不是沙箱

`command` 验收（`node test.js`、`go test ./...` 等）在临时目录里以当前用户身份运行：

- 能读当前用户能读的一切（包括 `~/.ssh`、`.env`）；
- 能联网；
- 能写临时目录之外的路径；
- 能启动子进程。

允许列表只解决一件事：**计划不能注入任意 shell 命令**（拒绝 `; | > $(` 与 `-` 参数，
只允许固定形态）。它不限制“合法命令做什么”。

需要真正隔离时，只有两条路：

1. 在专用用户/容器里运行整个 Kernel（推荐，改动最小）；
2. 引入 OS 级 sandbox（macOS seatbelt / Linux bubblewrap），目前未实现。

## HTTP 表面

- 读接口（`/api/objectives`、`/api/events`、`/api/tasks/{id}`、`/files/`）**没有认证**。
  因此默认只允许绑定回环地址；`--allow-remote` 等于把项目内容暴露到网络，需自行承担。
- 写接口（创建/计划/确认/执行/取消）需要 `X-Kernel-Token`，并校验 Origin 的
  scheme + host（精确匹配，不用前缀）。
- token 存在 `<state>/control-token`（0600）；`serve.log` 也是 0600，且 daemon 模式
  不再把 token 打进日志。
- 界面支持 `#token=...` 方便本地打开演示链接：fragment 不会发给服务器，读入后写入
  localStorage 并从地址栏移除；带 token 的链接等同于凭据，不要分享。
- 写操作不是 Kernel 进程直接写库，而是派生 `kernel goal ...` 子进程，保持单写者。

## 状态目录

- `kernel.lock` / `serve.lock` 是进程生命周期锁：进程死锁即释放，不依赖 PID 文件。
- `serve.json` 记录 pid/addr/instance；`--stop` 会先向服务查询 instance，不匹配就拒绝发信号，
  避免 PID 复用误杀。
- `/files/` 只服务 `out/` 内的普通文件：路径清理后仍限制在该目录，符号链接解析后必须仍在树内；
  不做上传、不做写入。它暴露的是交付内容本身，不是整个状态目录。
- 状态目录本身按普通文件权限创建（目录 0755），同一台机器上的其他用户通常读不到
  用户主目录下的文件；但不要把状态目录放在共享位置。

## CLI 执行（--executor opencode）

- `opencode run --standalone --auto` 会**自动批准**未被显式拒绝的权限，包括 shell；
  也就是说 worker 可以在任务目录里执行命令。这与 AO 的 worker 行为一致，但需要知道。
- 任务目录是 `<state>/work/...`，不是宿主项目目录；CLI 进程以当前用户身份运行，
  仍可访问全盘（同“命令验收不是沙箱”的边界）。
- `--standalone` 使用私有 server，不与桌面版 OpenCode 抢后台服务。

## 汇报人

`kernel goal report --llm` 会让模型把状态改写成中文汇报。它只读状态、只写
`<state>/report.md`，不参与任何判定；当有 scheduler 占用状态目录时命令会拒绝执行
（`aoenv.Setup` 会清理遗留会话，不能在有任务运行时调用）。

## 已知缺口

- worker 进程（AO/OpenCode）本身没有资源限制（CPU/内存/网络）；quarantine 只阻止继续调度，
  不会杀死已经在跑的进程。
- 崩溃后遗留的 AO chat host 由下一次 `aoenv.Setup` 清理，清理前可能仍在跑。
- 日志没有轮转，长期运行会持续增长。
