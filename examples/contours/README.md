# 轮廓线数据与小样（contour samples）

用 Meridian Patch 的**公开 HTTP 接口**对一个 `extent`（轴对齐矩形）图层生成窗口内的
并集轮廓线，供现场纸面核对。脚本只调用现有接口，不修改服务端任何代码。

## 复跑

```sh
examples/contours/run_demo.sh
```

该脚本会：

1. `go build ./...` 构建服务；
2. 在系统分配端口、临时数据目录启动独立进程（不碰任何运行中的库）；
3. 建三个**排他**矩形图层并写入样例地物（排他层允许接边、允许角点接触，禁止正面积重叠）；
4. 生成 `samples/` 下的 JSON 与 ASCII 小样；
5. 在另一个全新数据目录重跑 `ring-open`，逐字节比对 JSON，验证可复跑；
6. 退出时终止进程并清理临时目录。

Python 以 `python3 -I` 隔离模式运行：脚本目录与当前目录不进入 `sys.path`，
数据目录里即使有同名模块（如 `json.py`）也不会被导入。仅依赖标准库。

也可对已运行的服务单独调用生成器：

```sh
python3 -I examples/contours/contour_tool.py \
  --url http://127.0.0.1:8741 --layer ring-open \
  --window 0,0,12,12 --page-limit 2 \
  --out /tmp/contours --name my-sample
```

## 数据从哪些公开接口来

| 用途 | 接口 |
| --- | --- |
| 窗口内取地物（签名书签分页） | `POST /strata/{layer}/window` |
| 覆盖/未覆盖面积独立核对 | `POST /strata/{layer}/coverage` |
| 内部共享边独立核对 | `POST /strata/{layer}/seams` |
| 翻页失效演练的同层写入 | `POST /edits` |

窗口是半开区间 `[min_x,max_x) × [min_y,max_y)`：落在窗口**右、上**边的矩形边不被窗口
拥有，不会产出切边；左、下边计入。这与服务端半开规则一致。

## 每条线记录什么

`*.contour.json` 的 `contour_lines[]` 每条含：

- `start` / `end`：起止整数点；
- `axis`：`vertical` 或 `horizontal`；
- `orientation`：沿线段朝向（`up`/`down`/`left`/`right`），**约定覆盖区始终在行进方向左侧**；
- `length`：整数长度；
- `source_feature_ids`：拥有该边的原地物 ID（接边处多条矩形共线时可能有多个 ID）；
- `kind`：三类之一——
  - `outer`：覆盖区与窗口外部之间的外圈边；
  - `hole`：被覆盖区围住的留空（洞）的边；
  - `window-cut`：地物被窗口左/下边界切出来的边。

### 三类边如何区分

对窗口内矩形做坐标压缩得到网格，逐格判定覆盖/留空；对窗口边界留空格做洪水填充：

- 留空格能连通到窗口边界 → 相邻覆盖边是 `outer`；
- 留空格被完全围住、到不了窗口边界 → 相邻覆盖边是 `hole`；
- 覆盖边本身落在窗口左/下边界上（空侧在窗口之外）→ `window-cut`。

内部接边（两块矩形共享的正长度边）两侧都被覆盖，不产生轮廓线；这与 `seams` 接口
互为反证——小样的 `cross_checks.seams_absent_from_contour` 保证共享边不会漏成轮廓。

## 交叉核对（写在 JSON 的 `cross_checks`）

- `coverage_identity_holds`：服务端 `covered + uncovered == 窗口面积`；
- `seams_absent_from_contour`：服务端返回的每条共享边都不出现在轮廓线上；
- `covered_on_left_holds`：逐条线在中点左邻格取样，覆盖区确实在左侧；
- `counts_by_kind`、`contour_total_length`：分类计数与总长，便于纸面点数。

## 三个样例

### 1. `ring-open` —— 环形留空 + 窗口切边

四条仅接边的矩形拼成环，围住开口 `[4,8) × [4,8)`；西臂与底边贴窗口边界产生切边，
东、上边界在窗内成为外圈。预期：`outer` 2 条、`hole` 4 条、`window-cut` 2 条，
覆盖面积 84，服务端共享边 4 条且全部不进轮廓。

### 2. `corner-touch` —— 两块仅在角点相遇

`corner-alpha=[1,5)×[1,5)` 与 `corner-beta=[5,9)×[5,9)` 只在点 `(5,5)` 接触。
角点接触不构成正长度共享边：服务端 `seams` 为 0，两块各自保留独立外圈，
没有任何一条线同时引用两个地物 ID。预期 8 条全为 `outer`。

### 3. `paging-stale` —— 翻页期间数据变化

窗口分页大小设为 1，读到第 1 页后向同一图层插入一个新地物，再带书签取第 2 页。
书签绑定图层修订，服务端返回 `409 conflict / bookmark revision has expired`。
工具据此**中止并写出 `aborted: true`、空 `contour_lines` 的报告**，绝不把旧页与新页
拼成一个轮廓。普通成功读取则要求各页 `layer_revision` 一致
（`pagination.single_layer_revision`）。

## 修订口径

- `snapshot.layer_revision` 是该图层几何的身份，也是签名书签绑定的修订；同几何重跑稳定，
  因此纳入 JSON 并参与逐字节复跑比对。
- 全库 `revision` 会因其他图层的写入而推进，不属于本图层轮廓的身份，仅在控制台打印
  （`database_revision=...`），不写入 JSON，避免无关图层变更污染复跑哈希。
