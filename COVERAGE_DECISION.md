# 覆盖面积扫描线：逐事件长度树 vs 稀疏区段模型

本文只讨论 `POST /strata/{layer}/coverage` 的单次求值策略选择，给出可复算的观测量和切换判据。
仓库中的实测入口是 `internal/spatial/coverage_decision_probe_test.go`（独立诊断探针，生产代码不引用）。

## 1. 现状：逐事件维护的长度树

`internal/spatial/coverage.go` + `internal/spatial/lengths.go` 的实现是标准两次扫描：

1. 索引闭包选出候选，逐矩形 `window.Clip`，产出两个 x 事件 `(x, y0, y1, ±1)`。
2. 所有 `y0/y1` 排序去重得 `B` 个 y 断点，建线段树 `4B` 个槽位。
3. x 事件排序后逐事件前进：用 `width * 根节点 once/twice` 累加面积，再对该事件的 `[y0,y1)` 做一次区间加。

关键事实：`lengthNode.once/twice` 是**长度和**，不是面积，也不是计数。节点维护三个量：
覆盖计数 `count`、至少一次长度 `once`、至少两次长度 `twice`。合并规则（`lengths.go:39`）：

- `count >= 2`：`once = twice = span`
- `count == 1`：`once = span`，`twice = 子节点 once 之和`
- `count == 0`：`once = 子节点 once 之和`，`twice = 子节点 twice 之和`

这三分支是**精确的**：`twice` 不是 `covered - once` 的近似，`once` 也不是把重复面积再累加。
每个事件只做一次 O(log B) 区间加，没有重算。因此三种语义的正确性由构造保证：

| 语义 | 长度树上的取法 | 保证强度 |
| --- | --- | --- |
| 至少一次（并集） | 根 `once` | 精确，与重叠次数无关 |
| 至少两次 | 根 `twice` | 精确，三重及以上也只算一次 |
| 恰好一次 | `once - twice` | 精确，但当前 API 未暴露该字段 |
| 未覆盖 | `window.Area() - covered` | 精确，闭包恒等式 |

整数边界：面积为 `(max-min)` 的乘积，坐标为 `|c| ≤ 1e9`，窗口最大 `(2e9)² = 4e18 < 2^63-1`，
中间量只有面积与长度两项，均不溢出。这一点已用探针中的 `TestCoverageDecisionIntegerBoundaries`
和线上 HTTP 验证（见 §4）。

## 2. 对照模型：带精确计数的稀疏不交区段

稀疏模型不建树，而是在扫描线上维护一组**有序、互不相交、带精确重数 `count` 的 y 区段**：

```
segs = [(lo, hi, count), ...]        // count >= 1，升序，相邻不重叠
增量维护 once = Σ(hi-lo)、twice = Σ_{count>=2}(hi-lo)
每个 x 事件：在 [y0,y1) 上把 count 加 delta
```

一次更新只在被事件触碰的区段内做局部重切并回并。它的对外保证与长度树**逐项相同**
（两者在本仓库的全部差分测试中给出完全相同的 `covered/repeated/uncovered`），差别只在开销结构与内存：

| 维度 | 长度树（当前） | 稀疏区段 |
| --- | --- | --- |
| 每次更新成本 | O(log B)，与覆盖深度无关 | O(S_max)，S_max = 同时活跃的段数 |
| 内存 | 4B × 24B，B 由全窗断点数决定 | ≤ S_max 段，通常几十字节到几 KB |
| 额外可观测 | 无 | 精确重数直方图：面积按 `count` 分桶 |
| 恰好一次 | 可由 `once-twice` 取得 | 天然逐段可得 |
| 重复覆盖 | 只给"≥2 的并集" | 可精确回答"恰好 k 次" |

所谓"稀疏区段模型能保证什么"：**它把每个 y 单元的精确重数保留下来**，因此可以在同一个
数据结构上同时回答恰好一次、至少一次、至少 k 次、以及"面积 (i,j] 的区段长度"这类问题；
长度树只保留 `once/twice` 两个聚合，无法回答"恰好 k 次"或"三等以上的分布"，除非额外加维度。
两者对 API 当前暴露的三个字段给出相同结果，这一点已经过 2000+ 次随机数据差分验证。

## 3. 两个决定性观测

真正决定开销的不是"坐标跨度"，而是**扫描线上的活跃段数**。这可以从数据里直接读出来：

- `B`：`y0/y1` 去重后的断点数（窗内候选矩形数的 2 倍，上限 2R）。
- `S_max`：扫描线任一 x 处的活跃 y 区段数；即"某条竖线上最多有几个被覆盖的 y 带"。
- `S_sum`：稀疏模型在所有事件上的活跃段数之和，约等于其总工作量。

长度树的每次更新是 O(log B)，稀疏模型是 O(S_max)。因此切换点是 `S_max` 与 `log₂B`
的量级比较，而不是坐标范围。下面两个例子把这一点分别钉死。

### 例子 A：区域几乎全空、边沿密集变化

4096 个 1×1 方格沿左下边排成阶梯，扫描窗口是整平面 `[-1e9,1e9)²`：

| 观测量 | 值 |
| --- | --- |
| 候选矩形 R | 4096 |
| y 断点 B | 4097 |
| S_max | **1**（任一 x 处只有 1 个格子在覆盖线上） |
| S_sum | 4096 |
| covered / repeated / exactly_once | 4096 / 0 / 4096 |
| uncovered | 3999999999999995904（窗口面积 4e18 的 99.9999999999999%） |

结论：这是"覆盖面积几乎为 0、但断点极多"的极端情形。长度树要开 4×4097 个槽位，
每个事件走一次 O(log B)；稀疏模型全程只有 1 段，内存约 24B。
实测同一坐标集合在 scale=1、1e6、1e9 三档下 `B`、访问次数、耗时完全一致，说明**坐标跨度本身不进入成本**。
探针实测（`BenchmarkCoverageEdgeDenseEmpty` vs `BenchmarkProbeSparseEdgeDenseEmpty`）：

```
tree   ≈ 2.02 ms/op   1.64 MB/op   35 allocs/op
sparse ≈ 1.16 ms/op   2.02 MB/op   12306 allocs/op
```

稀疏模型在这个例子快约 1.7 倍，但每次更新都要分配，alloc 数高三个数量级。
在 A 类数据上两者都是亚毫秒到毫秒级，**不值得为它单独切换**。

### 例子 B：多层长条重叠

每层 1024 条几乎横贯窗口的长条，x 起点错开、y 区间互相重叠，三层是各自独立的 y 带
（层级之间不重叠，所以逐层可加）。实测单层（`ProbeExampleLongStrips(1024, 290)`）：

| 观测量 | 值 |
| --- | --- |
| 候选矩形 R | 1024 |
| y 断点 B | 1314 |
| S_max | 579 |
| S_sum | 1017593 |
| covered / repeated / exactly_once | 26259989524480 / 26219989518720 / 40000005760 |
| uncovered | 3999737400104755200 |

三层只是把 `covered/repeated/B/S_max/S_sum` 各自相加（y 带互不重叠，覆盖不会跨层重叠）。

结论：这是"跨度大 + 局部对象密"的典型请求。覆盖率超过 99.999999%，`S_max=579` 与 `B=1314` 同量级，
而长度树每事件只要 O(log₂1314)≈10 步。探针实测：

```
tree   ≈ 0.78 ms/op   431 KB/op      27 allocs/op
sparse ≈ 44.2 ms/op   77.0 MB/op  19483 allocs/op      → 约 56× 慢
```

容量上限（单层 4096 条、`c=4096` 深度重叠）：

```
tree   ≈ 4.49 ms/op   2.02 MB/op        35 allocs/op
sparse ≈ 1571 ms/op   2.92 GB/op   116485 allocs/op   → 约 350× 慢
```

## 4. 切换判据

由 §3 的实测，切换点由 `S_max / log₂B` 决定，而不是坐标跨度。把两层扫描的耗时拆开：

- 长度树 ≈ `B·log B` 的初始化 + 每事件 `log B`；
- 稀疏模型 ≈ 每事件 `S_max` 的局部重切与分配。

扫描实测（`MERIDIAN_COVERAGE_SCAN=1 go test ./internal/spatial -run SwitchScan -v`，
R=1024 长条，深度 c 控制 S_max）：

| S_max | tree ns/op | sparse ns/op | 谁赢 |
| --- | --- | --- | --- |
| 1 | 549k | 464k | sparse（微弱） |
| 3 | 580k | 702k | tree |
| 5 | 596k | 891k | tree |
| 7 | 619k | 1183k | tree |
| 19 | 635k | 2761k | tree |
| 159 | 769k | 14045k | tree |
| 1279 | 875k | 79792k | tree |

并扫描 R = 512/1024/2048/4096 验证这不是单一尺寸的偶然：`S_max=1` 时 sparse/tree ≈ 0.76–0.89，
`S_max=2` 时约 1.1–1.4，`S_max=7` 时约 1.5–2.2。也就是说**只要 S_max ≥ 2，长度树就基本已经领先**；
`log₂B` 的影响体现在 tree 的斜率上，B 从 513 到 4097 时 tree 的绝对成本涨了约 11 倍。

**可复核判据**：在一次 coverage 求值里读两个观测量：

```
B     = len(unique ys)               // 已由当前实现产出
S_max = 稀疏模型扫描中的峰值活跃段数   // 需要先跑一遍稀疏扫描才能得到
```

- 若 `S_max == 1`（等价地：每个 x 处的覆盖带互不相接），稀疏模型胜出，且内存从 `O(B)` 降到 O(1)。
  这正是例子 A 和"任何窗口内只有离散、互不重叠的长条"的情形。
- 若 `S_max >= 2`，长度树胜出，且差距随 `S_max` 单调扩大（`S_max=1279` 时已 90×）。
- 由于 API 上限是每层 4096 个矩形，`B ≤ 8192`，`log₂B ≤ 13`，长度树单次更新不超过 13 步；
  **在当前容量约束下没有任何数据分布能让稀疏模型的总工作量低于长度树**，除非 `S_max==1`。

因此**默认保持现状**。只有当出现以下全部条件时，才值得为 `S_max==1` 引入稀疏快路径：
窗口内矩形 y 带互不离散重叠、且求值规模接近容量上限、且 `visit` 数据显示候选集在 4096 量级。
其余情况下长度树对三种语义都是精确的，且是更好的选择。

## 5. 一致性验证（可复核）

- 探针对照：`probeReference` 用坐标压缩的单元分解独立计算 `covered/repeated/uncovered`，不共享任何扫描逻辑。
  - 600 次小网格随机 + 300 次大坐标随机：长度树、稀疏模型、参考解三者完全一致。
  - 15 类整数边界（全窗口、两角 1×1、跨中线条带、半开接边、三重覆盖、4096 重覆盖）全部一致。
  - 线上 HTTP 复核（`examples/walkthrough.py` 全 15 个 workflow 通过）：随机 300 次小网格无差异；
    边界用例中 `full-window` 得 4e18、`overlap-4096` 得 covered=repeated=4e18、`triple` 得 exactly_once=0，
    与 `[min,max)` 半开语义和"重复不按次数相加"规格相符。
- 回归：`go build ./...`、`go vet ./...`、`go test ./...` 全绿；15 个生产 workflow 全通过。

## 6. 复现命令

```sh
go test ./internal/spatial -run TestCoverageDecision -v                          # 快速差分 + 两例子
go test ./internal/spatial -run '^$' -bench 'Coverage|ProbeSparse' -benchtime 5x  # 两侧计时
MERIDIAN_COVERAGE_SCAN=1 go test ./internal/spatial -run 'SwitchScan|Crossover' -v # 切换点扫描
```

注意：环境中的 Go 工具链需要 `CGO_ENABLED=0`（容器内 gcc 不支持 `-m64`），
`examples/walkthrough.py` 依赖 PATH 中的 `go`。
