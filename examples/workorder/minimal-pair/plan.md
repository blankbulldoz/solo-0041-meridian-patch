# 重放整理：minimal-pair

两组最小对照：一处交换只有整组发才成立；另一组存在两种合法排序且最终形状不同。

- 基线形状摘要：`sha256:6bbdfcc52410633c39c371ac871f9b102aed4e3e626dc241d577e126b516d336`
- 提交顺序：`swap`, `choice`
- 事务组：2（唯一 1，多解 1，无法恢复 0，受阻 0）
- 任何排序都只能整组发送：`swap`

## 重放批次

每组按提交顺序整组发送一次 `POST /edits`；`edits` 的下标与 `source_lines` 一一对应。

### 1. `swap` — 唯一可重放

- 涉及图层：`yard`
- 源行（接收顺序）：3, 1, 2
- 重放源行顺序：`1, 2, 3`
- 最终形状摘要：`sha256:e8bf63b6f4acea16e5a07c020eb3dd83d0c1b986bffa73ff8ddcbe2febc075c8`

```json
{"edits":[{"layer":"yard","id":"alpha","mode":"replace","value":{"id":"alpha","class":"fixture","shape":{"extent":{"min_x":6,"min_y":0,"max_x":10,"max_y":4}}}},{"layer":"yard","id":"gamma","mode":"replace","value":{"id":"gamma","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":4}}}},{"layer":"yard","id":"beta","mode":"replace","value":{"id":"beta","class":"fixture","shape":{"extent":{"min_x":12,"min_y":0,"max_x":16,"max_y":4}}}}]}
```

按本批次顺序必须整组一次发送；拆批会在下列边界被拒绝：

- 源行 1 之后切分：前缀批次被拒绝 `conflict` — exclusive layer yard: alpha intersects beta
- 源行 2 之后切分：前缀批次被拒绝 `conflict` — exclusive layer yard: alpha intersects beta

> 共 6 种排序合法，但全部收敛到同一最终形状，因此重放无歧义
> 本组每一种合法排序都存在被单独拒绝的提交边界，因此无论顺序如何都只能整组一次发送

### 2. `choice` — 多解，不可无歧义恢复

- 涉及图层：`yard`
- 源行（接收顺序）：11, 12

存在 2 种合法最终形状，账本未记录原提交是哪一种：

- `sha256:bcd16e866b1d6111e9b890643daf7d0840d49c38b4cf167693630d354507972c`（1 种排序），例如源行 `11, 12`
- `sha256:5e366d9f713abaa5b28613aa7bc1ae9a479da983c35d9d8abed259c546ded7e3`（1 种排序），例如源行 `12, 11`

不可无歧义恢复；需人工指定目标形状后才能生成批次。

> 存在 2 种可达的最终形状；账本没有记录原提交落在哪一种

## 无法无歧义恢复的清单

- `choice`：2 种合法排序产生不同最终形状
