# 重放整理：fully-recoverable

所有组都可判定的小账本，用于检查终态摘要路径。

- 基线形状摘要：`sha256:ffa03815fd6e7f62d9550b51e090292b0a5adef40fd8e4952cd67afca8cee8c8`
- 全量重放后摘要：`sha256:3d524d8a641cf57184b22c09a0f4b053f085f732ef0cfef13c9e2ff0483d9fe5`
- 提交顺序：`rotate`, `refill`
- 事务组：2（唯一 2，多解 0，无法恢复 0，受阻 0）
- 任何排序都只能整组发送：`rotate`

## 重放批次

每组按提交顺序整组发送一次 `POST /edits`；`edits` 的下标与 `source_lines` 一一对应。

### 1. `rotate` — 唯一可重放

- 涉及图层：`yard`
- 源行（接收顺序）：2, 1
- 重放源行顺序：`1, 2`
- 最终形状摘要：`sha256:4ad3a7ff14cda1c56a9b4fc11bf6a6248820108800b01c54845038b3a051d3de`

```json
{"edits":[{"layer":"yard","id":"alpha","mode":"replace","value":{"id":"alpha","class":"fixture","shape":{"extent":{"min_x":6,"min_y":0,"max_x":10,"max_y":4}}}},{"layer":"yard","id":"beta","mode":"replace","value":{"id":"beta","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":4}}}}]}
```

按本批次顺序必须整组一次发送；拆批会在下列边界被拒绝：

- 源行 1 之后切分：前缀批次被拒绝 `conflict` — exclusive layer yard: alpha intersects beta

> 共 2 种排序合法，但全部收敛到同一最终形状，因此重放无歧义
> 本组每一种合法排序都存在被单独拒绝的提交边界，因此无论顺序如何都只能整组一次发送

### 2. `refill` — 唯一可重放

- 涉及图层：`marks`
- 源行（接收顺序）：12, 11
- 重放源行顺序：`12, 11`
- 最终形状摘要：`sha256:905701c5f543cbaff3c092ca6a74762b3f41c5ef91c584cfb17fb5cf782437fd`

```json
{"edits":[{"layer":"marks","id":"pin","mode":"remove"},{"layer":"marks","id":"pin","mode":"insert","value":{"id":"pin","class":"fixture","shape":{"mark":{"x":20,"y":5}}}}]}
```

> 本批次顺序的每个切分点都单独合法，该组原本也可以拆成多次提交

## 无法无歧义恢复的清单

无。
