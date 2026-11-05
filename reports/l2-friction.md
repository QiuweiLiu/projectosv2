# 三个真实目标的摩擦记录（2026-11-04）

方式：全部走 `serve --control` + HTTP API（与 cockpit 按钮同一条路径），真实模型规划、真实 AO Worker、
reviewer 开启。每次一个新状态目录。

## 1. 结果

| 目标 | 首次结果 | 处理 | 复跑 |
|---|---|---|---|
| G1 待办应用（app.js / index.html / test.js） | **completed** 3/3，reviewer 0 阻断 | — | — |
| G2 CSV 汇总（sales.csv → summarize.js → summary.md） | **failed**：T3 被 reviewer 判“多出 sales.csv” | 修 reviewer 依赖声明 | **completed**，`summary.md` = 总额 60 |
| G3 统计（data.csv → stats.py → stats.md） | **failed**：`python3 -m pytest` 环境缺模块 | 改规划器命令偏好 | **completed**，`stats.md` = 平均值 30 |

G2/G3 复跑时各出现 1 次真实阻断（T2 首轮），retry 后通过——reviewer 不是噪声。

## 2. 发现的摩擦与修复

### F1. reviewer 把传递依赖当“额外文件”（G2 失败原因）

G2 计划是 T1(sales.csv) → T2(summarize.js) → T3(summary.md)。T3 的直接依赖只有 T2，
但它的 worktree 里有注入的 sales.csv，reviewer 只被告知“已验收依赖：summarize.js”，
于是把 sales.csv 判成未请求的副作用，连续两轮阻断。

修复：

- reviewer 的依赖清单改为**祖先闭包**（与 worker 提示词、验收 sandbox 一致）；
- worker 会把注入的依赖落盘，所以这些路径也从 `Files changed by this run` 中剔除——
  它们是输入，不是这次运行产出；
- 回归测试 `TestReviewerSeesTransitiveDependencyFiles`。

### F2. 计划顺序错误（G1 首次失败的同类问题）

早前一轮把 `index.html` 排在 `app.js` 之前且不声明依赖，reviewer 正确阻断。
规划器提示词补两条：

- 产物中引用的文件（`<script src>` / import / require / readFile）必须由另一个任务产出，并声明为依赖；
- 创建共享文件的任务在前，使用方显式依赖它。

复跑 G1：T1=app.js、T2/T3 均依赖 T1，一次通过。

### F3. 规划器选择环境里不存在的命令（G3 失败原因）

计划给测试任务选了 `python3 -m pytest`，但执行环境的 python 没有 pytest，两次尝试都失败：

```
verification: command "python3 -m pytest" failed: No module named pytest
```

这是环境原因，不是代码原因。规划器提示词改为**优先 `python3 -m unittest`**（标准库），
pytest 仅在仓库本身使用 pytest 时选择。复跑 G3 使用 unittest。

### F4. 重规划历史在界面上不可见

replan 后当前计划只剩修复任务，界面看起来“一直就是这个形状”。现在 detail 返回 `plans[]`，
界面显示 `v1 superseded → v2 confirmed`，并把跨计划的 superseded 任务计入“已取代”。

## 3. 复跑后的产物（人工核对）

```
R2 out/sales.csv    amount 10 20 30
R2 out/summary.md   总额: 60
R3 out/data.csv     value 10 20 30 40 50
R3 out/stats.md     平均值: 30
```

## 4. 仍未解决

- **reviewer 首次阻断率偏高**：三个目标里两个在第二轮才通过。阻断都是真问题，
  但代价是每个目标多一轮 worker + review；值得做的是给 reviewer 更明确的“什么算 P1”。
- **命令型验收可能写别的任务的产物**：R2 的 T3 用 `node summarize.js` 验收，而该命令会写 summary.md。
  sandbox 是临时的，不会污染工作区，但语义上冗余。
- 交付物正确性仍靠 validator + reviewer；没有“对 out/ 再跑一次”的独立检查，本次是我人工核对的。

## 5. 结论

三个目标全部可交付，其中两个暴露的是**声明信息不全**（依赖闭包）和**计划选择不当**（命令、顺序），
不是 Kernel 语义错误。这两类问题都通过“让声明更完整 + 提示词约束”修掉，而不是放宽验收。
