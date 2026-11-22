# 历史施工单重放整理

`meridian-workorder` 把一张被打散的历史施工单整理成两组交付物：

1. **可交付的重放资料**：`replay.json`（可执行步骤）、`plan.json`（结构化判定）、`plan.md`（人读版）。
2. **无法无歧义恢复的清单**：`plan.json` 的 `ambiguous_groups` / `unrecoverable_groups` /
   `blocked_groups` 与 `replay.json` 的 `skipped_groups` / `unresolved_lines`。

工具只读施工单，不会连接或修改任何现有数据库。`-verify` 会在临时目录里启动真实
`meridian` 进程，用真实 `POST /edits` 逐组回放，并把结果与静态推演对比。

## 施工单格式

```jsonc
{
  "name": "sample-historical",
  "commit_order": ["g1-swap", "g2-choice"],   // 事务组的原始提交顺序（必填）
  "expect": {"g1-swap": "unique"},            // 可选：自校验，判定不符即报错
  "layers":   [ /* 基线图层定义，创建于重放之前 */ ],
  "features": [ /* 基线地物内容 */ ],
  "rows": [
    {"line": 3, "group": "g1-swap", "layer": "swap-yard", "id": "alpha",
     "mode": "replace", "value": {"id": "alpha", "class": "fixture", "shape": {"extent": {"min_x": 0, "min_y": 0, "max_x": 4, "max_y": 4}}}}
  ]
}
```

- `line` 是**原始源行号**，唯一且必填。所有输出都以它标注来源，重排只改发送顺序，不改行号。
- `group` 是事务组名；同组行在交付时必须**一次** `POST /edits` 发出。
- `rows` 的数组顺序就是"接收顺序"，可以被打散。
- `commit_order` 是唯一可信的组间顺序；缺失的组会被拒收。

## 判定分类

| 分类 | 含义 | 是否产出重放 |
| --- | --- | --- |
| `unique` | 所有合法排序收敛到同一最终形状 | 是，取字典序最小的源行顺序 |
| `ambiguous` | 存在两种及以上合法排序，且最终形状不同 | 否，需人工指定目标形状 |
| `unrecoverable` | 该组任何排序都对基线不成立 | 否 |
| `blocked` | 依赖的前序组未定，该组无法判定 | 否 |
| `unresolved` | 组内行数超过穷举上限（8） | 否 |

`ambiguous` / `unrecoverable` 会把其涉及图层标记为"未定"，后续触及同一图层的组一律
`blocked`——工具不会替人挑一条分支继续往下推。

## 整组原子性

对每个 `unique` 组额外计算两个属性：

- `atomic_required`：**所有**合法排序中都存在被单独拒绝的提交边界，即该组在任何情况下
  都只能整组发送。
- `replay_needs_whole_group`：仅"本工具选定的发送顺序"存在被拒边界；换成别的合法排序
  就能拆批，因此分组本身并不证明原子性。

`split_failures` 列出具体边界（`after_line`）与被拒原因，`-verify` 会在真实服务上逐个复现。

## 运行

```sh
go build -o meridian-workorder ./cmd/meridian-workorder

# 仅静态推演，输出 plan.json / plan.md / replay.json
./meridian-workorder -order examples/workorder/sample-historical/ledger.json

# 静态推演 + 临时目录真实回放 + 逐字执行 replay.json
./meridian-workorder -order examples/workorder/sample-historical/ledger.json -verify
```

`-out` 可指定输出目录（默认写在施工单旁边）；`-binary` 可复用已构建的 `meridian`。
`-verify` 额外产出 `verify.json`，其中每组记录计划摘要、真实摘要、以及每个拆批边界在
真实服务上的拒绝码。

## 样例

| 目录 | 覆盖点 |
| --- | --- |
| `minimal-pair` | 最小对照：交换位置只有整组发才成立 + 两种合法排序产生不同形状 |
| `fully-recoverable` | 全部组可判定，端到端摘要一致 |
| `sample-historical` | 综合：交换、同组先删后插同一编号、跨层原子组、删除后重用编号、多解、受阻、无法恢复 |

`sample-historical` 的组一览：

- `g1-swap`：三个矩形轮换位置，任一拆批边界都违反排他，**只有整组发才成立**。
- `g2-recycle`：同组先 `insert` 后 `remove` 同一编号；接收顺序非法，合法顺序为
  `remove` 再 `insert`，最终保留新形状。
- `g3-multilayer`：跨 `ledger` 与 `pin-field` 两层，共享一个修订。
- `g4-shape-choice`、`g6c-last-writer`：两种合法排序，**最终形状不同**，不可无歧义恢复。
- `g4b-after-choice`：触及 `g4-shape-choice` 遗留的未定图层，判定为 `blocked`。
- `g6b-order-locked`：本工具选定的顺序必须整组发，但另一合法排序可拆批——用于区分
  `atomic_required` 与 `replay_needs_whole_group`。
- `g7-lost`：组内唯一一行与基线冲突，任何排序都不成立，判定为 `unrecoverable`。

## 测试

```sh
go test ./internal/workorder
```

`TestLiveReplayMatchesPlan` 与 `TestArtifactExecutes` 会在 `go` 不在 `PATH` 时跳过；
其余测试（含源行号保持、原子性判定、多解检测、`blocked` 传播）不需要工具链。
