# 模型选择

## 模型从哪里来

Kernel 不内置任何模型。它把 `provider/model` 字符串放进 AO 的会话请求
（`internal/exec/ao/ao.go` 的 `"model": b.cfg.Model`），AO 再交给 OpenCode harness。
能不能用，取决于 **harness 运行所在的 OpenCode 环境**里有没有注册这个 provider/model。

```
Kernel --model=<provider/model> → AO session → OpenCode harness → provider
```

## 当前默认

所有角色默认 `opencode-go/deepseek-v4.1-flash`（2026-11-08 起；此前为
`deepseek/deepseek-flash`）：

| 角色 | 参数 |
|---|---|
| 规划、worker | `--model` |
| 审批（聊天风险裁判） | `approval` 角色 |
| reviewer | `--review-model`（缺省跟随 `--model`） |
| acceptance | `--acceptance-model`（缺省跟随 `--model`） |
| 汇报 / 解释 / 继续改进 | `--model`（各自的子命令） |

```sh
kernel goal plan --dir D --planner model --model opencode-go/kimi-k3.1-pro
kernel goal run  --dir D --model opencode-go/minimax-m3.1-flash \
                       --review-model opencode-go/kimi-k3.1-pro \
                       --acceptance-model opencode-go/kimi-k3.1-pro
```

Workspace 模式下，`serve` 用 `KERNEL_MODELS_FILE` / `KERNEL_MODELS_CACHE` 把根目录的
`models.json` / `models-cache.json` 传给每个项目子进程；直接对项目目录运行 `goal` 时，
也可以用这两个环境变量覆盖。

聊天工作台右上角的“模型”下拉设置 `chat` 角色；旁边的“设置”打开各角色面板
（含 `approval`，用于灰区审批的轻量风险裁判），
可以分别设置 Chat / Planner / Worker / Reviewer / Acceptance / Reporter。列表来自缓存，
“刷新列表”会重新执行 `opencode models`（遵循 `OPENCODE_BIN`）。列表为空时页面会自动刷新一次。
Planner / Worker / Reviewer / Acceptance / Reporter 也可以在 Cockpit 的“模型”卡片里设置。

刷新命令优先使用 `opencode models`；若失败再回退到 `opencode --standalone models`。
插件激活偶尔较慢，命令会短暂重试；两次都拿不到模型时不会用空结果覆盖已有缓存。

## 思考强度（variant）

每个角色可以单独设置思考强度；它保存在 `models.json` 的 `variants`，运行时组合成
OpenCode 原生的 `provider/model#variant`：

```json
{
  "worker": "opencode-go/deepseek-v4.1-flash",
  "approval": "opencode-go/deepseek-v4.1-flash",
  "variants": { "worker": "high", "reviewer": "max" }
}
```

预设：`minimal` / `low` / `medium` / `high` / `max`（具体支持哪些由模型决定）。
空值表示使用模型默认。回退规则与模型一致：

- `reviewer` / `acceptance` 未单独设置模型时，跟随 `worker` 的 variant；
- `chat` 未单独设置模型时跟随 `worker` 的 variant；
- 显式设置了模型时，只使用该角色自己的 variant。

聊天工作台的 variant 在**创建会话时**写入，修改后对新会话生效；已有会话继续使用原设置。
CLI 可用 `kernel goal models --dir D --variant worker=high`，API 用
`POST /api/models {"role":"worker","variant":"high"}`。

## 前提：harness 环境里真的注册了

模型 id 只是字符串，不会自动带来凭据。用之前确认：

1. 运行 Kernel 的那个环境（HOME / XDG_CONFIG_HOME / PATH）里的 OpenCode 配置包含该 provider；
2. 该 provider 的凭据可用；
3. AO 启动的 OpenCode harness 没有和桌面版 OpenCode 抢同一个 service 端口。

如果 harness 不认识，会得到明确错误，例如：

```
Invalid params: model not found: opencode-go/does-not-exist
```

这说明 Kernel 侧传递正常，问题在 harness 配置。

## 隔离环境（本项目测试用）

测试时用 `HOME=~/dev/p3/home`、`XDG_CONFIG_HOME=~/dev/p3/config` 隔离 AO/OpenCode 状态；
该目录下 OpenCode 配置为空，因此只有内置的 DeepSeek 可用。想用真实注册的模型，
需要去掉这些覆盖（或把 provider 配置复制进沙箱）。

## 执行方式：AO 或 OpenCode CLI

`goal` 命令现在支持两种 executor：

| executor | 说明 |
|---|---|
| `opencode`（默认） | Kernel 直接调用 `opencode run --standalone --auto --format json -m <model>`；每个任务一个独立工作目录 |
| `ao` | 通过 AO daemon 起会话（旧路径，保留） |

`plan / run / continue / report / explain` 都接受 `--executor`。CLI 方式的要点：

- `--standalone` 使用私有 server，不碰桌面版 OpenCode 的后台服务；
- 任务目录是 `<state>/work/<task>-<attempt>-<ts>`，启动时从 `<state>/repo` 复制（跳过 `.git`）；
- 产物从任务目录读取，改动的文件按与初始快照的哈希差异计算；
- 模型输出用 JSONL 事件流解析，token 用量从 `step_finish` 取。

限制：CLI 方式没有 AO 的 chat-host 清理和会话复用；Kernel 崩溃时，正在跑的
`opencode` 进程可能残留（进程组独立）。需要更严格的进程治理时用 `ao`。

## 按角色配置（推荐）

模型可以按角色分别设置，保存在状态目录的 `models.json`：

```sh
kernel goal models --dir D --set worker=opencode-go/deepseek-v4.1-flash
kernel goal models --dir D --set reviewer=opencode-go/kimi-k3.1-pro --set acceptance=opencode-go/kimi-k3.1-pro
kernel goal models --dir D --refresh      # 从 opencode 拉取模型列表并缓存
kernel goal models --dir D                # 查看当前设置
```

解析顺序：**命令行参数 > `models.json` > 内置默认（`opencode-go/deepseek-v4.1-flash`）**。

内置默认由 `cmd/kernel/goal.go` 的 `defaultModel` 单点定义；改默认只改这一处。
reviewer / acceptance 没单独设置时跟随 worker；approval 未设置时回退到 chat 的 base model
（不带思考强度）。

界面上有「模型」卡片，五个角色各一个下拉框（规划/执行/审查/验收/汇报），
列表来自缓存的 `opencode --standalone models`；改选后立即保存，右上角可刷新列表。
