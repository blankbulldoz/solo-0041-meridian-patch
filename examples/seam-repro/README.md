# 接边查询两种现场（临时库，可复跑）

针对同一个 meridian 服务保留的两个可复跑现场，数据已提交到 `./data`
（`meridian-state.mpb`，数据库修订 4）。复跑：

```sh
python3 -I replay.py        # 幂等：图层已存在则跳过种子化，只重放探针
```

`replay.py` 启动 `./meridian`（监听 `127.0.0.1:0`，数据目录 `./data`），
输出 JSON 报告后正常停机释放写锁。两次运行的探针结果应完全一致
（参照 `run-1.json` / `run-2.json`）。

## 现场 A：`sparse-verge`（稀疏层，2 个地物）—— 半开语义

`west`=[0,0,4,8) 与 `east`=[4,0,9,8) 共享垂直边 x=4。同一 8×8 视口平移：

| 窗口 | x=4 的位置 | seams 结果 | visit.candidates |
| --- | --- | --- | --- |
| [4,0,12,8) | 左边 | 1 条（fixed=4, [0,8)） | 6 |
| [-4,0,4,8) | 右边 | **空** | 6（相同） |
| [0,0,12,8) | 内部 | 1 条 | 6 |

两个窗口的候选数完全相同（两个矩形都被闭包络搜索访问到），区别只在
`plane.SharedBoundary` 的固定坐标判定 `window.MinX <= x < window.MaxX`
（internal/plane/seam.go:17）：左闭右开。结论：**半开语义**，规格第 10 条
"固定坐标在窗口右上边时排除，在左下边时计入"。不是限额、不是剪枝。

## 现场 B：`dense-thicket`（密集层，480 个地物）—— 候选耗尽

460 个互相包络相交的 `pad-*`（不接边）+ 10 对 `seam-w-k`/`seam-e-k`。
窗口 [800,0,840,24)：

- `POST /strata/dense-thicket/seams`：limit=1 与 limit=2000（上限）都返回
  **422 capacity "seam query exceeds seam or candidate budget"**；
  limit=2001 返回 400（limit 上限本身是 2000）。调大 limit 无效。
- `POST /join`（同一窗口，dense-thicket 与 probe-slab 两个方向）：**200**，
  各 20 对，候选 21 / 500，远低于预算。
- 独立证据（不经过 seams 代码路径：window 端点分页拉取全部 480 个地物，
  在 replay.py 内重算几何）：
  - 真实接边 **28 条 ≤ 2000** 结果上限 → 结果限额分支不可能触发；
  - 候选工作量 = 窗口候选 480 + 包络相交对 211,712 = **212,192 >
    200,000** 候选预算（internal/spatial/join.go:15，seams.go:36 复用）。

结论：**候选耗尽**。422 的消息文本对"结果限额"和"候选预算"两个分支完全
相同，只能靠独立计数区分。join 走另一层对的包络搜索，候选量小，故有响应。
