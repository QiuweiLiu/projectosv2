# macOS 原生外壳

## 它是什么

一个 Objective-C + WKWebView 的 `.app`：启动本地 `kernel serve`，把驾驶舱装进原生窗口。
不是 Electron，也没有新的前端构建链。

```
desktop/
  main.m       # 窗口、启动/停止服务、token 注入
  build.sh     # 构建 .app，把 kernel 二进制放进 Resources
  build/       # 构建产物（已在 .gitignore）
```

## 构建与运行

```sh
cd ~/dev/projectosv2
sh desktop/build.sh
open desktop/build/ProjectOS.app
```

外壳会：

1. 恢复上次打开的 Workspace 标签（至少一个）；没有记录时使用
   `~/Library/Application Support/ProjectOS/state`；
2. 为每个标签启动独立的 `kernel serve --control --daemon`，端口从 `8799` 起自动选择空闲端口；
3. 每个标签用各自的 `control-token` 以 `#token=…` 打开 **`/chat` 聊天工作台**
   （fragment 不发给服务器，页面存入 localStorage 后清除地址栏）；
4. 标签同时保活：各自的 SSE、Objective 与 Worker 互不影响；
5. 退出应用时只停掉自己启动的服务；已托管任务在独立会话里继续执行。

## 多工作区标签

- 菜单 **文件 → 添加工作区标签…**（`Cmd+O`）添加一个目录；最多 8 个标签。
- 点击标签切换；**文件 → 关闭当前标签**（`Cmd+W`）关闭当前标签并只停止该标签对应的服务。
- **标签 → 下一个 / 上一个**（`Cmd+Shift+]` / `Cmd+Shift+[`）切换；
  **标签 → 重新加载当前标签**（`Cmd+R`）重新加载当前页面。
- 标签列表与当前标签保存到 macOS 用户偏好（`ProjectOSWorkspacePaths` /
  `ProjectOSActiveWorkspacePath`），下次启动恢复。
- 重复选择同一目录不会新建标签，而是切换到已有标签。
- 窗口标题显示当前标签的目录名，标题栏代理图标指向完整路径。
- 标签异步准备：窗口先打开，每个标签独立检查/启动，一个慢工作区不会阻塞其他标签。
- 工作区在 iCloud 且文件未下载时，会先请求 iCloud 下载，并在标签内显示
  “正在下载 iCloud 文件…”；下载完成后自动启动服务。失败时显示原因，并提供
  “重试”和“在 Finder 中显示”。下载超过 15 分钟按失败处理。

`PROJECTOS_DIR` 仍然拥有最高优先级：设置后只打开该目录一个标签，且不写入偏好，
添加/关闭标签被禁用，适合自动化与 `--check`。

## 环境变量

| 变量 | 作用 |
|---|---|
| `PROJECTOS_DIR` | 本次启动强制指定 Workspace 根目录（优先于应用内保存的选择） |
| `PROJECTOS_KERNEL` | 指定 kernel 二进制（默认用 `Contents/Resources/kernel`） |
| `OPENCODE_BIN` | 显式指定 OpenCode CLI；未设置时外壳自动查找 OpenCode Desktop 自带的最新 CLI（Finder 启动时 PATH 很短） |

## 冒烟测试

```sh
desktop/build/ProjectOS.app/Contents/MacOS/ProjectOS --check
# ok http://127.0.0.1:8799

desktop/build/ProjectOS.app/Contents/MacOS/ProjectOS --check-multi /path/a /path/b
# ok http://127.0.0.1:8799 http://127.0.0.1:8800 distinct=1
```

`--check` 启动服务、请求 `/api/status`、停止服务，不打开窗口。
`--check-multi` 验证两个工作区能在不同端口同时存活。

调试用 `--dump-tabs <dir>`：启动标签、打印每个标签的路径/URL/端口、渲染原生标签栏 PNG
到 `<dir>/tabbar.png`，然后退出，不用于日常使用。

## 边界

- 只支持 macOS；Windows / Linux 外壳没有做。
- 每个标签从 8799 起自动挑选空闲端口；若 40 个端口都被占用，该标签打开失败并弹窗。
- 标签数量上限 8，避免同时加载过多 WKWebView。
- 同一目录只允许一个标签；标签是单窗口内的切换，不是多窗口。
- 没有托盘、通知或自动更新。
- `alert/confirm/prompt` 由 `WKUIDelegate` 转成原生 NSAlert；未设置时 WKWebView 会
  直接忽略对话框（confirm 返回 false）。
- 聊天工作台的“任务”按钮回到 Cockpit；Kernel 的轮询页面仍可用。
- 不签名/不公证；`build.sh` 只做 ad-hoc 签名，适合本机使用。

## 视觉方向

界面采用蓝白 Token 的安静工作台方向：左侧导航与对话列表、中间聊天区、右侧项目检查器组成三栏布局；
蓝色用于主要动作与选中态，白色面板和浅灰背景承载内容。聊天工作台与 Cockpit 共用这套蓝白视觉语言。
