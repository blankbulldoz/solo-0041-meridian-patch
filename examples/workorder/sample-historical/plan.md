# 重放整理：sample-historical

重建的历史账本。可信的只有：组归属、提交顺序、源行号。组内行顺序被打散，每行都保留原始源行号；每组以一次 POST /edits 交付。

- 基线形状摘要：`sha256:8237adae2aa1dfaa0213e50879ecd340b00f26d0234d5bd8487d1a5d8bf10a38`
- 提交顺序：`g1-swap`, `g2-recycle`, `g3-multilayer`, `g4-shape-choice`, `g4b-after-choice`, `g5-noop`, `g6-splittable`, `g6b-order-locked`, `g6c-last-writer`, `g7-lost`
- 事务组：10（唯一 6，多解 2，无法恢复 1，受阻 1）
- 任何排序都只能整组发送：`g1-swap`

## 重放批次

每组按提交顺序整组发送一次 `POST /edits`；`edits` 的下标与 `source_lines` 一一对应。

### 1. `g1-swap` — 唯一可重放

- 涉及图层：`swap-yard`
- 源行（接收顺序）：4, 3, 5
- 重放源行顺序：`3, 4, 5`
- 最终形状摘要：`sha256:2f6e62b100b1b13893cb0059fb24c4f1912acde1105b58744a04b92901431abc`

```json
{"edits":[{"layer":"swap-yard","id":"alpha","mode":"replace","value":{"id":"alpha","class":"fixture","shape":{"extent":{"min_x":8,"min_y":0,"max_x":12,"max_y":4}}}},{"layer":"swap-yard","id":"beta","mode":"replace","value":{"id":"beta","class":"fixture","shape":{"extent":{"min_x":16,"min_y":0,"max_x":20,"max_y":4}}}},{"layer":"swap-yard","id":"gamma","mode":"replace","value":{"id":"gamma","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":4}}}}]}
```

按本批次顺序必须整组一次发送；拆批会在下列边界被拒绝：

- 源行 3 之后切分：前缀批次被拒绝 `conflict` — exclusive layer swap-yard: alpha intersects beta
- 源行 4 之后切分：前缀批次被拒绝 `conflict` — exclusive layer swap-yard: beta intersects gamma

> 共 6 种排序合法，但全部收敛到同一最终形状，因此重放无歧义
> 本组每一种合法排序都存在被单独拒绝的提交边界，因此无论顺序如何都只能整组一次发送

### 2. `g2-recycle` — 唯一可重放

- 涉及图层：`chain-yard`
- 源行（接收顺序）：11, 12
- 重放源行顺序：`12, 11`
- 最终形状摘要：`sha256:c59941f03e09215ad48ca62a91a91eb2aa30d36d45160f9f9ebac21a502b8124`

```json
{"edits":[{"layer":"chain-yard","id":"link","mode":"remove"},{"layer":"chain-yard","id":"link","mode":"insert","value":{"id":"link","class":"fixture","shape":{"extent":{"min_x":8,"min_y":6,"max_x":12,"max_y":10}}}}]}
```

> 本批次顺序的每个切分点都单独合法，该组原本也可以拆成多次提交

### 3. `g3-multilayer` — 唯一可重放

- 涉及图层：`ledger`, `pin-field`
- 源行（接收顺序）：21, 22, 23
- 重放源行顺序：`21, 22, 23`
- 最终形状摘要：`sha256:e166d9104fc6e6fd17b3387113479e8dc0ef3deb7a003dca3d2dd7362557724e`

```json
{"edits":[{"layer":"ledger","id":"row-a","mode":"remove"},{"layer":"pin-field","id":"pin","mode":"replace","value":{"id":"pin","class":"fixture","shape":{"mark":{"x":31,"y":15}}}},{"layer":"ledger","id":"row-b","mode":"replace","value":{"id":"row-b","class":"fixture","shape":{"extent":{"min_x":4,"min_y":12,"max_x":6,"max_y":16}}}}]}
```

> 共 6 种排序合法，但全部收敛到同一最终形状，因此重放无歧义
> 本批次顺序的每个切分点都单独合法，该组原本也可以拆成多次提交

### 4. `g4-shape-choice` — 多解，不可无歧义恢复

- 涉及图层：`shape-yard`
- 源行（接收顺序）：42, 41

存在 2 种合法最终形状，账本未记录原提交是哪一种：

- `sha256:31aaa5d03a0fe40732ac077a09bc20bf3ae02d44c857f7dfe3db589770576bc5`（1 种排序），例如源行 `42, 41`
- `sha256:4fab683d13e29fa088b75de68e45ac32f7dfe75491a4c8b23eadf91b3a0c1e45`（1 种排序），例如源行 `41, 42`

不可无歧义恢复；需人工指定目标形状后才能生成批次。

> 存在 2 种可达的最终形状；账本没有记录原提交落在哪一种

### 5. `g4b-after-choice` — 受阻，无法判定

- 涉及图层：`shape-yard`
- 源行（接收顺序）：51

前序多解组未定，本组无法判定。

> 前序组把图层 shape-yard 留在未定状态；不先选定该结果就无法判定本组

### 6. `g5-noop` — 唯一可重放

- 涉及图层：`clean-yard`
- 源行（接收顺序）：61
- 重放源行顺序：`61`
- 最终形状摘要：`sha256:e0959f2e1bcba76e1fbd5568e1910737aed2d6dc0ec976104d7184aa5bff0dad`

```json
{"edits":[{"layer":"clean-yard","id":"plank","mode":"replace","value":{"id":"plank","class":"fixture","shape":{"extent":{"min_x":30,"min_y":6,"max_x":34,"max_y":10}}}}]}
```

### 7. `g6-splittable` — 唯一可重放

- 涉及图层：`ledger`
- 源行（接收顺序）：72, 71
- 重放源行顺序：`71, 72`
- 最终形状摘要：`sha256:555b336044e5d7fc91cbdfe0ce9428ea775b171334a308e1b480b1dc9659ee90`

```json
{"edits":[{"layer":"ledger","id":"row-c","mode":"insert","value":{"id":"row-c","class":"fixture","shape":{"extent":{"min_x":8,"min_y":12,"max_x":10,"max_y":16}}}},{"layer":"ledger","id":"row-b","mode":"replace","value":{"id":"row-b","class":"fixture","shape":{"extent":{"min_x":10,"min_y":12,"max_x":12,"max_y":16}}}}]}
```

> 共 2 种排序合法，但全部收敛到同一最终形状，因此重放无歧义
> 本批次顺序的每个切分点都单独合法，该组原本也可以拆成多次提交

### 8. `g6b-order-locked` — 唯一可重放

- 涉及图层：`clean-yard`
- 源行（接收顺序）：76, 75
- 重放源行顺序：`75, 76`
- 最终形状摘要：`sha256:a70623dc27cf1de919c21d5bb050782b1e4f07d5804e31864ce5c57fd4d8c29b`

```json
{"edits":[{"layer":"clean-yard","id":"spare","mode":"insert","value":{"id":"spare","class":"fixture","shape":{"extent":{"min_x":30,"min_y":6,"max_x":34,"max_y":10}}}},{"layer":"clean-yard","id":"plank","mode":"remove"}]}
```

按本批次顺序必须整组一次发送；拆批会在下列边界被拒绝：

- 源行 75 之后切分：前缀批次被拒绝 `conflict` — exclusive layer clean-yard: plank intersects spare

> 共 2 种排序合法，但全部收敛到同一最终形状，因此重放无歧义
> 本批次选定的顺序必须整组发送，但另一种合法排序可以拆批；仅凭分组不能证明该组原子

### 9. `g6c-last-writer` — 多解，不可无歧义恢复

- 涉及图层：`vote-yard`
- 源行（接收顺序）：65, 67, 66

存在 2 种合法最终形状，账本未记录原提交是哪一种：

- `sha256:d158c4fc27c8de4013fe38b595daa27000c636198be145df183a4feeb682a2a7`（3 种排序），例如源行 `65, 67, 66`
- `sha256:a908c4329dc950e3b592e7ecc9ec24b23a861d0e48a5ce4f0b49220434b7916d`（3 种排序），例如源行 `67, 66, 65`

不可无歧义恢复；需人工指定目标形状后才能生成批次。

> 存在 2 种可达的最终形状；账本没有记录原提交落在哪一种

### 10. `g7-lost` — 无法恢复

- 涉及图层：`pin-field`
- 源行（接收顺序）：81

没有任何排序能对基线成立，无法恢复。

> 本组没有任何排序能对基线成立；除非猜测缺失、损坏或错分组的行，否则无法回放
> 按接收顺序提交会被拒绝（conflict）：edit 0: feature "pin" already exists

## 无法无歧义恢复的清单

- `g4-shape-choice`：2 种合法排序产生不同最终形状
- `g4b-after-choice`：依赖未定的前序组
- `g6c-last-writer`：2 种合法排序产生不同最终形状
- `g7-lost`：对基线没有任何合法排序
