# 跨 Objective 共享产物（P5）

共享产物是**已验收交付的派生副本**，不是第二套权威存储。权威仍是各 Objective 的
SQLite、`runs`/`evidence` 和完成后的 `delivery.json`。

## 发布条件

只有同时满足以下条件才进入 `project-state/<projectID>/artifacts/`：

- Objective 状态为 `completed`；
- `delivery.json` 可读，`status` 为 `completed`，且所有 command check 为 `OK`；
- 最新 objective acceptance review（若存在）不是 blocking，且该 review 显式绑定到
  当前 plan version（旧版 review 不能否决新计划）；
- 每个 Task 状态为 `accepted`，且能从 `runs.result` 取到内容；
- 内容 SHA256 与 `evidence.digest` 一致；
- 路径不是绝对路径、不含 `..`，单文件 ≤5MB，合计 ≤20MB。

`out/` 不再被扫描：即使里面多出一个文件，只要没有对应 accepted Task，就不会发布。
被阻断、失败、取消、未完成的目标不会发布；它们的旧条目会在下一次发布时消失。
单个 Objective 的路径越界、超大产物、evidence 不匹配会记录到 `skipped` 并跳过，不会
阻止同 Project 其他健康 Objective 刷新。

## Manifest

`project-state/<projectID>/artifacts/MANIFEST.json`：

```json
{
  "version": 1,
  "projectId": "proj-...",
  "generatedAt": "2026-...",
  "entries": [{
    "projectId": "proj-...",
    "objectiveId": "obj-...",
    "taskId": "T1",
    "artifactPath": "results.json",
    "contentSha256": "...",
    "artifactVersion": "sha256:...",
    "planVersion": 1,
    "deliveryHash": "...",
    "evidenceId": "...",
    "verificationStatus": "verified",
    "publishedAt": "2026-..."
  }],
  "skipped": [{"objectiveId": "obj-...", "reason": "completed objective has no readable delivery.json"}]
}
```

Manifest 完全从 Objective DB、`delivery.json` 和 evidence 重建；删除后下一次发布即可恢复。
`skipped` 只记录“看起来应该发布但事实不完整”的 completed 目标，便于排查。

## 原子刷新与并发

发布在 `project-state/<projectID>/.publish.lock` 上持有独占 `flock`；seed 在读 Manifest
和文件之前先持有共享 `flock`，因此不会把旧 Manifest 和新树文件组合。文件先写入隔离 staging 目录，Manifest 写在树内，随后
整目录替换；失败时保留上一版完整树。旧树只在成功替换后删除。发布进程崩溃后，
`recoverArtifactTree` 会恢复 `.artifacts-old-*` 的唯一完整备份，并清理 staging/old。

## Worker 可见副本

新 Objective 启动时，只按 Manifest 复制到 `repo/project-artifacts/`：

- `repo/project-artifacts/<objectiveID>/<path>`：只读语义的副本；
- `repo/project-artifacts/MANIFEST.json`：来源、版本、哈希；
- 复制前重新校验内容 SHA256；不匹配就明确失败，不把篡改文件交给 worker；
- Manifest 不存在时清空该命名空间，避免遗留副本被误当成可信证据；
- Manifest 未列出的文件不会被复制。

Worker 可以修改自己工作区里的副本，但这不会改变共享区或原始 `runs.result`；下一次
发布会用权威内容覆盖共享区。共享区本身不是权威，不能反向修改 Objective 事实。

## 触发

- `finishGoal` 在 Objective 转为 completed 后刷新；失败只记录
  `artifact_publish_error`，不会回滚已经完成的 Objective。
- `ProjectSnapshot` / `kernel project snapshot` 也会重建并刷新。
- API：`GET /api/objectives/{id}` 的 `sharedArtifacts` 返回该 Objective 的 Manifest 条目。

## 测试

`cmd/kernel/project_artifacts_test.go` 覆盖：未完成/失败交付不发布、`out` 内容不被信任、
哈希篡改检测、同名文件隔离、旧条目清理、路径遍历、并发发布、失败保留旧树、重启重建。
