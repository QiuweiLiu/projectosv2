# 异常输入矩阵（2026-11-03）

目标：不依赖模型调用，验证边界条件下 Kernel 不静默放行、不留下幽灵状态。
全部条目都有对应测试；未覆盖项单独列出，不冒充已验证。

## 1. 路径形态

| 场景 | 测试 | 结果 |
|---|---|---|
| 目录含空格（`with space/state`） | `TestStateDirPathMatrix` | Open/Claim/Settle/只读重开正常 |
| 目录含中文与空格（`项目 数据/状态`） | 同上 | 正常 |
| 深路径（8 层 × 12 字符） | 同上 | 正常 |
| 只读目录 | `TestReadOnlyDirectoryFailsClearly` | 明确报错，不静默创建 |
| 只读数据库文件 | `TestReadOnlyDatabaseRejectsWrites` | 拒绝写入 |
| 旧库缺 `meta` 表 / 缺用量列 | `TestReadOnlyWithoutMetaTable` 等 | 只读降级为空，不报错 |
| artifact 路径含中文/空格 | `TestArtifactPathAllowsUnicodeAndSpaces` | 合法 |
| artifact 绝对路径 / `..` | `TestValidationRejectsUnsafeOrAmbiguousSpecs` | 拒绝 |

## 2. 进程与 daemon

| 场景 | 测试 | 结果 |
|---|---|---|
| daemon 不在（连接被拒） | `TestStartAgainstClosedDaemonFails` | Start 失败并上报 |
| daemon 在运行中消失 | `TestDaemonDiesMidRun` | 连续 observe 失败后 blocked，不重试 |
| 含空格/中文 data dir 的进程回收 | `TestMatchesChatHostHandlesSpacesInDataDir` / `TestMatchesChatHostNonASCII` | 精确匹配，`run-1` ≠ `run-10` |
| 取消失败 | `TestCancelFailureIsRecorded` / `TestCancelFailureStopsFurtherScheduling` | 记录并 quarantine |
| Abort 卡住 | `TestAbortRespectsDeadline` | 100ms deadline 内返回 |

## 3. 预算与时间

| 场景 | 测试 | 结果 |
|---|---|---|
| 用量读取失败且未知 | `TestUnknownUsageStopsRemainingTasks` | 停止剩余调度 |
| reviewer 用量 | `TestReviewUsageCountsTowardBudget` | 计入 |
| 时钟跳到未来 | `TestClockSkewDoesNotExpireFutureRun` | 不误判超时 |
| 时钟跳到过去 | `TestClockSkewExpiresOldRun` | 正确超时 |
| 时间字段损坏 | `TestActiveRunsRejectsBadTimestamp` | 报错，不按零处理 |

## 未覆盖（明确列出）

- **磁盘写满**：没有安全的模拟方式；只验证了只读目录/文件。
- **真实网络分区**：只测了连接被拒与连接中断，未测半开连接、DNS 故障。
- **Windows / Linux**：未在对应平台运行。
- **真实 host 崩溃（整机断电）**：SQLite 的 WAL 语义未做断电注入。
