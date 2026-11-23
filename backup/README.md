# MPB 定期备份、轮换与恢复演练

本目录把 Meridian Patch（MPB）纳入定期备份，并交付可直接运行的备份轮换和
恢复演练。所有脚本只依赖 Python 3 标准库；离线镜像校验复用 Go 侧的生产解码
路径（`cmd/mpbverify`），不重复实现几何规则。

## 组件

| 文件 | 职责 |
| --- | --- |
| `mpb_common.py` | HTTP 客户端、MPB v1 帧解析、Go 兼容的规范 JSON 校验、原子写、清单工具 |
| `mpb_backup.py` | 单次或守护态备份；落盘后离线校验；清单/轮换日志；GFS 轮换 |
| `mpb_workload.py` | 持续移动写负载，每个批次是一个原子写包并推进恰好一个修订，附操作日志 |
| `mpb_restore_drill.py` | 恢复演练：最新优先选镜像、损坏回退、离线启动、空间核验、损失分析、发布门槛 |
| `run_drill_demo.py` | 一条命令跑通“移动写入 + 多轮备份 + 轮换 + 健康/回退两次演练”的端到端演示 |
| `run-scheduled.sh` | cron / systemd timer 调用的定期入口 |
| 仓库内 `cmd/mpbverify` | 离线 MPB 校验器，复用生产的 `folio.Decode` 与 `fromImage` 全量校验 |

## 为什么备份保存的是一个提交

线上写入持续移动时，服务端在写锁内从一个已发布的不可变视图渲染镜像
（`Database.Export` → `state.image()`），HTTP 响应同时带 `Meridian-Revision`。
备份工具：

1. 一次 GET 取回完整镜像字节和提交修订号；
2. 校验 8 字节签名、长度、载荷 SHA-256、规范 JSON，并要求载荷内修订号等于
   响应头修订号；
3. 再用 `mpbverify` 跑与服务重启完全相同的结构、修订、几何规则和索引重建校验；
4. 全部通过后才把记录追加进 `manifest.jsonl`，记录包含提交修订、整文件
   SHA-256、载荷 SHA-256、字节数、图层摘要、时间戳和校验结果。

失败的镜像进入 `quarantine/` 且状态为 `quarantined`，永远不会成为恢复点。

## 备份轮换

`mpb_backup.py` 同时实施两层保留策略：

- **最近 N 份**（`--keep`，默认 3）始终保留；
- **GFS 时间槽**：`--hourly/--daily/--weekly/--monthly`，每个时间槽只保留一份
  最新备份，槽位按小时/日/ISO 周/月划分；设为 0 表示关闭该层。

每次删除都追加到 `rotation.jsonl`（含文件名、修订、SHA-256、字节数、原因），
`index.json` 是当前可用恢复点的目录，`manifest.jsonl` 是只追加的完整归档记录。
守护模式用 `--daemon --interval-seconds`，周期调度建议用 cron/systemd 调
`run-scheduled.sh`，由调度器拥有周期、备份工具保持单次可重跑。

## 恢复演练怎么选镜像、怎么判损失

1. **最新优先**：按 `(时间, 修订, 文件名)` 倒序逐个尝试清单中的有效备份。
2. **损坏回退**：先比对文件 SHA-256 与清单元数据，再做帧解析和 `mpbverify`
   全量校验；最新镜像损坏时记录失败原因并自动选择更早的可用镜像，报告里
   `selection.fallback_used` 标明是否发生回退。
3. **离线启动**：选中的镜像写入一个临时数据目录的 `meridian-state.mpb`，以
   系统分配端口启动独立 meridian 进程（不触碰线上数据目录、不执行 PUT
   /image），验证启动修订与清单修订一致。
4. **关键空间状态核验**（通过真实 HTTP API）：
   - 窗口面积：`coverage` 的 `covered / repeated / uncovered`；
   - 跨层配对：`join` 的配对数量、ID 对与剪裁交集矩形；
   - 另含 `locate` 与 `index-audit` 核验索引与目录一致。
5. **损失分析**：把选中镜像与线上当前提交镜像逐图层 diff，结合 `mpb_workload`
   的操作日志，输出失去的修订区间 `(恢复点修订, 线上修订]`、区间内每个已提交
   写包、各图层新增/删除/形变的地物 ID 以及图层边界变化。
6. **发布门槛**：离线镜像有效、离线启动修订匹配、全部空间核验通过、线上比对
   完成，四项全部为真才 `release_gate.open = true`，进程退出码为 0。

## 运行

### 端到端演示（推荐先跑，全部在临时目录）

```sh
python3 backup/run_drill_demo.py --artifacts ./backup-demo-artifacts
```

演示会构建两个二进制、启动带固定种子的服务、后台持续移动写入、连续备份并
触发轮换，然后先做一次健康演练（选中最新），再人为损坏最新备份做一次回退
演练。产物：

- `reports/healthy/drill-report.json`、`reports/fallback/drill-report.json`
- `reports/*/parameters.json`：完整可复跑参数（含 argv）
- `reports/*/recovered-server.log`：离线恢复进程的启动日志
- `*/archive/manifest.jsonl`、`rotation.jsonl`、`index.json`
- `demo-summary.json`：一次运行的结论汇总
- `*/workload.jsonl`：每个移动写包对应的修订与操作

### 针对真实服务的单次备份

```sh
python3 backup/mpb_backup.py --url http://127.0.0.1:8741 --archive /var/backups/mpb
```

### 恢复演练（可独立反复运行，临时目录由脚本自行管理）

```sh
python3 backup/mpb_restore_drill.py \
  --url http://127.0.0.1:8741 \
  --archive /var/backups/mpb \
  --binary ./meridian \
  --verifier ./mpbverify \
  --plan ./verify-plan.json \
  --op-log /var/log/mpb-workload.jsonl \
  --report-dir /var/log/mpb-drill/$(date -u +%Y%m%dT%H%M%SZ)
```

`--plan` 是核验计划 JSON，结构见 `run_drill_demo.py` 的 `build_plan()`：
每项是 `coverage` / `join` / `locate` / `audit` 之一并带期望值。线上不可达时
加 `--no-live` 做纯离线演练（跳过损失分析，其余门槛不变）。

### 离线手工校验

```sh
go build -o mpbverify ./cmd/mpbverify
./mpbverify /var/backups/mpb/mpb-*.mpb        # 每个文件一行 JSON，坏镜像退出码 1
```

## 恢复点与发布门槛怎么读

演练报告的 `recovery_point` 给出明确恢复点（归档文件名、提交修订、SHA-256、
时间、图层/地物数量）。门槛打开只代表该恢复点在隔离环境中可启动且空间状态
符合计划；真正回灌线上仍须按 API 约定带当前线上修订执行
`PUT /image`（`If-Meridian-Revision`），服务端会拒绝过期修订，避免覆盖演练之后
的新提交。门槛未打开时退出码为 1，报告保留全部失败证据，不得发布。
