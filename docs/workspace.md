# 多目标 Workspace

## 为什么不是“把多个目标塞进一个数据库”

Kernel 的调度器、预算、交付组装和 replan 都建立在“一个状态目录一个目标”上：
`kernel.lock` 只有一个写者，`work/` 和 `out/` 各只有一份，任务 ID 在一份数据库内唯一。
把第二个目标塞进同一个库，会同时破坏这些假设（任务 ID 冲突、交付混合、预算串账）。

因此多目标做在 Kernel **之上**：

```
D/                           # Workspace 根，serve --dir 指向这里
  serve.json                 # 后台服务会话
  control-token              # 写接口令牌
  models.json                # 全 Workspace 共用的角色模型
  kernel.db                  # 可选：旧式单目标，继续兼容
  projects/
    obj-1793.../             # 每个目标一个完整状态目录
      kernel.db
      kernel.lock
      work/
      out/
      delivery.md
      .project/
```

每个项目目录仍然只允许一个目标，所以 Kernel 不需要知道 Workspace 的存在。

## 行为

- `kernel serve --dir D` 扫描 `D/kernel.db` 和 `D/projects/*/kernel.db`，聚合目标列表。
- 新建目标自动创建 `D/projects/obj-<nanos>/`，再在该目录里执行 `goal create`。
- 所有按目标的操作（plan / confirm / run / cancel / continue / report / explain / diff）
  都路由到对应项目目录。
- 每个项目有自己的 `kernel.lock`，所以两个目标可以**并行执行**，预算也各自独立。
- 模型设置放在 Workspace 根；`serve` 通过 `KERNEL_MODELS_FILE` / `KERNEL_MODELS_CACHE`
  传给每个 `goal` 子进程，避免每个项目各配一份。
- 目标 ID 使用纳秒时间戳（`obj-<unixNano>`），同一秒创建多个不会撞 ID。

## CLI 用法

`serve` 负责创建和管理项目目录；直接使用 CLI 时，一个项目目录仍然只放一个目标：

```sh
kernel goal create --dir D/projects/obj-123 --goal "写一份周报"
kernel goal plan   --dir D/projects/obj-123 --planner model
kernel goal confirm --dir D/projects/obj-123
kernel goal run    --dir D/projects/obj-123 --reviewer --approve-risk
```

查看某个目标：

```sh
kernel goal status --dir D/projects/obj-123
```

## 迁移

旧的状态目录（根下有 `kernel.db` 和目标）无需迁移：`serve` 会把它当作一个项目继续展示。
第一次在旧目录里新建目标时，新目标进入 `D/projects/`，旧目标保持原位。

## 边界

- 一个项目目录内仍然不能放两个目标；这是 Kernel 的硬约束，不是 UI 限制。
- Workspace 没有跨目标调度器；并行度由用户启动多少个 `goal run` 决定。
- 删除项目目录等于删除该目标的全部状态；没有回收站。
