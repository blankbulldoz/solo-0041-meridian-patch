# Meridian Patch

Meridian Patch 是一个带持久化和几何索引的 Go HTTP 数据库。它支持半开矩形和整数点，用于空间检索、稳定分页、邻近查找、配对、接边和面积测算。

## 启动

需要 Go 1.22 或更新版本以及 Linux 或 macOS，基线仅使用 Go 标准库。演练脚本需要 Python 3。

```sh
go build -o meridian ./cmd/meridian
./meridian -listen 127.0.0.1:8741 -data ./data
python3 examples/walkthrough.py --workflow coverage
```

演练脚本启动独立进程并使用临时状态目录和系统分配的端口。它不会修改运行中的数据库。数据文件为 `meridian-state.mpb`。`meridian.writer` 由操作系统锁定，不要手动删除后尝试接管。服务默认绑定回环地址，供受信任调用者使用。

## API

所有路径以 `/meridian/v1` 开头。写入 JSON 使用 `Content-Type: application/json`。图层名、地物 ID、类别为不超过 48 字符的小写名称，首字符为小写 ASCII 字母。坐标对象中省略的整数分量使用零值。

| 方法与路径 | 请求及结果 |
| --- | --- |
| `GET /status` | 返回全库 `revision` 和 `writable` |
| `GET /strata` | 返回当前图层和各自修订、边界、排他标记与条目数 |
| `POST /strata` | `name`, `kind`, `bounds`, `exclusive`, 可选 `if_revision` |
| `PUT /strata/{layer}/layout` | 新 `bounds`、`exclusive` 及可选 `if_revision`。形状类型保持原样 |
| `POST /edits` | `edits` 与可选 `if_revision`。每项有 `layer`, `id`, `mode`，写入时提供同 ID 的 `value` |
| `POST /rehearsals` | 与 `/edits` 相同的写包，外加可选 `probes`。不落盘，返回约束结论、受影响图层与探针在基准/候选视图下的命中和面积差异 |
| `POST /strata/{layer}/window` | `bounds`, `limit`, 可选 `class`, `bookmark`。按 ID 返回 `features`，有更多数据时返回 `next` |
| `POST /strata/{layer}/locate` | `{ "x": 2, "y": 1 }`。返回拥有该点的 `features` |
| `POST /strata/{layer}/nearest` | `point`, `limit`, 可选 `class`。返回带 `distance_squared` 的 `neighbors` |
| `POST /strata/{layer}/coverage` | `bounds` 和可选 `class`。返回 `covered`, `repeated`, `uncovered` |
| `POST /strata/{layer}/seams` | `bounds`, `limit`。按 ID 对返回正长度共享边 |
| `POST /join` | `left`, `right`, `bounds`, `limit`。返回同一全库修订的相交对 |
| `POST /relation` | `left` 与 `right` 分别含 `layer` 和 `id`。返回相交、双向包含和闭包距离 |
| `GET /strata/{layer}/index` | 返回 `tree` 结构统计 |
| `GET /strata/{layer}/index-audit` | 核对索引和目录，返回 `records` 与 `checked_nodes` |
| `GET /image` | 返回 MPB v1 镜像及 `Meridian-Revision` |
| `PUT /image` | 二进制 `application/vnd.meridian.image` 与必需的 `If-Meridian-Revision` |

例如，创建矩形图层并写入一个地物使用下面的 JSON 对象。

```json
{"name":"ochre-field","kind":"extent","bounds":{"min_x":-4,"min_y":-4,"max_x":12,"max_y":12},"exclusive":false}
```

```json
{"edits":[{"layer":"ochre-field","id":"prong","mode":"insert","value":{"id":"prong","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}}}]}
```

`409 conflict` 表示修订条件失效、重名或末态违反排他策略。`400 invalid` 表示请求语义无效，`404 missing` 表示找不到图层或地物，`422 capacity` 表示数据库或查询工作量上限。正常窗口读带 `revision` 和 `layer_revision`。`visit` 报告索引访问开销，候选数可以超过真实命中数。

### 写包试演

`POST /rehearsals` 接受与 `POST /edits` 完全相同的写包（含可选 `if_revision`），但不发布任何修订、不触碰磁盘镜像。写包按输入次序暂存，并走与正式提交相同的末态规则（形状、图层边界、排他策略与容量上限）；因此被拒绝的写包返回与 `/edits` 相同的状态码和问题码。通过约束时返回：

- `ok`、`baseline_revision`（试演所基于的全库修订）与 `projected_revision`（正式提交将发布的修订）；
- `affected`：受影响图层按名称排序的末态摘要（含投影修订与条目数）；
- `probes`：每个探针在基准视图与候选视图下的结果差异。

探针 `kind` 支持 `window`、`locate`、`nearest`、`coverage`、`seams` 与 `join`，参数与对应读取端点一致（`bounds`、`point`、`class`、`limit`、`layer`，join 使用 `left_layer`/`right_layer`），每次试演最多 8 个探针。命中类探针给出 `before`、`after`、`added`、`removed` 与候选视图的 `visit`；`coverage` 给出 `covered`、`repeated`、`uncovered` 三个面积的 `before`/`after`/`delta`。探针本身无效时按其镜像读取端点的状态码拒绝（如点图层 coverage 返回 400，图层缺失返回 404）。

试演结果只在 `baseline_revision` 仍是当前全库修订时可引用。调用者在正式写入前应比对 `GET /status` 的 `revision`，或直接在 `POST /edits` 上携带 `if_revision=baseline_revision`：基准一旦变化，正式提交会以 `409 conflict` 失败，调用者即可判定旧预演失效并重新试演。对于需要交换矩形才能成立的写包，试演与正式提交共用同一末态暂存逻辑，因此两边的约束结论和投影结果一致。

边界、距离和镜像规则见 [PROJECT_SPEC.md](PROJECT_SPEC.md)。正式测试暂缓。`examples/walkthrough.py --help` 列出可离线执行的生产工作流演练。
