# legacy-parcels 矩形移交：转换与核对报告

把旧镜像 `input/old-image.mpb` 中 `legacy-parcels` 图层的 15 个矩形移交给目标团队。
目标方提供的排他边界更窄（`[-8,-8)-(40,24)`，`exclusive=true`），要求保持原 ID，
且拒绝接收有冲突的数据。本目录是完整的本地转换及核对材料。

## 输入与目标约束

| 项 | 值 |
| --- | --- |
| 旧镜像 | `input/old-image.mpb`（MPB v1，2019 字节，sha256 `8a65965f…95567f`） |
| 源图层 | `legacy-parcels`，extent，边界 `[-64,-64)-(96,96)`，非排他，15 个矩形 |
| 目标约束 | 同图层名，边界收窄为 `[-8,-8)-(40,24)`，排他（禁止正面积重叠，允许接边） |
| 随行图层 | `control-marks`（3 个点）不受新边界约束，原样随行，用于证明转换是有选择的 |
| 镜像修订 | 源镜像修订 3，移交镜像保留原修订号作为血缘信息；导入时由目标库重新分配 |

坐标均按半开区间 `[min,max)` 解释，冲突指候选形状之间的正面积交集。

## 分类结果（三桶）

每条记录的原形状、候选形状（剪裁后）、冲突同伴完整保存于 `output/report.json`。

### 1. 完全合法（2 条，直接移交）

| ID | 类别 | 原形状 = 候选形状 | 冲突同伴 |
| --- | --- | --- | --- |
| alpha | survey | (0,0)-(8,8) | 无 |
| bravo | fixture | (12,0)-(20,6) | 无 |

### 2. 可剪裁后合法（3 条，按候选形状移交）

| ID | 类别 | 原形状 | 候选形状（剪裁后） | 冲突同伴 |
| --- | --- | --- | --- | --- |
| charlie | fixture | (-12,0)-(-4,6) | (-8,0)-(-4,6) | 无 |
| delta | survey | (30,18)-(48,28) | (30,18)-(40,24) | 无 |
| mike | fixture | (34,4)-(48,10) | (34,4)-(40,10) | 无 |

### 3. 剪裁后仍冲突（10 条，不直接移交）

无法安置（候选形状为空，冲突对象是目标边界本身）：

| ID | 类别 | 原形状 | 候选形状 | 原因 |
| --- | --- | --- | --- | --- |
| echo | survey | (44,4)-(56,10) | 空 | 整体在边界外（原与 mike 重叠的区域也在界外） |
| foxtrot | fixture | (0,30)-(10,40) | 空 | 整体在边界外 |
| golf | survey | (-12,2)-(-8,9) | 空 | 仅与边界贴边（max_x = -8），剪裁后零面积 |

互斥组成员（各自可保留，但与同伴正面积重叠，见下节消解）：

| ID | 类别 | 原形状 = 候选形状 | 冲突同伴 | 消解结果 |
| --- | --- | --- | --- | --- |
| hotel | survey | (24,8)-(32,16) | india | 保留 |
| india | fixture | (26,2)-(33,10) | hotel | 舍弃 |
| juliett | fixture | (22,-6)-(28,0) | kilo | 保留 |
| kilo | survey | (24,-4)-(30,2) | juliett | 舍弃 |
| november | survey | (4,12)-(12,20) | oscar | 保留 |
| oscar | fixture | (8,14)-(16,22) | november, papa | 舍弃 |
| papa | survey | (14,12)-(22,20) | oscar | 保留 |

## 互斥消解：选择依据与被舍弃者

选择规则（确定性，写入 `report.json` 的 `selection_rule`）：组内按候选面积降序、
ID 升序依次考虑；与已保留者无正面积重叠则保留，否则舍弃。

| 冲突组 | 保留 | 被舍弃者 | 选择依据 |
| --- | --- | --- | --- |
| hotel, india | hotel | **india** | hotel 候选面积 64 > india 56；重叠区面积 12 |
| juliett, kilo | juliett | **kilo** | 面积并列 36，按 ID 升序 juliett 胜出；重叠区面积 16 |
| november, oscar, papa | november, papa | **oscar** | 三者面积并列 64，november 凭 ID 升序先保留；oscar 与 november 重叠（面积 24）被舍弃；papa 与已保留者无重叠，保留 |

## 最终移交清单（9 条，全部保持原 ID）

`alpha, bravo`（原样）＋ `charlie, delta, mike`（按剪裁后形状）＋
`hotel, juliett, november, papa`（互斥组胜出者）。
被舍弃不移交：`echo, foxtrot, golf`（无法安置）与 `india, kilo, oscar`（互斥落选）。

最终可用文件：`output/handover-image.mpb`（sha256 `57d5fc3a…248866`）。

## 临时目标库真实导入核验

`verification/import-log.json` 记录了在一个全新临时数据库（空目录、系统分配端口）
上的完整核验，13 步全部通过：

1. 目标库初始为空（修订 0）。
2. 对照组 A（只换图层定义、不剪裁的 15 条原形状）被目标库拒绝：
   `400 invalid — feature "charlie" is outside legacy-parcels bounds`。
3. 对照组 B（剪裁但不做互斥消解）被拒绝：
   `400 invalid — exclusive layer legacy-parcels: hotel intersects india`。
   两组对照证明目标库确实会拒绝有冲突的数据，转换是必要的。
4. 两次拒绝后目标库仍是修订 0（旧视图保留）。
5. `PUT /meridian/v1/image`（`If-Meridian-Revision: 0`）真实导入移交镜像一次，成功，目标库分配修订 1。
6. 回读核对：图层定义（收窄边界 + 排他）与 9 条地物的 ID、类别、形状逐项一致；
   `index-audit` 记录数 9 与目录一致；`control-marks` 3 个点原样未动；
   再导出的目录与移交目录逐层相同（仅修订被重分配）。
7. 探针：向导入后的图层插入与 alpha 重叠的矩形被 `409 conflict` 拒绝，
   且失败写入未推进修订——图层在线后排他策略真实生效。

## 复现

```sh
go build -o /tmp/meridian ./cmd/meridian   # 需 Go 1.22+
python3 -I handover/tool/seed_source.py  --binary /tmp/meridian --out handover/input/old-image.mpb
python3 -I handover/tool/convert.py      --input handover/input/old-image.mpb \
    --layer legacy-parcels --bounds=-8,-8,40,24 --exclusive \
    --image-out handover/output/handover-image.mpb --report-out handover/output/report.json
python3 -I handover/tool/verify_import.py --binary /tmp/meridian \
    --old-image handover/input/old-image.mpb --image handover/output/handover-image.mpb \
    --report handover/output/report.json --log-out handover/verification/import-log.json
```

## 文件清单

| 路径 | 说明 |
| --- | --- |
| `tool/mpb.py` | MPB v1 编解码，与 `internal/folio` 逐字节对齐（签名、长度、SHA-256、规范 JSON） |
| `tool/client.py` | 最小 HTTP 客户端与临时库进程管理 |
| `tool/seed_source.py` | 生成旧镜像的场景脚本（临时源库导出） |
| `tool/convert.py` | 转换器：剪裁、三桶分类、互斥消解、产出移交镜像与报告 |
| `tool/verify_import.py` | 核验器：临时目标库对照拒绝、真实导入、回读比对 |
| `input/old-image.mpb` | 旧镜像（转换输入） |
| `output/handover-image.mpb` | 最终可用文件（已真实导入核验） |
| `output/report.json` | 机器可读核对材料（三桶、原/候选形状、冲突同伴、选择依据） |
| `verification/import-log.json` | 导入核验逐步记录 |
