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

1. 使用上次在应用内选择的 Workspace；没有选择时使用
   `~/Library/Application Support/ProjectOS/state`；
2. 如果该目录没有活着的服务，启动 `kernel serve --control --daemon --addr 127.0.0.1:8799`；
3. 读取 `control-token`，以 `#token=…` 打开 **`/chat` 聊天工作台**
   （fragment 不发给服务器，页面存入 localStorage 后清除地址栏）；
4. 关闭窗口时只停掉自己启动的 HTTP 客户端；已托管任务在独立会话里继续执行。

## 选择工作区

菜单 **文件 → 选择工作区…**（`Cmd+O`）可以创建或选择一个文件夹作为后续工作区。
选择会保存到 macOS 用户偏好；应用会停止自己启动的旧服务，在新目录启动服务并重新加载
聊天工作台。已托管的 Worker 不会被取消。

命令行的 `PROJECTOS_DIR` 仍然拥有最高优先级，适合自动化和 `--check`。

## 环境变量

| 变量 | 作用 |
|---|---|
| `PROJECTOS_DIR` | 本次启动强制指定 Workspace 根目录（优先于应用内保存的选择） |
| `PROJECTOS_KERNEL` | 指定 kernel 二进制（默认用 `Contents/Resources/kernel`） |

## 冒烟测试

```sh
desktop/build/ProjectOS.app/Contents/MacOS/ProjectOS --check
# ok http://127.0.0.1:8799
```

`--check` 启动服务、请求 `/api/status`、停止服务，不打开窗口。

## 边界

- 只支持 macOS；Windows / Linux 外壳没有做。
- 端口固定 8799；如果被别的程序占用，启动会失败并弹窗。
- 窗口是 WebView；原生菜单目前只有“选择工作区…”和退出，没有托盘、通知或自动更新。
- 聊天工作台的“任务”按钮回到 Cockpit；Kernel 的轮询页面仍可用。
- 不签名/不公证；`build.sh` 只做 ad-hoc 签名，适合本机使用。

## 视觉方向

界面采用 "ink rail + warm paper + oxidized copper" 的安静工作台方向：深色侧栏承载导航，
暖纸色主区承载内容，单一铜色用于主要动作、选中态和实时状态。视觉方向参考并改编自
[unslop](https://github.com/heydamir/unslop) by Damir Sokolovsky（CC BY 4.0）。
