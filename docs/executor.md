# 执行器：opencode CLI（默认）与 AO（可选）

## 为什么默认换成 CLI

Kernel 需要“用 OpenCode 里注册的模型”。AO 的 `opencode-v2` harness 在真实环境下
认证检查失败（`auth_check_failed`），v1 harness 又依赖 tmux；而
`opencode run --standalone` 不抢桌面版的后台服务，能直接使用已注册的 provider/model。

## 能力对照

| 能力 | AO | opencode CLI |
|---|---|---|
| 隔离工作目录 | git worktree | 有 repo 时用 `git worktree add`，否则目录拷贝（跳过 `.git`） |
| 启动 / 观察 / 收集 | session API | 子进程 + JSONL 事件流 |
| 取消 | session cancel + 进程清理 | 进程组 `SIGTERM` → 5s 后 `SIGKILL` |
| 崩溃清理 | 清理 chat-host | 每任务写 `.kernel-run.pid`；启动时按进程 cwd 校验后杀进程组 |
| 权限模式 | `approvalMode` | worker 传 `--auto`；审查/规划不传（默认拒绝越权写） |
| 用量 | 后端回报 | 从 `step_finish.part.tokens` 读取实测 tokens；没有时标注“估算” |
| 改动文件 | worktree diff | 与初始快照的哈希差异 |
| 会话续跑 | 支持 | 支持 `--session`：重试继续同一会话，但仍是新工作目录 |

## 已知代价

- 会话续跑只续对话上下文，不续工作目录；重试仍在新目录，提示词会要求忽略旧路径。
- `<state>/work/` 默认保留最新 20 个尝试目录；`goal run --keep-work N` 可调，
  `goal prune --dir D --keep N` 可手动清理，运行中的目录不会被删。
- 清理依赖 `lsof` 读取进程 cwd；没有 `lsof` 时退回命令行匹配，准确性下降。
- 进程以当前用户身份运行，仍不是沙箱（见 `docs/security.md`）。

## 什么时候还用 AO

- 需要 AO 的多轮会话/客户端复用能力；
- 或者要继续用 DeepSeek + AO 的既有链路。

`--executor ao` 保留，行为不变。


## 交互式聊天的执行器

任务执行仍走 `opencode run --standalone`。交互式聊天不走这个 CLI 包装，而是由
`internal/opencodeserver` 启动一个私有 `opencode serve`，使用 v2 Session API 和 SSE
（详见 `docs/chat.md`）。它和任务执行器共享已注册的模型，但不共享 Task/Run 状态，
也不使用 `--auto` 的 worker 权限模式。
