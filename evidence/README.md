# 窗口边界语义的前后证据

`boundary_evidence.py` 启动一个真实的 meridian HTTP 服务（临时数据目录），
通过公开接口写入固定夹具并查询两个边界场景，输出完整 JSON 响应与判定。

## 场景

查询窗口固定为 `[0,10) × [0,10)`（半开：左、下边包含，右、上边排除）。

1. **窗口边上只有一侧地物可见的共享边**（`POST /strata/edge-ware/seams`）
   - `outer-left [-6,2,0,8]` 完全在窗外，与窗内的 `inner-left [0,0,4,10]`
     在左侧（x=0，窗口包含边）共享竖边 → 必须返回，区间按窗口裁剪。
   - `outer-right [10,2,16,8]` 与窗内的 `inner-right [6,0,10,10]`
     在右侧（x=10，窗口排除边）共享竖边 → 必须排除。
   - `outer-top [2,10,8,16]` 与 `inner-bottom [0,6,10,10]`
     在上侧（y=10，窗口排除边）共享横边 → 必须排除。
   - 夹具还自然产生 `inner-bottom` 与 `outer-left` 在 x=0 的短竖边（y∈[6,8)），
     同样落在包含边上，一并返回。
2. **实际交集完全在窗外的全局相交对**（`POST /join`）
   - 窗内左层 `alpha [4,4,6,12]` 与右层 `bravo [2,10,8,14]` 全局相交，
     交集 `[4,10,6,12]` 完全位于窗口上方 → 配对必须被丢弃。
   - 同一 `alpha` 与 `charlie [2,6,8,8]` 的交集 `[4,6,6,8]` 在窗内 → 保留。

## 复现

```sh
CGO_ENABLED=0 python3 evidence/boundary_evidence.py
```

## 证据文件

- `before.json`：重构前（提交 cc46c80）的原始响应与判定；
- `after.json`：抽取统一候选遍历（`internal/spatial/candidates.go`）后的响应与判定。

两次运行的 `observed` 逐字段相同，五项判定全部为 `true`，证明两个入口的
窗口边界、候选计数（`visit`）、结果预算语义未发生变化。
