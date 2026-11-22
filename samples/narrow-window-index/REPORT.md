# 狭窗索引候选样本：交付说明

两份样本用同一套输入参数、同一批请求生成，几何上只有 180 个零面积接边矩形（fringe）的位置不同。样本 A 把这些 fringe 贴在每个查询窗的边上，样本 B 把它们停到远离所有窗的区块。因此狭窗的真实命中完全相同，包围候选数从 33 降到 3。

## 交付物

| 文件 | 用途 |
| --- | --- |
| `payloads/sample-a.json` / `payloads/sample-b.json` | `POST /strata` + `POST /edits` 的完整输入（各 400 次 insert，单批） |
| `images/sample-a.mpb` / `images/sample-b.mpb` | MPB v1 镜像，可直接 `PUT /image` 导入 |
| `windows.json` | 六个查询窗，两份样本共用同一文件 |
| `params.json` | 图层定义、数量、类别比例、角色构成 |
| `results/sample-a.json` / `results/sample-b.json` | 导入后的索引统计、每窗 visit、命中清单、请求哈希 |
| `build_samples.py` | 生成 payload 与归一化镜像 |
| `run_index.py` | 导入镜像并采集索引统计与每窗结果 |
| `verify_geometry.py` | 不经过服务器，用纯区间运算独立重算候选/命中 |
| `verify_images.py` | 直接解码两份 MPB 镜像并逐条对比目录内容 |
| `verify_payload_import.py` | 仅凭 payload JSON 重建镜像并逐字节比对 |

## 输入参数

```json
{
  "bounds": {
    "max_x": 80,
    "max_y": 80,
    "min_x": -80,
    "min_y": -80
  },
  "exclusive": false,
  "kind": "extent",
  "name": "candidate-field"
}
```

- 地物总数：400，两份样本相同。
- 类别比例：core 200 / halo 120 / edge 80，两份样本的每一条地物 ID→类别映射完全相同。
- 角色构成：anchor 4、hit 18、fringe 180、background 198。
- 查询窗口（`windows.json`，两份样本共用）：

| 窗 | 请求体 | 请求 SHA-256 |
| --- | --- | --- |
| w1 | `{"bounds":{"max_x":-10,"max_y":-34,"min_x":-60,"min_y":-36},"limit":256}` | `eddfe264454cb6b1…` |
| w2 | `{"bounds":{"max_x":-10,"max_y":-6,"min_x":-60,"min_y":-8},"limit":256}` | `348022abd10fc360…` |
| w3 | `{"bounds":{"max_x":-10,"max_y":26,"min_x":-60,"min_y":24},"limit":256}` | `285aaa774fca5082…` |
| w4 | `{"bounds":{"max_x":12,"max_y":60,"min_x":10,"min_y":-60},"limit":256}` | `e95c7cfd7a7d415a…` |
| w5 | `{"bounds":{"max_x":42,"max_y":60,"min_x":40,"min_y":-60},"limit":256}` | `93f10d3ab957ac7f…` |
| w6 | `{"bounds":{"max_x":2,"max_y":2,"min_x":-2,"min_y":-2},"limit":256}` | `80ec4676e29cb0eb…` |

窗均为细长或小方窗；每个窗恰好 3 条正面积真实命中，命中几何在两份样本中完全相同。

## 导入后的索引统计

| 指标 | 样本 A | 样本 B |
| --- | --- | --- |
| 图层条目数 count | 400 | 400 |
| 图层 revision | 1 | 1 |
| 库 revision | 1 | 1 |
| 树 nodes | 95 | 95 |
| 树 leaves | 48 | 48 |
| 树 depth | 7 | 7 |
| 树 items | 400 | 400 |
| 树 max_leaf | 12 | 12 |
| root_bounds | {"max_x": 80, "max_y": 80, "min_x": -80, "min_y": -80} | {"max_x": 80, "max_y": 80, "min_x": -80, "min_y": -80} |
| index-audit records | 400 | 400 |
| index-audit checked_nodes | 95 | 95 |
| 导入后导出与镜像逐字节一致 | True | True |
| 镜像 SHA-256 | `85226c0e6d014a65…` | `1704baa73ac2495c…` |
| 六窗候选总数 | 198 | 18 |
| 六窗真实命中总数 | 18 | 18 |
| 六窗访问节点总数 | 132 | 126 |

两份样本的图层边界（±80）、条目数（400）、类别比例、树形计数与 root_bounds 全部一致；差异只在狭窗所触及的节点候选上。

## 每条窗的候选与真实命中

### 样本 A

| 窗 | visit.candidates | visit.nodes | 真实命中 | 返回 feature IDs |
| --- | --- | --- | --- | --- |
| w1 | 33 | 23 | 3 | `hit-w1-0`, `hit-w1-1`, `hit-w1-2` |
| w2 | 33 | 25 | 3 | `hit-w2-0`, `hit-w2-1`, `hit-w2-2` |
| w3 | 33 | 17 | 3 | `hit-w3-0`, `hit-w3-1`, `hit-w3-2` |
| w4 | 33 | 25 | 3 | `hit-w4-0`, `hit-w4-1`, `hit-w4-2` |
| w5 | 33 | 21 | 3 | `hit-w5-0`, `hit-w5-1`, `hit-w5-2` |
| w6 | 33 | 21 | 3 | `hit-w6-0`, `hit-w6-1`, `hit-w6-2` |

候选 ID 清单（离线几何按同一 `MayMeet` 闭包重算，与 visit.candidates 逐窗相等）：

- **w1**（33）：`fringe-w1-00`, `fringe-w1-01`, `fringe-w1-02`, `fringe-w1-03`, `fringe-w1-04`, `fringe-w1-05`, `fringe-w1-06`, `fringe-w1-07`, `fringe-w1-08`, `fringe-w1-09`, `fringe-w1-10`, `fringe-w1-11`, `fringe-w1-12`, `fringe-w1-13`, `fringe-w1-14`, `fringe-w1-15`, `fringe-w1-16`, `fringe-w1-17`, `fringe-w1-18`, `fringe-w1-19`, `fringe-w1-20`, `fringe-w1-21`, `fringe-w1-22`, `fringe-w1-23`, `fringe-w1-24`, `fringe-w1-25`, `fringe-w1-26`, `fringe-w1-27`, `fringe-w1-28`, `fringe-w1-29`, `hit-w1-0`, `hit-w1-1`, `hit-w1-2`
- **w2**（33）：`fringe-w2-00`, `fringe-w2-01`, `fringe-w2-02`, `fringe-w2-03`, `fringe-w2-04`, `fringe-w2-05`, `fringe-w2-06`, `fringe-w2-07`, `fringe-w2-08`, `fringe-w2-09`, `fringe-w2-10`, `fringe-w2-11`, `fringe-w2-12`, `fringe-w2-13`, `fringe-w2-14`, `fringe-w2-15`, `fringe-w2-16`, `fringe-w2-17`, `fringe-w2-18`, `fringe-w2-19`, `fringe-w2-20`, `fringe-w2-21`, `fringe-w2-22`, `fringe-w2-23`, `fringe-w2-24`, `fringe-w2-25`, `fringe-w2-26`, `fringe-w2-27`, `fringe-w2-28`, `fringe-w2-29`, `hit-w2-0`, `hit-w2-1`, `hit-w2-2`
- **w3**（33）：`fringe-w3-00`, `fringe-w3-01`, `fringe-w3-02`, `fringe-w3-03`, `fringe-w3-04`, `fringe-w3-05`, `fringe-w3-06`, `fringe-w3-07`, `fringe-w3-08`, `fringe-w3-09`, `fringe-w3-10`, `fringe-w3-11`, `fringe-w3-12`, `fringe-w3-13`, `fringe-w3-14`, `fringe-w3-15`, `fringe-w3-16`, `fringe-w3-17`, `fringe-w3-18`, `fringe-w3-19`, `fringe-w3-20`, `fringe-w3-21`, `fringe-w3-22`, `fringe-w3-23`, `fringe-w3-24`, `fringe-w3-25`, `fringe-w3-26`, `fringe-w3-27`, `fringe-w3-28`, `fringe-w3-29`, `hit-w3-0`, `hit-w3-1`, `hit-w3-2`
- **w4**（33）：`fringe-w4-00`, `fringe-w4-01`, `fringe-w4-02`, `fringe-w4-03`, `fringe-w4-04`, `fringe-w4-05`, `fringe-w4-06`, `fringe-w4-07`, `fringe-w4-08`, `fringe-w4-09`, `fringe-w4-10`, `fringe-w4-11`, `fringe-w4-12`, `fringe-w4-13`, `fringe-w4-14`, `fringe-w4-15`, `fringe-w4-16`, `fringe-w4-17`, `fringe-w4-18`, `fringe-w4-19`, `fringe-w4-20`, `fringe-w4-21`, `fringe-w4-22`, `fringe-w4-23`, `fringe-w4-24`, `fringe-w4-25`, `fringe-w4-26`, `fringe-w4-27`, `fringe-w4-28`, `fringe-w4-29`, `hit-w4-0`, `hit-w4-1`, `hit-w4-2`
- **w5**（33）：`fringe-w5-00`, `fringe-w5-01`, `fringe-w5-02`, `fringe-w5-03`, `fringe-w5-04`, `fringe-w5-05`, `fringe-w5-06`, `fringe-w5-07`, `fringe-w5-08`, `fringe-w5-09`, `fringe-w5-10`, `fringe-w5-11`, `fringe-w5-12`, `fringe-w5-13`, `fringe-w5-14`, `fringe-w5-15`, `fringe-w5-16`, `fringe-w5-17`, `fringe-w5-18`, `fringe-w5-19`, `fringe-w5-20`, `fringe-w5-21`, `fringe-w5-22`, `fringe-w5-23`, `fringe-w5-24`, `fringe-w5-25`, `fringe-w5-26`, `fringe-w5-27`, `fringe-w5-28`, `fringe-w5-29`, `hit-w5-0`, `hit-w5-1`, `hit-w5-2`
- **w6**（33）：`fringe-w6-00`, `fringe-w6-01`, `fringe-w6-02`, `fringe-w6-03`, `fringe-w6-04`, `fringe-w6-05`, `fringe-w6-06`, `fringe-w6-07`, `fringe-w6-08`, `fringe-w6-09`, `fringe-w6-10`, `fringe-w6-11`, `fringe-w6-12`, `fringe-w6-13`, `fringe-w6-14`, `fringe-w6-15`, `fringe-w6-16`, `fringe-w6-17`, `fringe-w6-18`, `fringe-w6-19`, `fringe-w6-20`, `fringe-w6-21`, `fringe-w6-22`, `fringe-w6-23`, `fringe-w6-24`, `fringe-w6-25`, `fringe-w6-26`, `fringe-w6-27`, `fringe-w6-28`, `fringe-w6-29`, `hit-w6-0`, `hit-w6-1`, `hit-w6-2`

补充探针 `class=halo`（同 bounds、同 limit）：候选 w1=33, w2=33, w3=33, w4=33, w5=33, w6=33，命中均为 0。`visit.candidates` 在类别判定之前按包围盒计数，类别过滤不会压低候选数；结合上方候选 ID 清单可见，每个窗多出的 30 条全部是 `fringe-*`，真实命中始终只有 3 条。

### 样本 B

| 窗 | visit.candidates | visit.nodes | 真实命中 | 返回 feature IDs |
| --- | --- | --- | --- | --- |
| w1 | 3 | 23 | 3 | `hit-w1-0`, `hit-w1-1`, `hit-w1-2` |
| w2 | 3 | 23 | 3 | `hit-w2-0`, `hit-w2-1`, `hit-w2-2` |
| w3 | 3 | 23 | 3 | `hit-w3-0`, `hit-w3-1`, `hit-w3-2` |
| w4 | 3 | 19 | 3 | `hit-w4-0`, `hit-w4-1`, `hit-w4-2` |
| w5 | 3 | 19 | 3 | `hit-w5-0`, `hit-w5-1`, `hit-w5-2` |
| w6 | 3 | 19 | 3 | `hit-w6-0`, `hit-w6-1`, `hit-w6-2` |

候选 ID 清单（离线几何按同一 `MayMeet` 闭包重算，与 visit.candidates 逐窗相等）：

- **w1**（3）：`hit-w1-0`, `hit-w1-1`, `hit-w1-2`
- **w2**（3）：`hit-w2-0`, `hit-w2-1`, `hit-w2-2`
- **w3**（3）：`hit-w3-0`, `hit-w3-1`, `hit-w3-2`
- **w4**（3）：`hit-w4-0`, `hit-w4-1`, `hit-w4-2`
- **w5**（3）：`hit-w5-0`, `hit-w5-1`, `hit-w5-2`
- **w6**（3）：`hit-w6-0`, `hit-w6-1`, `hit-w6-2`

补充探针 `class=halo`（同 bounds、同 limit）：候选 w1=3, w2=3, w3=3, w4=3, w5=3, w6=3，命中均为 0。`visit.candidates` 在类别判定之前按包围盒计数，所以类别过滤不会把候选降到 0；B 每个窗的候选数已经等于真实命中数，没有额外的包围候选可被过滤。

## 如何验证两份差异不是靠改请求获得

1. **请求逐字节相同。** `results/sample-X.json` 每窗记录 `request_sha256`；上表两列的六窗哈希完全相等，`class_probes` 的哈希也相等。请求体只含 `bounds` 与 `limit`。
2. **窗口定义共用一份文件。** `windows.json` 被两份样本的构建与运行脚本共同读取，`build_samples.py` 中不存在按样本改写窗口的分支。
3. **数量与类别比例独立重算。** `verify_geometry.py --sample a|b` 不连接服务器，仅凭 payload 用半开区间运算核对 400 条、core/halo/edge = 200/120/80，并断言两份样本的非 fringe 记录逐字段相同、每窗命中 ID 列表相同。
4. **差异只允许出现在 fringe 位置。** 同一脚本断言恰好 180 条 `fringe-*` 记录在 A/B 间坐标不同，其余 220 条完全相同。
5. **payload 足以重建镜像。** `verify_payload_import.py --sample a|b` 直接从 JSON 创建图层并单批写入 400 条，导出、归一化后与 `images/sample-X.mpb` 逐字节相等（SHA-256 见上表）。
6. **镜像内容逐条对比。** `verify_images.py` 直接解码两份 MPB（校验签名、长度和 SHA-256），确认图层定义、修订、400 个 ID 与 ID→类别映射完全相同，并断言恰好 180 条 `fringe-*` 几何不同、其余 220 条逐字节相同（输出见 `results/image-comparison.json`）。
7. **运行可重复。** 对同一二进制重复执行 `run_index.py`，两次 `results/` 目录逐文件相同。
8. **索引自洽。** 两份样本的 `index-audit` 均报告 400 条、95 个节点；导入后立即导出与镜像逐字节一致，说明运行观察来自磁盘上的同一份数据。

## 复现步骤

```sh
go build -o /tmp/meridian ./cmd/meridian
python3 -I samples/narrow-window-index/build_samples.py --binary /tmp/meridian
python3 -I samples/narrow-window-index/verify_geometry.py --sample a
python3 -I samples/narrow-window-index/verify_geometry.py --sample b
python3 -I samples/narrow-window-index/verify_images.py
python3 -I samples/narrow-window-index/verify_payload_import.py --binary /tmp/meridian --sample a
python3 -I samples/narrow-window-index/verify_payload_import.py --binary /tmp/meridian --sample b
python3 -I samples/narrow-window-index/run_index.py --binary /tmp/meridian
```

索引同事也可以只导入镜像：

```sh
curl -sS -X PUT http://127.0.0.1:8741/meridian/v1/image \
  -H "Content-Type: application/vnd.meridian.image" \
  -H "If-Meridian-Revision: 0" \
  --data-binary @samples/narrow-window-index/images/sample-a.mpb
```

导入后库 revision 为 1，图层 revision 也为 1，因此两份镜像可以在空目录上互换导入而不需要改请求或改数据。

