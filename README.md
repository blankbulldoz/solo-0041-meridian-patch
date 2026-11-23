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

### 启动就绪门槛

每次启动先取得数据目录的唯一写锁，再按顺序通过四道门槛，全部通过后才绑定端口并记录 `meridian ready`：

1. `lock` — `meridian.writer` 上的 `flock` 独占锁。另一实例持有时以退出码 `10` 拒绝启动。
2. `image` — 磁盘镜像的签名、长度、SHA-256 与规范 JSON 校验，随后按几何规则重建内存索引。失败退出码 `11`。
3. `index` — 逐层把索引条目、叶容量与节点包围盒对照目录记录（`index-audit` 的启动版本）。失败退出码 `12`。
4. `probe` — 可选的实定位探针，例如 `-probe ochre-field,4,1,dowel|ferrule`，要求 `locate` 返回的 ID 集合与期望完全一致。失败退出码 `13`。

```sh
./meridian -listen 127.0.0.1:8741 -data ./data \
  -probe ochre-field,4,1,dowel|ferrule -require-probe \
  -readiness-report ./logs/readiness.json
```

`-require-probe` 让缺少 `-probe` 也按 `probe` 门槛失败。就绪日志行 `meridian readiness gates passed` 记录镜像摘要、逐层 `records/checked_nodes/revision` 和探针的期望、实际、缺失与多余 ID；`-readiness-report` 另行原子写出同一份证据 JSON。启动失败时写 `meridian not ready`，带 `stage` 字段，且不绑定端口、不写就绪报告。正常关闭仍走 SIGTERM 与优雅停机。

## 接管演练

`examples/takeover.py` 在同一台机器上用同一数据目录先跑旧实例、再跑新实例，全部子进程日志与事件日志落在 `--workdir`（默认临时目录）。默认用当前源码构建一个二进制扮演新旧两版；真实升级时分别传入二进制：

```sh
go build -o /tmp/meridian-old ./cmd/meridian   # 旧版本
go build -o /tmp/meridian-new ./cmd/meridian   # 新版本
python3 examples/takeover.py --old-binary /tmp/meridian-old --new-binary /tmp/meridian-new --keep
```

脚本按阶段执行并逐项断言：

| 阶段 | 断言 |
| --- | --- |
| 0 基线 | 旧实例写入图层与地物，导出镜像与磁盘字节一致，作为升级前快照 |
| 1 写者互斥 | 旧实例持续提交时三次启动新实例，均以 `lock`（退出码 10）被拒，未绑定端口；旧实例仍可读写，修订继续增长 |
| 2 门槛接管 | 停旧实例后新实例依次通过镜像、索引与探针门槛：镜像摘要等于升级前摘要、逐层核验记录数与层修订、探针结果一致；就绪日志先于地址日志；新写者就位后第二个竞争实例仍被锁拒绝 |
| 3 镜像损坏 | 副本翻转一个负载字节，以 `image`（退出码 11）拒绝，错误含 `checksum`；原镜像字节未被改写 |
| 4 内容漂移 | 把地物移出探针点后重新签名，镜像本身校验通过，只有探针以 `probe`（退出码 13）捕获，报出 `missing` |
| 5 探针不符 | 期望值漏掉一个真实命中，以 `probe` 拒绝并报出 `unexpected`；共享目录未被改动 |
| 6 回滚门槛 | 升级后产生新提交时，文件级换回被拒（会丢失已提交修订）；在活动写者上以 `If-Meridian-Revision` 做 CAS 镜像恢复成功并推进到新修订 |
| 7 最终重启 | 从共享目录重启并再次通过门槛，内容签名与 CAS 恢复后的镜像一致 |

### 区分三类故障

| 现象 | 门槛 | 退出码 | 日志特征 | 含义与处置 |
| --- | --- | --- | --- | --- |
| 锁忙 | `lock` | 10 | `acquire data directory: resource temporarily unavailable` | 另一进程仍是写者。不要删除 `meridian.writer`；先停旧进程并用 `GET /status` 确认 `writable` 后重试 |
| 镜像损坏 | `image` | 11 | `image checksum mismatch` / `invalid image size or version` / `noncanonical image payload` | 磁盘镜像不可用。不要原地修复：用备份镜像走 CAS 恢复，或从目录重建后重新探针 |
| 内容漂移 | `probe` | 13 | `expected [...] observed [...] (missing [...], unexpected [...])`，且镜像校验已通过 | 镜像与索引自洽但与升级前事实不符。按探针差异定位地物，确认后再决定接受或回滚 |
| 地物探针不符 | `probe` | 13 | 同上 | 探针期望值写错，或地物确实变了。用 `locate` 复核后修正期望或回滚 |
| 索引不一致 | `index` | 12 | `index audit for layer ...` | 索引条目、叶容量或包围盒与目录不符。不要带病启动，报告该层与修订 |

脚本结束时输出分类表、回滚门槛表、修订轨迹（升级前 → 接管 → 回滚），并写出 `summary.json` 与 `takeover-events.jsonl`。

### 回滚门槛

- **允许文件级换回**：新实例从未提交（修订仍等于升级前修订），且磁盘镜像摘要等于升级前摘要。此时换回并重启，重启必须重新通过全部启动门槛。
- **只允许 CAS 镜像恢复**：新实例已产生提交。用 `PUT /image` 携带 `If-Meridian-Revision: <当前修订>` 与升级前镜像；过期条件返回 `409 conflict` 并保留现库；成功会创建新修订，不丢弃历史。
- **禁止文件级换回**：`commit_uncertain`（改名后目录同步失败）或写者已围栏。此时只读重启，核对镜像、索引与探针后再决定，不能直接覆盖 `meridian-state.mpb`。


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
