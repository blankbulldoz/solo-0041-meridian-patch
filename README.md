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
| `POST /transplant/preview` | `source`（base64 的 MPB v1 镜像）、`source_layer`、`target`、`bounds`、`offset`、`mode`，可选 `if_revision`。返回裁切平移后的地物、被排除计数与冲突清单 |
| `POST /transplant/commit` | `plan`（预览签发的令牌）与可选 `if_revision`。整体应用或整体拒绝 |

例如，创建矩形图层并写入一个地物使用下面的 JSON 对象。

```json
{"name":"ochre-field","kind":"extent","bounds":{"min_x":-4,"min_y":-4,"max_x":12,"max_y":12},"exclusive":false}
```

```json
{"edits":[{"layer":"ochre-field","id":"prong","mode":"insert","value":{"id":"prong","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}}}]}
```

### 区域移植

`POST /transplant/preview` 从一份备份镜像中取一个区域，平移后写入目标库的现有图层。预览不改库：

- 源图层与目标图层必须同名几何类型。点按半开区间归属 `bounds`；矩形先与 `bounds` 求正面积交集，再按 `offset` 的整数偏移平移。整块移出窗口但与之正面积相交的矩形仍会被裁切后带入。
- 目标层可以已有地物。预览检查平移后的边界、`mode` 对应语义（`insert` 拒绝已存在 ID，`replace` 要求 ID 都存在）以及目标层最终排他策略。目标层是排他层时，只要末态无正面积重叠或同坐标点即可，因此允许两个 ID 互换矩形。
- 所有问题以 `conflicts` 的稳定 `kind` 数组返回（`empty`、`kind`、`geometry`、`bounds`、`duplicate`、`missing`、`exclusive`、`capacity`）。有任一项时 `plan` 不出现。
- 无冲突时返回签名的 `plan`。`plan` 绑定目标图层修订而非全库修订，所以预览与执行之间别的图层提交不会使其失效；目标层自身变化则使 `plan` 失效。令牌由进程内密钥签名，重启后需重新预览。
- `POST /transplant/commit` 重新核验目标层修订与末态规则。目标层在预览后变化时返回 `409 conflict`，整批不落地，只保留其他写入；篡改的 `plan` 返回 `400 invalid`。成功的提交把导入地物并入目标层并生成新的全库修订。

```json
{"source":"<base64 MPB v1>","source_layer":"quarry-field","target":"mosaic-field",
 "bounds":{"min_x":0,"min_y":0,"max_x":8,"max_y":4},"offset":{"x":100,"y":50},"mode":"insert"}
```

```json
{"plan":"<preview 返回的 plan>"}
```

`409 conflict` 表示修订条件失效、重名、末态违反排他策略或移植预览后目标层发生变化。`400 invalid` 表示请求语义无效，`404 missing` 表示找不到图层或地物，`422 capacity` 表示数据库或查询工作量上限。正常窗口读带 `revision` 和 `layer_revision`。`visit` 报告索引访问开销，候选数可以超过真实命中数。

边界、距离和镜像规则见 [PROJECT_SPEC.md](PROJECT_SPEC.md)。正式测试暂缓。`examples/walkthrough.py --help` 列出可离线执行的生产工作流演练。
