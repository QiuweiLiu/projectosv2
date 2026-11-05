# 全量审查（2026-11-04）

## 方式

两个独立 reviewer，范围不重叠，各自加载 `code-reviewer`：

- **A：核心正确性** — engine / store / goal / compile / replan / delivery / verify / `goal.go`
- **B：对外接口** — serve / control / api / status / ui / ui.html / aoenv

只读审查，不给修法。共 **34 条**：P0×3、P1×22、P2×9。

## 结果

### 已修（25 项）

| 编号 | 问题 | 修法 |
|---|---|---|
| A P0-01 | 旧 Run 能结算更新的 attempt，覆盖已接受状态 | 删除未使用的 `Reject`；`SettleRun` 校验 run 与 task 的 attempt 一致，且更新条件带 `attempts` |
| A P0-03 | `out/` 里若有符号链接，组装会写到树外 | 组装拒绝 `out` 为符号链接；写入前逐级 `Lstat`；拷贝遇到符号链接直接报错 |
| A P1-04 | 非法终态组合可被结算（失败却 accepted） | 只接受四种终态；`accepted` 必须对应 `succeeded` |
| A P1-05 | 崩溃后遗留的 AO chat host 无人清理 | `aoenv.Setup` 启动时先 `ReapAll`（单 scheduler 前提下，残留必属死运行） |
| A P1-06 | 组装/验证出错仍判 completed | 组装或验证出错即判 failed，不写 `.project` 快照 |
| A P1-07 | 产物路径 `"."` 被接受 | 明确拒绝 `"."`；空路径原本已拒绝 |
| A P1-08 | 两个任务可产出同一路径，交付时静默覆盖 | 计划校验要求产物全局唯一 |
| A P1-09 | 复用更早计划里退役的 ID | 重规划同时对照数据库中的历史任务 ID |
| A P1-10 | 新增任务上限按总数算 | 只统计旧计划里不存在的新 ID（≤5） |
| A P1-11 | `--max-replans` 可以突破“只重规划一次” | 启动时钳制为 1 |
| A P1-12 | 已接受任务只比较产物与 validator | 比较产物/依赖/validator/尝试上限；写入前把已接受任务按旧定义原样带入，计划记录不再漂移 |
| A P1-15 | 允许列表按前缀匹配，`npm run testevil`、`node -e` 可通过 | 改为按 token 解析：npm 只收两种精确形式；node 只收脚本路径；python/go 拒绝一切以 `-` 开头的参数 |
| A P1-16 | quarantine 持久化失败被忽略 | 持久化失败记事件；清除时先落盘再清内存 |
| A P1-13 | 重规划非原子 | 新增 `store.ApplyReplan`：建计划、退役旧任务、确认、切换 objective 在同一事务；失败则整体回滚 |
| A P1-17 | reviewer 返回未知 severity 视为不阻断 | 未知级别按 P1 处理，附回归测试 |

接口侧：

| 编号 | 问题 | 修法 |
|---|---|---|
| B P1-01 | token 打进 `serve.log`，token/log 权限 0644 | 日志 0600；daemon 模式不打印 token，由父进程从文件读给用户；`writeToken` 强制 chmod 0600 |
| B P1-02 | 绑定 `:8787` 时只读 API 无认证却暴露全网 | 默认只允许回环；非回环必须显式 `--allow-remote` |
| B P1-03 | 就绪探测信任任意 HTTP 服务、无启动锁 | 服务启动时抢 `serve.lock`；`/api/status` 返回 instance，启动/停止都校验 instance 是否匹配 |
| B P1-04 | 启动失败留下无 `serve.json` 的孤儿进程 | 就绪失败/进程早退时杀掉子进程，并打印子进程日志尾部 |
| B P1-08 | 计划/执行可被重复点击触发 | 每个目标 plan/run 单飞，重复请求返回 409；取消不受限 |
| B P2-09 | Origin 用前缀匹配，`127.0.0.1.evil.com` 通过 | 解析 URL 后精确匹配 host |
| B P2-10 | 慢轮询覆盖新选择 | 详情/任务加载加代次守卫 |
| B P2-11 | 时间线永远停在前 100 条 | 新增 `?tail=1` 取最新；之后用 `after` 游标增量合并 |
| B P2-12 | 非法 JSON 被静默忽略 | 非空非法 body 返回 400 |
| B P2-13 | 只有 ReadHeaderTimeout | 增加 Read/Write/Idle 超时 |

### 未修（明确的取舍）

| 编号 | 问题 | 理由 |
|---|---|---|
| A P2-18 | 结算/事件错误被丢弃 | 需要故障注入框架；当前只在关键路径返回错误 |
| A P2-19 | 指纹不含副作用文件内容 | 副作用文件不进入交付；改动会影响证据版本语义，需单独设计 |
| A P2-20 | 部分 `List` 错误被忽略 | 影响状态展示，不影响执行正确性 |
| A P1-14 | `scope/inputs/allowed/forbidden/acceptance/priority` 只写进提示词 | 设计如此：没有工具级强制就无法执行；已在 `docs/plan-semantics.md` 明确声明 |
| A P0-02 | 命令验收不是安全沙箱 | 允许列表只防注入；真隔离需要 OS sandbox，超出当前范围（见“接受的风险”） |
| B P1-05 | `--stop` 可能因 PID 复用杀掉无关进程 | 需要进程身份校验（启动时间/可执行路径）；当前用 `serve.json` + 存活探测 |
| B P1-06/07 | `aoenv` 可能采纳外来 daemon / 起第二个 | 需要 AO 侧健康检查提供身份字段；已在 Open 中记录 |
| B P2-14 | 日志无上限 | 单机自用，暂不引入轮转 |

## 接受的风险（必须写清楚）

1. **命令验收不是沙箱**：`node test.js` 以当前用户身份运行，能读全盘、能联网。
   允许列表只防“计划注入任意 shell 命令”，不防“合法命令做任意事”。
   真正的隔离需要 OS 级 sandbox（seatbelt/bubblewrap），不在当前范围。
2. **单机单用户**：只读 API 无认证，默认只绑回环；`--allow-remote` 等于把项目内容暴露到网络。
3. **计划字段有装饰性**：见 `docs/plan-semantics.md`，`acceptance/scope/inputs/allowed/forbidden/priority`
   不参与机器判定。

## 结论

审查没有推翻调度与结算模型，但抓到了三类真问题：

- **状态机缺口**（旧结果覆盖新 attempt、非法终态组合）；
- **声明不一致**（产物重复、路径 `.`、历史 ID、已接受任务的字段漂移）；
- **边界失守**（token 入日志、默认全网绑定、前缀匹配、重复执行）。

已修的都有回归测试或真实验证；未修的写进了 HANDOFF 的 Open，不假装不存在。
