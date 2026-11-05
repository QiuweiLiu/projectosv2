# 只读看板（`kernel serve`）

用途：不查数据库就能看清"谁被拦下、为什么、证据是什么版本"。

启动：

```sh
kernel serve --dir <state-dir> --addr 127.0.0.1:8787
```

## 页面结构

```
ProjectOS Kernel            [quarantined]
objective · pack · hash · 估算 token · 时间

[all][accepted][blocked][pending]  ☑ 自动刷新 3s

Task  Status  Att  Ev  Rev  Block  Error      │ 任务详情：task/artifact/deps/attempts/error
A     accepted 2    2   2    1                │ 审查：targeted clean / full blocking + findings
B     accepted 1    1   1    0                │ 证据：accepted/rejected + 版本 + digest + 原因
C     accepted 1    1   1    0                │ 事件时间线：最近 100 条
```

## 接口

| 路径 | 内容 |
|---|---|
| `GET /` | 单页应用（原生 JS，无外部依赖） |
| `GET /api/status` | 任务列表 + objective meta + quarantine |
| `GET /api/tasks/{id}` | 单个任务的证据、审查（含 findings）、错误 |
| `GET /api/events?after=&limit=` | 事件时间线，`limit` 上限 500 |

## 设计取舍

- **只读**：没有写接口，所有操作仍走 CLI。避免本地 HTTP 承担鉴权和误触风险。
- **证据版本用内容指纹**：`sha256(content + changed files)`。AO 的 `workspaceVersion` 是比对基线，
  两次 attempt 可能相同，用它会让"被拒的那次"和"通过的那次"长得一模一样。
- **事件按 seq 分页**：`after` 便于以后做增量刷新，而不是每次全量拉。
- **无鉴权**：默认只监听 `127.0.0.1`；`--addr` 改成 `0.0.0.0` 前需要自行加防护。

## 不做

- 重跑任务、清 quarantine、编辑 objective（需要写接口 + 本地鉴权）
- 认证、多用户、历史对比
- 前端框架与构建链
