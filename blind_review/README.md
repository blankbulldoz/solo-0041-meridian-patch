# 成对地物盲审样本工程（blind_review）

为标注同事准备的成对**轴对齐整数矩形（extent）**盲审样本。三份产物严格分离：
盲审输入不含任何标签，答键单独存放，原始形状证据保留每一次公开关系查询的
HTTP 入参与原始响应。

## 三类目标关系（无向对层面）

| 类别码 | 含义 | 判定（来自服务 `/relation` 双向查询） |
| --- | --- | --- |
| `C1_TOUCH_DISJOINT` | 零距离不相交 | 双向 `intersects=false` 且 `distance_squared=0`（共享边/角，半开区间内部不重叠） |
| `C2_PARTIAL_OVERLAP` | 正面积相交但互不包含 | `intersects=true`，双向 `left_contains/right_contains` 均为 false |
| `C3_REVERSE_CONTAINMENT` | 反转左右才变成包含 | 正向 `right_contains=true, left_contains=false`；反转方向后变为 `left_contains=true` |

几何契约来自仓库 `PROJECT_SPEC.md`：矩形为半开 `[min_x,max_x) × [min_y,max_y)`，
相交要求**正面积**交集，包含是方向化的闭区间比较，距离按**闭包**欧氏距离平方计算。

## 目录结构

```
blind_review/
├── README.md
├── scripts/
│   ├── generate_samples.py     # 启动服务 → 盲抽 → 公开查询打标 → 补样 → 分组 → 出件
│   ├── verify_independently.py # 不连服务、不用第三方库，仅凭坐标独立复核
│   └── render_figures.py       # 纯标准库 SVG 图证（有标注版 + 无标注盲审版）
├── data/
│   ├── blind_input.json        # 盲审输入：只有坐标/行号/分组，无任何关系字段
│   ├── answer_key.json         # 答键：实际 /relation 返回值派生
│   └── sampling_summary.json   # 抽样与分组汇总
├── evidence/
│   ├── raw_relation_responses.json  # 180 条有序查询的原始 HTTP 响应
│   ├── stage_a_random_draws.json    # 阶段A 300 次盲抽全量记录（含本地预判 vs 服务器实际）
│   └── supplement_ledger.json       # 补样台账：缺口、构造、服务器复核、平衡核对
└── figures/
    ├── evidence/  # 90 张带关系标注 SVG
    └── blind/     # 90 张无标注 SVG（供盲审标注）
```

## 样本规模与分组（防跨组泄漏）

- 90 个**无向形状对**，三类各 30 对；每对生成 forward / reversed 两条有序行，共 180 行。
- 分组在**无向对层面**用固定随机种子（`SEED+1`）划分：每类 24 对训练组、6 对抽查组。
- 同一对的正反方向共享 `pair_id` 与分组，因此**正反方向不可能跨训练组/抽查组**；
  复核脚本会显式断言不存在同时出现在两个组的 `pair_id`。
- 训练组 144 行（72 对），抽查组 36 行（18 对）。

## 类别不均衡与补样依据（可核对）

阶段 A 在固定采样窗口 `[-180,180)²`（图层边界 `[-1000,1000)²`）内做 300 次盲抽，
全部由服务 `/relation` 实际打标。本轮盲抽的服务器实际类别分布为：

| 类别 | 盲抽命中 | 目标 | 缺口（定向补样） |
| --- | --- | --- | --- |
| `C2_PARTIAL_OVERLAP` | 55 | 30 | 0（超出部分在定额截断时丢弃） |
| `C1_TOUCH_DISJOINT` | 1 | 30 | 29 |
| `C3_REVERSE_CONTAINMENT` | 1 | 30 | 29 |
| `C0_OTHER`（正距离分离等，不入选） | 243 | — | — |

阶段 B 仅对缺口类别做确定性构造（边/角接、部分重叠、严格包含），每对仍通过
**同一个公开 `/relation` 查询**复核，服务器类别与目标不一致则拒绝入选并记录。
所有补样逐条写入 `evidence/supplement_ledger.json`，含缺口余额、本地预判类、
服务器实际类、原始双向响应；台账中的缺口数、接受数、最终定额均可由
`stage_a_random_draws.json` 重新推算，复核脚本会做算术对账。

## 独立复核（不依赖服务）

`verify_independently.py` 仅用 Python 标准库、以 `-I` 隔离模式运行，直接从
`blind_input.json` 的整数坐标重算半开相交、剪裁框、双向包含和闭包距离，然后：

1. 重算值与答键逐字段比对；
2. 答键与保存的原始 HTTP 响应逐字段比对（证明答键是查询结果的忠实副本）；
3. 双向重算必须满足交换对称，并由坐标重新推出无向类别，与声明类别比对；
4. 分组泄漏检查：每对两个方向同组，无 `pair_id` 跨训练/抽查组；
5. 补样台账与阶段 A 日志做缺口/接受/定额算术核对；
6. 盲审输入泄漏检查：不得出现任何关系标签字段。

当前结果：**3948 项检查全部 PASS**。

## 一键复跑

```sh
# 需要 Go 构建 meridian（基线仅用 Go 标准库）；脚本只依赖 Python 3 标准库
bash blind_review/scripts/run_pipeline.sh
```

所有随机性由固定种子 `20261122` 驱动，重复运行产物一致。
