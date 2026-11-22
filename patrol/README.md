# 空间巡查样本包（Meridian Patch Patrol Pack）

本目录提供一套**可重跑**的空间巡查抽样工具与样本包。给定现有图层
（`seed-scenario.json`）、候选探针文件（`candidate-probes.json`）和抽取总数，
工具通过真实 HTTP API 对运行中的 Meridian Patch 服务做 `locate` 与 `nearest`
查询，按确定性规则选出样本，并把原始探针、实际定位/最近邻证据和缺额全部落盘，
供另一个人在同一库（或同修订重建库）上独立复查。

## 覆盖的三类巡查位置

探针按服务返回的半开归属（左下包含、右上排除）与闭包最近邻距离分类：

| 分组 | 判定规则 | 场景含义 |
| --- | --- | --- |
| `edge_zero_unowned` | `locate` 无归属，且最近距离平方为 0 | 点落在开边上：闭包距离为零但没有任何矩形拥有它 |
| `tie` | 最小距离被 ≥2 个地物共享 | 最近邻同距，验证距离相同按 ID 排序 |
| `unique` | 最小距离恰好对应 1 个地物 | 只有一个最近邻 |

一个探针可同时带多个标签（例如开边上的同距点同时属于前两组）；
`primary_group` 固定按 `edge_zero_unowned > tie > unique` 归组，保证可重复。

## 文件

- `seed-scenario.json` — 确定性图层场景：1 个非排他矩形图层、8 个地物、
  5 个类别（`beacon`/`echo`/`fixture`/`gasket`/`sleeve`），几何上刻意制造
  开边零距离、同距角点和唯一最近邻。
- `candidate-probes.json` — 26 条候选探针，含重复坐标（p15=p14、p26=p25）、
  成簇热点（p07/p16/p17/p18 同坐标）和 1 条越界无效探针（p19）。
- `patrol_pack.py` — 全部逻辑，仅依赖 Python 3 标准库，始终以 `python3 -I` 运行。
- `rebuild-pack.sh` — 构建服务、起临时库、播种、生成并自检样本包。
- `recheck.sh` — 复查者入口：对运行中的服务重放样本包校验。
- `sample-pack/` — 用当前规则生成的规范样本包（抽取总数 9）。

## 抽样规则（可重复）

1. 候选先做本地校验：非整数或超出 `[-10^9, 10^9]` 的探针记入
   `shortfalls.json` 的 `invalid_candidates`，不发给服务。
2. 按坐标去重：重复坐标只保留 `probe_id` 最小者，其余记入
   `duplicate_coordinates`；去重后的每个坐标各发一次 `locate` 和
   `nearest`（limit=128）。
3. 抽取总数按三组均分（余数依次分给前三组）。
4. **组内优先覆盖不同地物类别**：每轮选择能带来最多“新类别”的探针，
   类别增量相同时按 `probe_id` 字典序取最小；类别只统计零距离/最小距离
   证据（归属者与并列最近邻），远端邻居不参与，避免类别信号被稀释。
5. 某组候选不足时，缺额记入 `group_shortfalls`，再从全局剩余探针按
   `probe_id` 顺序补足；全部唯一坐标用尽后仍不足的部分进 `overall_shortfall`。
6. 所有排序键只含探针 ID、坐标和服务返回值；同修订重跑输出字节一致。

## 使用方式

### 一条命令端到端（临时库，不碰任何运行中的服务）

```sh
GO_BIN=/path/to/go ./rebuild-pack.sh            # 默认 TOTAL=9，输出到 ./sample-pack
TOTAL=25 OUT=/tmp/pack25 ./rebuild-pack.sh      # 自定义总数与输出目录
```

### 针对已运行的服务

```sh
# 1. 播种场景（仅空库需要；已有图层时直接从第 2 步开始）
python3 -I patrol_pack.py seed --base-url http://127.0.0.1:8741 \
        --scenario seed-scenario.json
# 2. 生成样本包
python3 -I patrol_pack.py generate --base-url http://127.0.0.1:8741 \
        --candidates candidate-probes.json --total 9 --out sample-pack
# 3. 复查（任何同修订的库均可，包括独立重建的库）
./recheck.sh http://127.0.0.1:8741 sample-pack
```

`demo` 子命令会自行 `go build`、起临时进程、播种、生成并立即复查，
适合离线演练；服务不在 PATH 时用 `GO_BIN` 指定工具链。

## 输出物（`sample-pack/`）

| 文件 | 内容 |
| --- | --- |
| `raw-probes.json` | 原始探针全量（含无效项与拒绝原因）、候选文件 SHA-256 |
| `evidence.json` | 每个去重坐标的实际定位、完整最近邻列表（含距离平方与访问开销）、分组标签、并列最近邻 ID、类别签名、修订号 |
| `selection.json` | 各组配额、池大小、命中探针、覆盖类别与最终选择顺序 |
| `shortfalls.json` | 无效候选、重复坐标、无界探针、各组缺额与总缺额 |
| `manifest.json` | 规则版本、库/图层修订、图层摘要、计数与上述四文件的 SHA-256 |
| `verify-verdict.json` | 仅复查时生成：逐项核对结果与修订漂移，失败时进程退出码为 1 |

## 复查语义

`verify` 会：(a) 重算四个输出文件的 SHA-256 并与 manifest 比对；
(b) 对样本中每个坐标重新发起 `locate`/`nearest`，逐字段比对定位、
最近邻、标签和类别签名；(c) 检查全库修订是否漂移；(d) 用同一确定性
规则重放抽样，确认选择结果可复现。任一项不一致都会在 `mismatches`
中列出具体探针与字段，并以退出码 1 结束——复查者据此定位需要修订的库状态。
