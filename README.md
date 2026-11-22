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

例如，创建矩形图层并写入一个地物使用下面的 JSON 对象。

```json
{"name":"ochre-field","kind":"extent","bounds":{"min_x":-4,"min_y":-4,"max_x":12,"max_y":12},"exclusive":false}
```

```json
{"edits":[{"layer":"ochre-field","id":"prong","mode":"insert","value":{"id":"prong","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":4,"max_y":3}}}}]}
```

`409 conflict` 表示修订条件失效、重名或末态违反排他策略。`400 invalid` 表示请求语义无效，`404 missing` 表示找不到图层或地物，`422 capacity` 表示数据库或查询工作量上限。正常窗口读带 `revision` 和 `layer_revision`。`visit` 报告索引访问开销，候选数可以超过真实命中数。

边界、距离和镜像规则见 [PROJECT_SPEC.md](PROJECT_SPEC.md)。正式测试暂缓。`examples/walkthrough.py --help` 列出可离线执行的生产工作流演练。

## 归档

`mpbarchive` 把 MPB v1 镜像转成可发给归档方的普通数据文件档案,不需要数据库进程即可核对。

```sh
go build -o mpbarchive ./cmd/mpbarchive
./mpbarchive unpack meridian-state.mpb archive/   # 解码、校验并写出档案
./mpbarchive verify archive/                      # 复核档案内部一致性
./mpbarchive compare archive-a archive-b          # 分类两份档案的差异
```

每份档案包含 `layers.csv`(图层表)、`features.csv`(地物表)、`classes.csv`(按形状类别的核对摘要)和 `manifest.json`(绑定各文件摘要)。每行带有基于内容的 `row_digest`,行的身份与修订无关,跨档案可核对。修订和面积、到原点距离平方等坐标派生量以十进制字符串输出,超出 JavaScript 安全整数范围(2⁵³−1)的值不会丢失精度。类别摘要的 `total_area` 使用任意精度十进制。

`compare` 接受档案目录或 MPB 文件,给出四种结论:`identical`(完全相同)、`revision-only`(只换过提交号)、`relayout`(重新布局:图层边界或排他策略变化而地物行不变)、`content-changed`(内容改变),并列出布局变化和逐行的地物增删改。读取档案时会重算所有行摘要与类别摘要,篡改过的行会被拒绝。

`python3 examples/archive_demo.py` 用两份真实导出(其中一份经恢复仅提交号前移)、一次真实布局调整和一份合法但地物内容变动的镜像端到端证明上述核对。
