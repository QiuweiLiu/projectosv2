# 能力包（Capability Packs）

Kernel 内核不出现任何领域词。领域差异放在 `internal/policy`，通过 objective 的
`"pack"` 字段选择。

## 现有包

| 名称 | 审查 | 默认尝试次数 | 适用 |
|---|---|---|---|
| `simple` | 不需要 | 1 | 低风险、便宜、一次就能过的产物 |
| `research` | **必须** | 2 | 需要通过独立复核的产物 |

`research` 未提供 `--reviewer` 时，CLI 直接拒绝运行（exit 1），不会静默降级。

## 用法

```json
{
  "name": "two-attempts",
  "pack": "research",
  "tasks": [ ... ]
}
```

```sh
kernel run --objective objectives/two-attempts.json --dir ./state --reviewer
```

## 新增一个包

1. 在 `internal/policy/policy.go` 的 `builtin` 里加一条（名称、是否需要审查、默认尝试次数）。
2. 如需要新的验收方式，在 `internal/objective` 增加 validator 类型（不要引入脚本执行）。
3. 补一个测试，证明包与包之间**行为真的不同**，而不只是配置字段不同。

Kernel 本身不需要改动；这是"换领域仍有用"这一设计原则的最小验证。
