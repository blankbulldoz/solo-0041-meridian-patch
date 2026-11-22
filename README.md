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
| `POST /imports` | `transform` 与 `items`，可选 `if_revision`。一个变换整批作用于多个图层，原子提交 |
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

## 网格导入变换

`POST /imports` 用于把互成四分之一转角的两张网格对齐到目标平面。请求对象含一个变换和一批地物，变换对整批统一生效，多个图层在同一修订原子提交，任一项目失败则整包拒绝、修订不前进。

```json
{
  "transform": {"swap": true, "sign_x": -1, "sign_y": 1, "translate_x": 0, "translate_y": 0},
  "items": [
    {"layer": "quartz-pins", "value": {"id": "brad", "class": "fixture", "shape": {"mark": {"x": 2, "y": 5}}}},
    {"layer": "quartz-fields", "value": {"id": "prong", "class": "fixture", "shape": {"extent": {"min_x": 1, "min_y": 2, "max_x": 4, "max_y": 5}}}}
  ]
}
```

变换是整数等距映射，求值次序固定为先线性后平移：`x' = sign_x·(swap ? y : x) + translate_x`，`y' = sign_y·(swap ? x : y) + translate_y`。`sign_x`、`sign_y` 只能取 `1` 或 `-1`；`swap` 交换两轴，覆盖 90°/270° 转角；两轴同时反转并平移即点反射（绕中心 `c` 为 `p -> 2c - p`，可用 `plane.PointReflection` 构造）。八个带符号轴排列即方形二面体群的全部元素。

半开矩形规则按轴书面定义。保号轴上区间仅平移 `[lo,hi) -> [t+lo, t+hi)`；反号轴上开边从高端换到低端，为保持左下包含、右上排除且不丢失任何整数格点归属，区间向内收一个单位重新闭合：`[lo,hi) -> [t-hi+1, t-lo+1)`。因此面积、宽高（交换轴时互换）和相邻矩形的接边关系完全保持，被拥有格点经变换后仍被且仅被目标矩形拥有。点按同一公式逐坐标映射。

变换后的每个坐标都要落在系统范围 `[-10^9, 10^9]` 内；越界点或矩形返回 `400 invalid`，平移量本身也受同一范围约束，中间整数运算带溢出检查。随后还会按目标图层边界和排他策略再次校验。`examples/walkthrough.py --workflow imports` 保存了一组可核对的边界点示例（含开边排除与点反射中心）。

边界、距离和镜像规则见 [PROJECT_SPEC.md](PROJECT_SPEC.md)。正式测试暂缓。`examples/walkthrough.py --help` 列出可离线执行的生产工作流演练。
