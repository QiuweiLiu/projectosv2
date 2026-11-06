# ProjectOS v2

单机、目标驱动的多 Agent 工作系统：自然语言目标 → 计划 → 人工确认/审批 → 并行执行 →
三层验收 → 交付 → 继续改进。Kernel 是唯一写者，模型只提案，人在关键处确认。

本仓库是 monorepo：

```
cmd/            CLI（kernel goal / serve）
internal/       调度、存储、验收、执行器、汇报、diff
desktop/        macOS 原生外壳（Objective-C + WKWebView）
docs/           Kernel 当前实现文档
reports/        验收、审查与真实运行证据
.project/       Kernel 当前状态与控制面
design/         产品设计、背景、路线与控制面
```

## 当前状态

P2 可用性已完成并验证：

- 目标 → 计划 → 确认 → 并行执行 → 三层验收 → 交付 → 恢复；
- 多目标 Workspace：每个目标独立数据库/锁/work/out，可并行；
- 执行前审批：高风险任务与 command 校验需要显式批准；
- 文本产物 diff、会话续跑、work 保留策略；
- token 优先使用 provider 实测值；
- **持久化聊天工作台**：OpenCode v2 Session、SSE 流、工具级 ask 审批、历史恢复；
- Project / Objective 目录与归属映射；
- macOS 原生外壳（非 Electron），默认打开 `/chat`；
- **P5**：可信共享产物 Manifest、结构化 Progress、有界自主恢复、按需 Evidence Context、
  Cockpit 恢复/证据展示、SSE 游标恢复与故障注入测试。

266 项 Go 测试 + `-race` + `vet` 全绿。边界与未实现项见 `docs/api.md` 第 11 节和
`.project/HANDOFF.md`。

## 快速开始

```sh
go test ./...
go build -o bin/kernel ./cmd/kernel

# 单个目标
./bin/kernel goal create --dir /tmp/demo --goal "创建 hello.txt，内容为 hello world"
./bin/kernel goal plan   --dir /tmp/demo --planner model
./bin/kernel goal confirm --dir /tmp/demo
./bin/kernel goal run    --dir /tmp/demo --reviewer --approve-risk

# 多目标驾驶舱 + 聊天
./bin/kernel serve --dir ~/ProjectOS --control --daemon
# 浏览器打开输出地址；聊天在 /chat

# 项目级快照
./bin/kernel project list --dir ~/ProjectOS
./bin/kernel project snapshot --dir ~/ProjectOS --project proj-...
```

`goal run` 需要能访问已注册的 OpenCode provider/model；模型配置见 `docs/models.md`。

## 桌面外壳

```sh
sh desktop/build.sh
open desktop/build/ProjectOS.app
```

默认 Workspace 是 `~/Library/Application Support/ProjectOS/state`，可用 `PROJECTOS_DIR` 覆盖。
只支持 macOS；未签名/未公证。

## 文档入口

- `docs/api.md`：本地 API、Workspace、验收与边界
- `docs/chat.md`：聊天、Session、SSE 与工具权限
- `docs/workspace.md`：多目标设计
- `docs/approval.md`：执行前审批的准确范围
- `docs/desktop.md`：原生外壳构建与行为
- `docs/executor.md`：OpenCode CLI / AO 执行器
- `design/docs/kernel-implementation.md`：从设计到实现的记录

## 安全边界

- worker 以当前用户身份运行，`command` 校验有白名单但**不是沙箱**；
- 审批是执行前门禁，不是逐工具调用拦截；
- 仓库不包含运行状态、模型凭据或控制 token；`work/`、`out/`、`*.db`、`desktop/build/` 已忽略。
