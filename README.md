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
| `PUT /image?mode=preflight` | 与上相同的请求体、媒体类型和 `If-Meridian-Revision`（预检时可省略），只读地返回镜像能否读取、目标与下一修订、新增/完全替换/消失/未变的图层名、逐层与全库的地物变化数以及镜像 SHA-256。不写磁盘、不推进修订 |

例如，创建矩形图层并写入一个地物使用下面的 JSON 对象。

```json
{"name":"ochre-field","kind":"extent","bounds":{"min_x":-4,"min_y":-4,"max_x":12,"max_y":12},"exclusive":false}
```

```json
{"edits":[{"layer":"ochre-field","id":"prong","mode":"insert","value":{"id":"prong","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}}}]}
```

`409 conflict` 表示修订条件失效、重名或末态违反排他策略。`400 invalid` 表示请求语义无效，`404 missing` 表示找不到图层或地物，`422 capacity` 表示数据库或查询工作量上限。正常窗口读带 `revision` 和 `layer_revision`。`visit` 报告索引访问开销，候选数可以超过真实命中数。

预检响应中的 `target_revision` 是该报告对照的目标修订，`next_revision` 是成功恢复将得到的修订。报告可读性由 `readable` 表示；不可读镜像以 `readable: false` 加 `problem` 返回而不是 500。`layers.replaced` 是两侧都存在但定义或地物不同的图层名，恢复会整体替换这些层；`details` 逐层给出 `definition_changed` 与 `features` 的新增、删除、修改计数，`features.changed` 是三者之和。目标修订在预检后被任何写入推进时，以旧 `target_revision` 发出恢复会得到 `409 conflict`。

边界、距离和镜像规则见 [PROJECT_SPEC.md](PROJECT_SPEC.md)。正式测试暂缓。`examples/walkthrough.py --help` 列出可离线执行的生产工作流演练。
