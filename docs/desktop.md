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
cd ~/dev/projectos-kernel
sh desktop/build.sh
open desktop/build/ProjectOS.app
```

外壳会：

1. 使用 `~/Library/Application Support/ProjectOS/state` 作为 Workspace；
2. 如果该目录没有活着的服务，启动 `kernel serve --control --daemon --addr 127.0.0.1:8799`；
3. 读取 `control-token`，以 `#token=…` 打开驾驶舱（fragment 不发给服务器，页面存入
   localStorage 后清除地址栏）；
4. 关闭窗口时只停掉自己启动的 HTTP 客户端；已托管任务在独立会话里继续执行。

## 环境变量

| 变量 | 作用 |
|---|---|
| `PROJECTOS_DIR` | 指定 Workspace 根目录 |
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
- 窗口只是 WebView，没有原生菜单、托盘、通知或自动更新。
- 不签名/不公证；`build.sh` 只做 ad-hoc 签名，适合本机使用。
