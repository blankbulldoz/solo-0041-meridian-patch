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
| `POST /strata` | `name`, `kind`, `bounds`, `exclusive`, 可选 `cuts` 与 `if_revision` |
| `PUT /strata/{layer}/layout` | 新 `bounds`、`exclusive`、可选 `cuts` 及可选 `if_revision`。形状类型保持原样 |
| `POST /edits` | `edits` 与可选 `if_revision`。每项有 `layer`, `id`, `mode`，写入时提供同 ID 的 `value` |
| `POST /strata/{layer}/window` | `bounds`, `limit`, 可选 `class`, `bookmark`, `index`。按 ID 返回 `features`，有更多数据时返回 `next` |
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

边界、距离和镜像规则见 [PROJECT_SPEC.md](PROJECT_SPEC.md)。正式测试暂缓。`examples/walkthrough.py --help` 列出可离线执行的生产工作流演练。

## 热点切线索引

图层可以在布局中声明自定义切线 `cuts: {"x": [...], "y": [...]}`（每轴至多 16 条，严格递增，落在图层边界内）。切线把图层划分成片区网格，内存中为每个片区和一棵"溢出"树各建一个局部索引：包络被单个片区完整包含的地物归入该片区，跨越切线的地物归入溢出树。每个地物恰好属于一棵子树，因此跨片区合并结果不会重复返回同一 ID；片区加溢出覆盖全层，因此局部窗口查询不会漏掉命中。

窗口查询携带 `"index": "local"` 时走热点网格：只访问与窗口（闭区间意义下）相接的片区子树和溢出树，响应中 `index` 报告实际路径、`cells` 报告触及的片区数，`visit` 仍为索引访问开销。省略 `index` 或传 `"global"` 时走全层树。两条路径返回的地物集合与排序完全一致，书签可以混用。没有配置切线的图层拒绝 `local` 模式。

切线随图层定义写入镜像，布局切换与普通变更一样在一次提交内原子发布，一次读取捕获的视图始终包含同一代数的数据与索引；进程重启后从镜像中的切线和实际地物重建网格。`GET /strata/{layer}/index` 的 `grid` 字段描述网格结构，`GET /strata/{layer}/index-audit` 同时核验每个地物是否落在其归属子树。`examples/hotspot_bench.py` 对比两种数据形态下两条路径的访问量。
