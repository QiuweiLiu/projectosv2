# 长期上下文与恢复（P1.3）

目标：关掉终端再回来，用户能看懂"目标是什么、做到哪、为什么卡住"。

## 权威划分

| 位置 | 内容 | 权威性 |
|---|---|---|
| Kernel SQLite | objective / plan / task / run / evidence / review / event | **运行时唯一权威** |
| `<state-dir>/.project/` | PROJECT.md / STATE.md / HANDOFF.md / snapshot.json | **目标级派生快照**，可随时重建 |
| `<workspace>/project-state/<project>/.project/` | 项目级 PROJECT/STATE/HANDOFF/DECISIONS/snapshot | **项目级派生快照**，从多个目标库重建 |
| `delivery.md/json` | 交付报告 | 完成时生成 |

`.project/` 由唯一 writer（`kernel goal export` 或完成时自动）生成，文件头带
`generated-by: projectos-kernel`，不手工编辑。

## 内容与预算

- `PROJECT.md`：目标与验收（≤4KB）
- `STATE.md`：计划版本 + 任务表（状态/尝试/产物/原因）
- `HANDOFF.md`：当前状态、未完成任务数、下一步
- `snapshot.json`：完整快照 + 内容哈希 + 生成时间

超过 4KB 直接拒绝写入，不会生成无限增长的日志。

## 恢复语义

```sh
kernel goal resume --dir D            # 只读检查
kernel goal resume --dir D --write    # 用数据库重建快照
```

- 数据库存在、快照存在且哈希一致 → `snapshot matches database`
- 数据库变了但没重新导出 → `snapshot is stale`
- 快照缺失 → 提示导出；**仍可继续工作**（数据库才是权威）
- 数据库缺失 → 无法恢复，明确报错（Markdown 不是运行时状态）

## 已验证（2026-11-03）

```
goal export → .project 四个文件，hash 035461ae8d81
goal resume → matches database
改库后 resume → "snapshot is stale"
resume --write → regenerated → matches
```

## 项目级上下文

一个 Project 可以包含多个 Objective。项目级 `.project/` 不复制目标运行状态，只汇总：
目标列表、已验证交付、当前阻塞、显式决策。它由 `GET /api/projects/{id}` /
`kernel project snapshot` 从目标数据库重建，不能手工当作权威状态。

## 未实现

- 跨机器同步（单机 SQLite）。
- 把 `.project/` 写进任意真实项目仓库（当前写在 state dir；写用户仓库需要显式授权）。
- 上下文注入 Planner：目前 Planner 只拿到 objective 字段，尚未注入历史失败摘要。
