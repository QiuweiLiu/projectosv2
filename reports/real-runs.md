# 真实运行记录（2026-11-03）

本文件汇总 B 阶段所有**真实** AO + DeepSeek 运行。每条都对应 `~/dev/p3/` 下的运行目录与
`evidence/real/` 内的导出。Fake/单测结果不计入本表。

## 成功的运行

| 运行 | 场景 | 结果 |
|---|---|---|
| run-3 | A∥B→C，无 review | A=42、B=7、C=49，全部 accepted；peak 并发 2 |
| run-5 | `--fail-a` | A 写出 40 → 校验失败 blocked；C 级联 blocked，attempts=0 |
| run-6 | `--max-runs 2` | A/B accepted 并写 evidence；C 被预算阻断 |
| run-7 | `--inject-review p1` | artifact 正确，但注入 P1 → A/B blocked、C 无 Run |
| run-10 | 真实 reviewer + 额外文件 | reviewer 报 P1 `extra_file_notes_md`，A blocked |
| run-16 | reviewer + 两轮 | A：P1 → 修复 → targeted review → accepted |
| kernel-run-7 | CLI + reviewer + 两轮 | A 两轮、B/C 各一轮，全部 accepted，exit 0 |
| kernel-run-8 | 字节级 exact | `c.txt` 写 `49\n` 被拒（`"49\n" != "49"`），C blocked |
| kernel-run-9 | 二次仍写额外文件 | reviewer 正确阻断（复核文件系统确认确有其事，非误报） |
| kernel-run-10 | spawn 超时 | `CHAT_DRIVER_UNAVAILABLE` → Blocked，未重试 |
| kernel-run-11 | 审查后最终形态 | A 两轮（P1 → 带反馈重试 → targeted 通过）、B/C accepted，exit 0 |
| kernel-run-12 | reviewer 权限收紧 | worker=`auto`、reviewer=`default`；reviewer 未写任何文件；三任务全 accepted |
| kernel-run-13 | 能力包 research | 未加 `--reviewer` 被 CLI 拒绝；加上后 A 两轮、B/C 各一轮，全 accepted，tokens≈455 |

## 失败与修正（这些才是主要产出）

| 运行 | 现象 | 根因 | 修正 |
|---|---|---|---|
| run-8 | reviewer 对 A 报 `[]`，但 A 写了 notes.md | reviewer 只拿到 artifact 内容，看不到文件清单 | `Collect` 增加 `Artifact.Files` |
| run-9 | 同上，确认漏报 | — | 同上 |
| run-11 | 复审总对着"旧版本" | AO `workspaceVersion` 是比对基线，不是内容版本 | 改用内容指纹 |
| run-12/13 | 复审没记录却放行 | 记录失败只改局部变量，未接门禁 | 不可记录的 review 一律拒绝 |
| run-14 | 第二次 review 409 | 幂等键 `kernel-review-A-1` 复用 | 键改为 run/artifact 维度 |
| run-15 | 复审把正常的 a.txt 判成"额外文件" | 提示词要求"只报某 code"，模型被迫编理由 | 改为复核既有 finding 是否仍存在 |
| kernel-run-2/3 | `unable to open database file (14)` | 我的批量替换误把 `store.Open` 改成只读打开 | 修正调用点，补只读测试 |
| kernel-run-6 | reviewer 把"文件清单"当成 artifact 内容 | 提示词重排后 `%s` 参数顺序错位 | 修正模板，补提示词布局测试 |

## 结论边界

- 全部运行使用 `deepseek/deepseek-flash`；未统计 token，费用可忽略但**没有精确账单**。
- reviewer 质量不稳定：同配置下 run-8/9 漏报、run-15 误报、run-16 正常。**"接了 reviewer"不等于"审查可靠"**。
- 未覆盖：网络中断、provider 5xx、host 崩溃、多机、Windows/Linux。

## 复现

```sh
cd ~/dev/projectos-kernel
AO_BIN=... DEEPSEEK_API_KEY=... ./bin/kernel run \
  --objective objectives/two-attempts.json --dir /tmp/k7 \
  --reviewer --max-runs 6 --max-reviews 6
```
