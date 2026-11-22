# 零距离判定迁移手册

占位审核与接边巡检两个客户端目前都把 `distance_squared == 0` 当作碰撞。本手册说明为什么这个判定是错的，给出一组可直接运行的请求覆盖四种典型输入，并比较两条接入路径的迁移方法。

文中所有请求与响应都来自配套脚本 [examples/zero-distance-migration.sh](../examples/zero-distance-migration.sh) 对真实服务的运行结果（步骤编号 S1–S5、O1、Q1–Q9、A1–A3 与脚本一一对应）。在空数据目录下启动服务后运行脚本，可逐条复现本手册。

```sh
go build -o meridian ./cmd/meridian
./meridian -listen 127.0.0.1:8741 -data ./data   # 空数据目录
examples/zero-distance-migration.sh               # 安装 jq 时会断言全部关键字段
```

## 1. 背景：为什么"距离为 0"不等于碰撞

`distance_squared` 是**闭包**（closed envelope）上的欧氏距离平方，而归属与相交使用**半开**规则（见 PROJECT_SPEC.md）：

- 点归属采用左下包含、右上排除的半开区间；
- 矩形相交指正面积交集；
- 闭包只用于最近邻距离和索引裁剪——沿开边的点可具有零距离，即使不被该矩形拥有。

因此 `distance_squared == 0` 只是"可能接触"的候选条件：半开边上的点、角触、共享边都会得到零距离，但它们在半开判定下并不相交。反过来，服务端的排他图层在提交时拒绝的是正面积重叠、允许接边——客户端用零距离当碰撞，判定口径与服务端不一致，会把合法接边误报为冲突。

替代原语（均携带 `revision`，可对照读取视图）：

| 端点 | 判定内容 | 关键字段 |
| --- | --- | --- |
| `POST /relation` | 同一库视图下两个已入库地物的半开相交、双向包含、闭包距离 | `intersects`, `left_contains`, `right_contains`, `distance_squared`, `intersection` |
| `POST /join` | 窗口内两图层的相交对，**先把形状裁到窗口再判定** | `pairs[].intersection`（正面积证据） |
| `POST /strata/{layer}/seams` | 窗口内正长度共享边（角触与正面积交叠都不算） | `seam.axis`, `seam.fixed`, `seam.from`, `seam.to` |
| `POST /strata/{layer}/locate` | 半开规则下哪些矩形拥有该点 | `features` |

注意 `/relation` 与 `/join` 都按图层名加地物 ID 解析**已入库**地物。占位审核的典型流程是：把候选地物写入一个非排他暂存图层，用 `/join`（暂存层 × 存量层）按窗口取相交对，判定后清空暂存层。

## 2. 基线数据（S1–S5）

三个图层，边界均为 `[-20,-20,24,24)`，非排他：

- `plots`（extent）：`alpha`=[0,0,4,3)，`beta`=[4,0,8,2)，`gamma`=[4,3,8,6)，`delta`=[10,0,16,6)
- `overlays`（extent）：`epsilon`=[14,4,20,10)
- `markers`（mark）：`pin-edge`=(4,1)，`pin-corner`=(4,3)

```
 y
10|                                ┌───────────┐
  |                                │  epsilon  │
 6|          ┌───────┐   ┌─────────┼───┐       │
  |          │ gamma │   │ delta   │   │       │
 4|          │       │   │      [交集]  │       │
 3| ┌───────┼──•────┤    │         │   │       │    • = pin-corner (4,3)
 2| │ alpha ├───────┤    │         └───┼───────┘
 1| │   •   │ beta  │    │             │          • = pin-edge (4,1)
 0| └───────┴───────┘    └─────────────┘
  +─0───────4───────8────10────14───16───20──→ x
```

（示意，不按比例。`alpha` 与 `beta` 共享竖直边 x=4；`alpha` 与 `gamma` 只在角点 (4,3) 接触；`delta` 与 `epsilon` 的正面积交集是 [14,4,16,6)。）

```sh
# S1–S3：建层（各返回 201，revision 依次为 1、2、3）
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"name":"plots","kind":"extent","bounds":{"min_x":-20,"min_y":-20,"max_x":24,"max_y":24},"exclusive":false}' \
  http://127.0.0.1:8741/meridian/v1/strata
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"name":"overlays","kind":"extent","bounds":{"min_x":-20,"min_y":-20,"max_x":24,"max_y":24},"exclusive":false}' \
  http://127.0.0.1:8741/meridian/v1/strata
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"name":"markers","kind":"mark","bounds":{"min_x":-20,"min_y":-20,"max_x":24,"max_y":24},"exclusive":false}' \
  http://127.0.0.1:8741/meridian/v1/strata

# S4：一个写包写入全部 7 个地物（返回 200，revision 4）
curl -s -X POST -H 'Content-Type: application/json' -d '{
  "edits": [
    {"layer":"plots","id":"alpha","mode":"insert","value":{"id":"alpha","class":"parcel","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}}},
    {"layer":"plots","id":"beta","mode":"insert","value":{"id":"beta","class":"parcel","shape":{"extent":{"min_x":4,"min_y":0,"max_x":8,"max_y":2}}}},
    {"layer":"plots","id":"gamma","mode":"insert","value":{"id":"gamma","class":"parcel","shape":{"extent":{"min_x":4,"min_y":3,"max_x":8,"max_y":6}}}},
    {"layer":"plots","id":"delta","mode":"insert","value":{"id":"delta","class":"parcel","shape":{"extent":{"min_x":10,"min_y":0,"max_x":16,"max_y":6}}}},
    {"layer":"overlays","id":"epsilon","mode":"insert","value":{"id":"epsilon","class":"overlay","shape":{"extent":{"min_x":14,"min_y":4,"max_x":20,"max_y":10}}}},
    {"layer":"markers","id":"pin-edge","mode":"insert","value":{"id":"pin-edge","class":"survey","shape":{"mark":{"x":4,"y":1}}}},
    {"layer":"markers","id":"pin-corner","mode":"insert","value":{"id":"pin-corner","class":"survey","shape":{"mark":{"x":4,"y":3}}}}
  ]
}' http://127.0.0.1:8741/meridian/v1/edits

# S5：确认基线修订
curl -s http://127.0.0.1:8741/meridian/v1/status
# {"revision":4,"writable":true}
```

以下 Q1–Q9 全部在**同一修订 4** 上读取（每个响应都带 `revision`，脚本逐条断言了这一点）。

## 3. 旧路径对照（O1）

旧逻辑的取数方式：对候选点调用 `/nearest`，把 `distance_squared == 0` 的邻居全部记为碰撞。

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"point":{"x":4,"y":1},"limit":4}' \
  http://127.0.0.1:8741/meridian/v1/strata/plots/nearest
```

```json
{"revision":4,"layer_revision":4,"neighbors":[
  {"feature":{"id":"alpha","class":"parcel","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}},"distance_squared":0},
  {"feature":{"id":"beta","class":"parcel","shape":{"extent":{"min_x":4,"min_y":0,"max_x":8,"max_y":2}}},"distance_squared":0},
  {"feature":{"id":"gamma","class":"parcel","shape":{"extent":{"min_x":4,"min_y":3,"max_x":8,"max_y":6}}},"distance_squared":4},
  {"feature":{"id":"delta","class":"parcel","shape":{"extent":{"min_x":10,"min_y":0,"max_x":16,"max_y":6}}},"distance_squared":36}],
 "visit":{"nodes":1,"candidates":4}}
```

`alpha` 与 `beta` 都是零距离，旧逻辑记录两起碰撞。但 (4,1) 在 `alpha` 的右上（排除）边上，`alpha` 并不拥有它——见 Q1/Q2。

## 4. 四种输入的同一组请求（Q1–Q9）

### 4.1 半开边上的点（Q1、Q2）

`pin-edge` (4,1) 落在 `alpha`=[0,0,4,3) 的排除边 x=4 上。

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":{"layer":"plots","id":"alpha"},"right":{"layer":"markers","id":"pin-edge"}}' \
  http://127.0.0.1:8741/meridian/v1/relation
```

```json
{"revision":4,"left_revision":4,"right_revision":4,
 "intersects":false,"left_contains":false,"right_contains":false,"distance_squared":0}
```

`distance_squared` 为 0 但 `intersects` 为 `false`：旧布尔是误报。真正拥有该点的是 `beta`：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"x":4,"y":1}' \
  http://127.0.0.1:8741/meridian/v1/strata/plots/locate
```

```json
{"revision":4,"layer_revision":4,
 "features":[{"id":"beta","class":"parcel","shape":{"extent":{"min_x":4,"min_y":0,"max_x":8,"max_y":2}}}],
 "visit":{"nodes":1,"candidates":2}}
```

### 4.2 角触（Q3、Q4、Q5）

`alpha`=[0,0,4,3) 与 `gamma`=[4,3,8,6) 只在角点 (4,3) 接触。

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":{"layer":"plots","id":"alpha"},"right":{"layer":"plots","id":"gamma"}}' \
  http://127.0.0.1:8741/meridian/v1/relation
```

```json
{"revision":4,"left_revision":4,"right_revision":4,
 "intersects":false,"left_contains":false,"right_contains":false,"distance_squared":0}
```

响应与 Q1 形状相同（零距离、不相交、无 `intersection` 字段）——旧布尔同样误报，且无法区分"开边上的点"与"角触"。角点本身的归属是确定的：(4,3) 是 `gamma` 的左下角，被 `gamma` 拥有：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":{"layer":"plots","id":"gamma"},"right":{"layer":"markers","id":"pin-corner"}}' \
  http://127.0.0.1:8741/meridian/v1/relation
```

```json
{"revision":4,"left_revision":4,"right_revision":4,
 "intersects":true,"left_contains":true,"right_contains":false,"distance_squared":0}
```

角触不产生共享边。全窗查询只返回 `alpha`–`beta` 的正长度共享边（此响应同时是 4.4 的未裁切基线）：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"bounds":{"min_x":0,"min_y":0,"max_x":8,"max_y":6},"limit":10}' \
  http://127.0.0.1:8741/meridian/v1/strata/plots/seams
```

```json
{"revision":4,"layer_revision":4,
 "seams":[{"first":"alpha","second":"beta","seam":{"axis":"vertical","fixed":4,"from":0,"to":2}}],
 "visit":{"nodes":4,"candidates":10}}
```

### 4.3 裁切前相交而窗内不相交（Q6、Q7、Q8）

`delta`=[10,0,16,6) 与 `epsilon`=[14,4,20,10) 的正面积交集是 [14,4,16,6)。逐对判定确认它们真相交，并给出交集证据：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":{"layer":"plots","id":"delta"},"right":{"layer":"overlays","id":"epsilon"}}' \
  http://127.0.0.1:8741/meridian/v1/relation
```

```json
{"revision":4,"left_revision":4,"right_revision":4,
 "intersects":true,"left_contains":false,"right_contains":false,"distance_squared":0,
 "intersection":{"min_x":14,"min_y":4,"max_x":16,"max_y":6}}
```

但 `/join` 先把两个形状裁到查询窗口再判定。窗口 [10,0,14,4) 不覆盖交集时，`epsilon` 被裁为空，该对不出现：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":"plots","right":"overlays","bounds":{"min_x":10,"min_y":0,"max_x":14,"max_y":4},"limit":10}' \
  http://127.0.0.1:8741/meridian/v1/join
```

```json
{"revision":4,"left_revision":4,"right_revision":4,"pairs":[],"visit":{"nodes":2,"candidates":2}}
```

同一对、同一修订，窗口覆盖交集时才返回，并附窗内交集：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":"plots","right":"overlays","bounds":{"min_x":10,"min_y":0,"max_x":20,"max_y":10},"limit":10}' \
  http://127.0.0.1:8741/meridian/v1/join
```

```json
{"revision":4,"left_revision":4,"right_revision":4,
 "pairs":[{"left":"delta","right":"epsilon","intersection":{"min_x":14,"min_y":4,"max_x":16,"max_y":6}}],
 "visit":{"nodes":2,"candidates":2}}
```

要点：零距离（甚至 `intersects: true`）回答的是"这两个地物是否相交"，回答不了"窗内是否相交"。按窗口出工单的客户端必须以 `/join` 的窗内判定为准，并把 `intersection` 作为证据留存。

### 4.4 共享边被窗裁成较短线（Q9，对照 Q5）

`alpha` 与 `beta` 的完整共享边是 x=4、y∈[0,2)（Q5）。把窗口收窄到 [3,-1,5,1)，同一对、同一修订，返回的共享边被裁成 y∈[0,1)：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"bounds":{"min_x":3,"min_y":-1,"max_x":5,"max_y":1},"limit":10}' \
  http://127.0.0.1:8741/meridian/v1/strata/plots/seams
```

```json
{"revision":4,"layer_revision":4,
 "seams":[{"first":"alpha","second":"beta","seam":{"axis":"vertical","fixed":4,"from":0,"to":1}}],
 "visit":{"nodes":3,"candidates":7}}
```

`from`/`to` 是"共享边 ∩ 窗口"的区间。接边巡检若只存区间长度，跨窗口比较会把裁切误判为边变短——必须连同窗口与修订一起存（见第 6 节）。

### 4.5 判定对照表

| 输入 | `distance_squared` | `intersects` | 旧布尔结论 | 占位审核应采用 | 接边巡检应采用 |
| --- | --- | --- | --- | --- | --- |
| 半开边上的点 alpha×pin-edge（Q1） | 0 | false | 碰撞（误报） | 不碰撞；拥有者见 `/locate`（Q2） | 非接边 |
| 角触 alpha×gamma（Q3） | 0 | false | 碰撞（误报） | 不碰撞 | 非接边（Q5 中无此缝） |
| delta×epsilon，窄窗（Q6、Q7） | 0 | true | 碰撞（窗内误报） | 窗内不碰撞（`pairs` 为空） | 以 `/join` 窗口判定为准 |
| alpha×beta，窄窗（Q9） | 0 | false | 碰撞（误报） | 不碰撞 | 接边成立，证据为 `axis`/`fixed`/`from`/`to` |

## 5. 接入路径 A：只存一枚布尔值的消费者（占位审核）

### 5.1 切换规则

存储结构不变（仍一枚布尔），只改判定来源：

| | 旧 | 新 |
| --- | --- | --- |
| 判定 | `collided = (distance_squared == 0)` | `collided = intersects` |
| 取数 | `/nearest` 的零距离邻居 | 逐对 `/relation`；按窗批量用 `/join`（暂存层 × 存量层） |
| 辅助列 | 无 | 可保留 `distance_squared` 作"相邻"提示，但不得再参与碰撞结论 |

切换后与服务端口径一致：排他图层提交时校验的正是正面积重叠（允许接边），`intersects` 是同一判定。

### 5.2 读同一修订的例子（批量回填）

回填必须保证一批行读自同一视图，否则碰撞结论可能混合多个修订：

```sh
# 1. 快照当前修订
curl -s http://127.0.0.1:8741/meridian/v1/status
# {"revision":4,"writable":true}  → 记 R=4

# 2. 逐行调用 /relation，校验响应的 revision == R，且 left_revision/right_revision 与首轮一致
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":{"layer":"plots","id":"alpha"},"right":{"layer":"markers","id":"pin-edge"}}' \
  http://127.0.0.1:8741/meridian/v1/relation
# {"revision":4,"left_revision":4,"right_revision":4,"intersects":false,...}  → 本行 collided := false

# 3. 若任一响应的 revision 不等于 R：回填期间有写入，重新执行第 1 步再跑本批
```

单次响应总是取自一个不可变视图（自洽）；上述校验防的是**跨行**混入不同修订。若回填后要基于判定写回数据，用 `if_revision` 做全库 CAS——陈旧修订被 409 拒绝且不推进修订（A2、A3）：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"if_revision":999,"edits":[{"layer":"plots","id":"alpha","mode":"remove"}]}' \
  http://127.0.0.1:8741/meridian/v1/edits
# HTTP 409  {"code":"conflict","message":"expected database revision 999, current revision is 4"}

curl -s http://127.0.0.1:8741/meridian/v1/status
# {"revision":4,"writable":true}  → 被拒绝的写包没有推进修订
```

### 5.3 处理旧数据的例子

旧表 `review_pairs(layer_a, id_a, layer_b, id_b, collided)` 只有布尔、没有修订。逐行回填：

| 旧 `collided` | 新 `intersects` | 含义 | 处理 |
| --- | --- | --- | --- |
| 1 | true | 真碰撞（如 delta×epsilon，Q6） | 保留，补记 `checked_revision` |
| 1 | false | 零距离误报：开边上的点或角触（如 Q1、Q3） | 改为 0；`distance_squared==0` 的可另记"相邻"提示 |
| 0 | true | 理论上不会出现（相交必有零距离）；出现即说明旧行不是按距离规则生成的 | 改为 1 并排查旧数据来源 |
| 0 | false | 一致 | 不动 |
| — | 404 | 地物已删 | 标记 `stale` 转人工，不得用旧布尔猜测 |

地物已删时 `/relation` 返回 404（A1）：

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"left":{"layer":"plots","id":"beta"},"right":{"layer":"markers","id":"pin-gone"}}' \
  http://127.0.0.1:8741/meridian/v1/relation
# HTTP 404  {"code":"missing","message":"feature \"pin-gone\" does not exist in layer \"markers\""}
```

## 6. 接入路径 B：保留方向与裁切证据的新消费者（接边巡检）

### 6.1 记录结构

新消费者不应只存结论，应把可解释的方向与裁切证据一并落库。接边记录建议：

```json
{
  "pair": {"first": "alpha", "second": "beta"},
  "direction": {"axis": "vertical", "fixed": 4},
  "clipped_span": {"from": 0, "to": 1},
  "window": {"min_x": 3, "min_y": -1, "max_x": 5, "max_y": 1},
  "revision": 4,
  "layer_revision": 4
}
```

- **方向证据**：`axis`（共享线走向）与 `fixed`（固定坐标），直接取自 `seam`。
- **裁切证据**：`clipped_span` 是"共享边 ∩ 窗口"的区间。只有连同 `window` 与 `revision` 一起存，才能回答"这次看到的边为什么比上次短"——是窗口裁切（Q9 对照 Q5）还是数据真变了。
- 碰撞类判定同理：存 `intersects`、双向 `left_contains`/`right_contains`（包含方向）与 `intersection`（正面积证据），而不是只存布尔。

### 6.2 读同一修订的例子

对照 Q5 与 Q9：两次 `/seams` 的 `revision` 与 `layer_revision` 都是 4，而 `from`/`to` 从 [0,2) 变为 [0,1)——差异完全来自窗口裁切，不是数据变化。巡检比对的规则：

1. 比较两条接边记录前，先对齐 `layer_revision`：相同则可安全比较 `from`/`to`；不同则先查 `GET /strata` 确认图层何时被写，再用相同窗口重测。
2. 跨窗口比较没有意义：先把窗口对齐，再比较区间。

### 6.3 处理旧数据的例子

旧接边表只有 `(first, second)` 和"距离 0"标记，没有几何证据。重分类流程：

1. 用巡检窗口跑 `/seams`，得到当前接边集合 S（带方向与裁切证据）。
2. 旧对在 S 中 → 升级为带证据记录。例：`(alpha, beta)` 命中 Q5/Q9，补记 `axis=vertical, fixed=4, from/to` 与窗口、修订。
3. 旧对不在 S 中 → 调 `/relation` 区分：
   - `intersects: true` → 正面积重叠，不是接边；非排他图层可能出现，转冲突工单（排他图层在提交时就进不来）。
   - `distance_squared == 0` 且 `intersects: false` → 角触或开边接触。例：`(alpha, gamma)` 经 Q3 确认为角触，降级为提示，不再当接边。
   - `distance_squared > 0` → 已分离，关闭。
4. `/relation` 返回 404 → 地物已删，归档（同 A1）。

## 7. 错误码与边界备忘

- `400 invalid`：窗口非正面积、`limit` 超界、书签与谓词不符等。
- `404 missing`：图层或地物不存在（回填见 5.3）。
- `409 conflict`：`if_revision` 陈旧（见 5.2）、书签修订过期。
- `422 capacity`：`/join` 与 `/seams` 的结果或候选超预算——缩小窗口分批重试。
- 所有响应整数为 JSON 整数；修订、面积、距离可能超出 32 位，客户端应使用可表示 64 位整数的解码器。
