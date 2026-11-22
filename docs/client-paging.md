# 客户端接入：续页、从头读还是停止

本文给第一次接入 Meridian Patch 的客户端一份可执行的判断依据：拿到一页 `window`
响应之后，遇到下一件事，应该**续页**、**从头读**还是**停止**。

所有结论都对照仓库里的实际行为，不是从规格推测的：

- 书签结构：`internal/bookmark/token.go`
- 续页判定：`internal/db/query.go:31`（`Window`）
- 修订推进与围栏：`internal/db/database.go:77`（`commit`）
- 镜像导出与恢复：`internal/db/image.go:13`（`Export`）、`internal/db/image.go:19`（`Restore`）
- HTTP 状态映射：`internal/httpapi/respond.go:21`

每一步都有可运行的对照：`examples/paging_client.py`。它启动自己的临时进程，串过异层改动、
同层写入、参数改变、重启和镜像恢复五个事件，并把每一步判定打印成决策表。运行方式见文末。

## 两种修订，用途不同

响应里的两个数字不是同一把尺子，混用会直接导致错误的重读或漏读。

| 字段 | 含义 | 用途 |
| --- | --- | --- |
| `revision` | 全库修订，每次成功提交 +1 | `if_revision`、`If-Meridian-Revision` 的 CAS 条件 |
| `layer_revision` | 该图层最后一次改动的全库修订 | 绑定书签；只有它决定分页能否续 |

`internal/db/query.go:43` 把 `l.revision`（图层修订）写进书签作用域，`internal/db/query.go:50`
只比较它。由此得到两条方向相反的规则：

- **别的图层写入会让 `revision` 前进，但不会让书签失效**，因为图层修订没变。
- **图层修订一变，全库修订必然也变了**，因为图层修订就是这个图层最后一次写入时的全库修订。
  反过来不成立：同一个 `revision` 下，不同图层的 `layer_revision` 可以相差很多。

所以别用 `revision` 判断分页，也别用 `layer_revision` 判断写入冲突。

## 三个判断：续页 / 从头读 / 停止

| 你要做的事 | 依据 | 结果 |
| --- | --- | --- |
| 用同一 `layer`、同一 `bounds`、同一 `class` 带上 `next` | 200，响应带新的 `next` 或没有 | **续页** |
| 换了图层、窗口边界或类别 | 400 `invalid` | **从头读**：这是另一次扫描，不是续页 |
| 同一图层有新的成功写入（`layer_revision` 变了） | 409 `conflict` | **从头读**：旧页作废，从第一页重扫 |
| 进程重启过 | 400 `invalid`（不是 409） | **从头读**：书签签名密钥已轮换 |
| 镜像恢复过 | 409 `conflict`（若图层仍在）或 404 `missing`（若图层被删） | **从头读** / **停止** |
| 图层已不存在 | 404 `missing` | **停止**：没有可续的对象 |

判断顺序建议如下，可省掉无谓的重读：

1. `409 conflict` → 从头读，新书签会带上新的图层修订。
2. `400 invalid` → 先分辨是参数错还是书签不匹配；若是书签不匹配且你确实换了谓词，
   按新谓词从头读。
3. `404 missing` → 停止，别重试。
4. `200` → 交付本页；有 `next` 就续页，没有就结束。

## 每一步的事实与出处

### 异层改动：续页

别的图层提交，`revision` 前进，`layer_revision` 不变，书签仍可用。实测：

```
other-layer write, resume  200  resume  db=4 layer=3 ids=['c', 'd']
```

同一次调用里再发一遍第一页请求，返回的行和 `next` 都与原来逐字节相同——书签是对
（图层、边界、类别、图层修订、最后 ID）的确定性封装，不依赖服务端状态
（`internal/bookmark/token.go:38`）。因此**重发同一个请求是安全的**，可以当作断点续传的
基础。

顺序读取的完整性来自谓词里的 `row.ID > after`（`internal/spatial/search.go:20`）与结果按
ID 排序，所以同一图层修订内按 ID 严格递增，不会重复、不会跳行。

### 同层写入：从头读

同一图层任何一次成功提交都会换掉 `layer_revision`，旧书签在 `internal/db/query.go:50`
被拒为 409：

```
same-layer write, rewind   409  rewind  bookmark revision has expired
```

要点是"成功提交"的边界比直觉窄：

| 请求 | 推进修订？ | 书签 |
| --- | --- | --- |
| 被整包拒绝的 `POST /edits`（越界、缺层、排他冲突） | 否 | 仍可用 |
| `PUT .../layout` 但布局与原来相同 | 否，直接返回当前修订（`internal/db/mutation.go:56`） | 仍可用 |
| `replace` 写入一个字节完全相同的值 | **是**，只要模式合法就建新层（`internal/db/mutation.go:137`） | **失效** |
| `if_revision` 不匹配的写入 | 否（提交前就拒绝） | 仍可用 |

实测中"被拒绝的批次"和"无变化的重设边界"都保住了书签，"写入相同值"却让它失效：

```
rejected batch, resume          200  resume  db=5 ids=['c', 'd']
no-op reframe, resume           200  resume  db=5 ids=['c', 'd']
idempotent replace, rewind      409  rewind  bookmark revision has expired (a same-valued replace still commits)
```

所以客户端不能把"我写的内容没变"当成"书签还有效"。判断依据只有响应里新的
`layer_revision`，或直接观察 409。

### 参数改变：从头读

书签作用域包含 `layer`、`bounds`、`class`（`internal/db/query.go:43`），比较在
`internal/db/query.go:47`。任何一项不同都返回 400 `invalid`：

```
class added, rewind      400  rewind  bookmark does not match the window predicate
bounds changed, rewind   400  rewind  bookmark does not match the window predicate
layer switched, rewind   400  rewind  bookmark does not match the window predicate
```

两条容易踩的细节：

- **`limit` 不在作用域里。** 分页大小可以随页变化：`internal/db/query.go:35` 只校验
  `1..256`，书签不记录它。实测同一书签用 `limit=3`、或连续用 `limit=1` 翻页，返回
  的行都从原游标继续、无重复。
- **400 不会烧掉书签。** 用错的谓词被拒后，换回原来的谓词同一书签仍可用：

  ```
  original predicate again  200  resume  ids=['c', 'd']; a 400 does not burn the token
  ```

- **缺省 `class` 与 `class: ""` 等价。** JSON 零值让两者都进入同一作用域，混用也不会误判。

### 重启：从头读（错误码是 400，不是 409）

重启时 `bookmark.New()` 用 `crypto/rand` 重新生成 HMAC 密钥
（`internal/bookmark/token.go:20`），书签不持久化，也不在镜像里。旧书签验签失败，
被归到"谓词不匹配"的同一个 400：

```
restart, rewind        400  rewind  db unchanged at 6; bookmark does not match the window predicate
cursor across restart, rewind  400  rewind  ... (same revision, new process key)
```

重启后 `revision` 和 `layer_revision` 都不变（镜像里带着它们），`GET /status` 可直接核对。
数据仍然可读，只是游标不能跨进程。

这意味着**不要把 `next` 存进任务队列跨进程/deploy 使用**。跨请求保留书签可以，跨重启不行。
重启后要么从头读，要么换成带 `if_revision` 的"按修订重读"策略。

### 镜像恢复：从头读，或停止

恢复在 `internal/db/image.go:19`：先把镜像解成候选状态（校验签名、长度、SHA-256、
JSON 规范形、几何规则），再在写锁内以目标库当前修订做 CAS，然后把**所有导入图层的
修订统一重定基**到新修订（`internal/db/image.go:25`、`:27`）。

- **目标修订过期** → 409 `conflict`，旧库保留（`internal/db/database.go:84`）。
- **镜像损坏** → 400 `invalid`，且校验先于 CAS：坏镜像配错的 `If-Meridian-Revision`
  报的是 400 而不是 409。
- **成功** → 新 `revision`，所有导入图层 `layer_revision` 都等于它；旧书签一律 409。

实测：

```
image restore, rewind      200  rewind  db=7 layers={'alpha-flat': 7, 'beta-flat': 7}
restored layer, rewind     409  rewind  bookmark revision has expired (imported layers were rebased)
```

注意"镜像本身很旧"并不等于"CAS 会失败"：CAS 比较的是**目标库当前修订**，不是镜像里的
修订。把上一版镜像恢复到更新的目标库上会成功，并**整体删除镜像里没有的图层**：

```
restore dropped layer, stop  404  stop  layer "gamma-flat" does not exist
```

所以恢复后，客户端对消失图层上的游标只能停止，不能重试；对仍在的图层则从头读。
恢复还会把整库换成镜像内容，客户端应当把恢复当成一次**新代**，丢弃所有缓存的行。

### 停止：只有 404 与不可写

- `404 missing`：图层或地物不存在，重试无意义。
- `503 unavailable`（`code: unavailable`）：进程已被围栏，写入永久停止
  （`internal/db/database.go:81`），读仍可用原内存视图。
- `503 commit_uncertain`：重命名已完成但目录同步未能确认（`internal/db/database.go:105`），
  进程已围栏。**结果未知**，不能当作成功也不能当作失败。

这两种 503 都不要循环重试写入；恢复方式见规格：重新打开数据目录。客户端可先用
`GET /status` 观察 `writable:false`（此时 `/status` 本身也返回 503）。

别把**持久化失败**和**围栏**混为一谈。实测中把数据目录设为只读后提交，返回的是
`500 internal`，`revision` 不变、`writable` 仍为 `true`，权限恢复后写入照常：

```
write while dir RO:      500 {"code": "internal", "message": "database operation failed"}
status right after:      200 {"revision": 2, "writable": true}
write after perms restored: 200 ...
```

这条路径发生在暂存或重命名**之前**，旧视图未被替换，所以不是围栏，也不会产生未知结果。
客户端可以安全重试；只有 503 才表示"停下并重新打开目录"。

其余状态码是请求本身的问题（`415 media_type`、`428 revision_required`、`413 capacity`），
与分页无关，修请求即可。唯一例外是 `428`：它意味着你漏带了 `If-Meridian-Revision`，
恢复请求根本没被考虑。

## 把页交给下游而不重复发布

游标只是"读到哪"，下游还关心"发布了哪些"。两者分开管，才能既快又不重复。

**发布键用 `(layer, layer_revision, id)`。** 同一图层修订内，按 ID 严格递增，因此
一个 `(layer, layer_revision)` 就是一代不可变内容；`id` 在层内唯一。把这三元组当幂等键，
重发、重放、断线重连都不会重复发布。

**一代只在收齐后提交。** 依次累加 `features`，直到响应**没有 `next`** 才把这一代整体
交给下游；中途出现 409，就丢弃未提交的缓冲、用新书签从头读。这样下游不会看到半截的
一代，也就不会出现"旧的半代 + 新的整代"混在一起。`examples/paging_client.py` 里的
`Publisher` 就是这么写的，并验证了：

```
downstream full scan       200  stop    3 pages, generation 10
replayed requests          200  resume  5 rows seen, 0 added, 1 committed generation
mid-scan 409, rewind       409  rewind  bookmark revision has expired; the partial page was not published
rewind publishes generation 200 stop    generations [10, 11]
no duplicate publication   200  resume  10 publications, 10 distinct; each generation publishes each id once and replaces the last
```

要点是"每一代内每个 id 恰好发布一次，新代整体替换上一代"，而不是"全库每个 id 永不再发"：
同层写入后内容确实变了，新一代重发全部行是正确的。

**如果下游要求跨代去重**，就在下游保留 `(layer, id) -> 最后发布的 layer_revision`，
只在新值更大时覆盖。图层修订单调递增（全库修订只增不减，图层修订取自全库修订，
见 `internal/db/database.go:90`），所以"取较大的修订"总能收敛到当前权威内容。

注意这里的语义是**权威**而不是**新旧内容**：镜像恢复会把导入图层重定基到一个更高的修订，
即使镜像里的内容比恢复前更旧。按较大修订覆盖仍然正确——恢复就是新的权威状态——但不要
用 `layer_revision` 的大小去推断"内容更晚产生"。

**分页大小可以变，发布键不变。** 增大 `limit` 少发几次请求、或减小 `limit` 细粒度推进，
发布结果完全一致——因为去重键不含 `limit`。这让客户端可以按下游背压动态调页大小。

## 可执行请求片段

以下片段对照本机实测行为，`Content-Type: application/json`，路径前缀 `/meridian/v1`。

### 1. 第一页

```sh
curl -sS -D - -X POST http://127.0.0.1:8741/meridian/v1/strata/alpha-flat/window \
  -H 'Content-Type: application/json' \
  -d '{"bounds":{"min_x":0,"min_y":0,"max_x":10,"max_y":4},"limit":2,"class":"fixture"}'
```

```json
{"revision":3,"layer_revision":3,"features":[{"id":"a","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":2,"max_y":2}}},{"id":"b","class":"fixture","shape":{"extent":{"min_x":2,"min_y":0,"max_x":4,"max_y":2}}}],"visit":{"nodes":1,"candidates":4},"next":"eyJzY29wZSI6..."}
```

字段顺序固定为 `revision`、`layer_revision`、`features`、`visit`、`next`。只有当候选行
**严格多于** `limit` 时才返回 `next`（`internal/db/query.go:56` 用的是 `>` 而不是 `>=`），
所以恰好读完一页不会多给一个空游标；没有更多数据时 `next` 字段整个省略，而不是 `null`
（`WindowPage.Next` 带 `omitempty`）。把 `next` 原样保存为续页凭据——它是带签名的、
不含任何服务端状态的字符串。

### 2. 续页（只换 `next`，谓词逐字相同）

```sh
curl -sS -X POST http://127.0.0.1:8741/meridian/v1/strata/alpha-flat/window \
  -H 'Content-Type: application/json' \
  -d '{"bounds":{"min_x":0,"min_y":0,"max_x":10,"max_y":4},"limit":2,"class":"fixture","bookmark":"<上一页的 next>"}'
```

`bounds` 与 `class` 必须与第一页相同；`limit` 可任意改。没有 `next` 就表示读完了。

### 3. 判定框架（伪代码，任何语言可套）

```python
def read_window(session, layer, bounds, limit, category=None, start=None):
    """Yields generations. Each generation is a list of rows, delivered whole."""
    cursor, rows = start, []
    while True:
        status, body = session.window(layer, bounds, limit, category, cursor)
        if status == 409:                     # 同层写入或镜像恢复
            cursor, rows = None, []           # 从第一页重来
            continue
        if status in (400, 404):              # 谓词不符 / 图层消失
            raise ScanStopped(status, body)   # 400 需换谓词；404 不要重试
        assert status == 200, (status, body)
        rows += body["features"]
        cursor = body.get("next")
        if cursor is None:
            yield body["layer_revision"], rows
            return
```

把 `layer_revision` 作为这一代的版本号交给下游即可实现上面的幂等键。

### 4. 带 CAS 的写入

```sh
curl -sS -X POST http://127.0.0.1:8741/meridian/v1/edits \
  -H 'Content-Type: application/json' \
  -d '{"if_revision":7,"edits":[{"layer":"alpha-flat","id":"a","mode":"replace","value":{"id":"a","class":"fixture","shape":{"extent":{"min_x":0,"min_y":0,"max_x":3,"max_y":2}}}}]}'
```

409 表示别的写入者先提交了；用响应中看到的 `revision` 或新读一页后重试。这是**全库**条件，
不是图层条件。

### 5. 导出与恢复

```sh
# 导出：响应头带 Meridian-Revision，正文为 MPB v1 二进制
curl -sS -D headers.txt -o backup.mpb http://127.0.0.1:8741/meridian/v1/image

# 恢复：目标修订取自当前 GET /status，而不是镜像里的修订
curl -sS -X PUT http://127.0.0.1:8741/meridian/v1/image \
  -H 'Content-Type: application/vnd.meridian.image' \
  -H 'If-Meridian-Revision: 7' \
  --data-binary @backup.mpb
```

恢复成功后的响应是目录：`revision` 与每个导入图层的 `revision` 都是新的同一值。
**恢复后必须丢弃所有书签**，按上面的规则从头读；镜像里没有的图层直接判定停止。

### 6. 重启后的核对

```sh
curl -sS http://127.0.0.1:8741/meridian/v1/status
# {"revision":7,"writable":true}
```

`revision` 应与重启前一致；`writable:false` 或 503 表示进程已被围栏，只读，需要重新打开
数据目录。用旧书签续页此时会得到 400，应从第一页读。

## 常见误判

| 现象 | 不要做 | 应该做 |
| --- | --- | --- |
| `revision` 变了 | 丢弃书签从头读 | 看 `layer_revision` 是否变；没变就续页 |
| 自己的写入没改变内容 | 认为书签仍有效 | `replace` 相同值也会推进图层修订；按 409 处理 |
| 400 后以为书签坏了 | 永久丢弃书签 | 换回原谓词，它仍可用 |
| 重启后收到 400 | 当作写入冲突去重试写入 | 从头读；400 是密钥轮换 |
| 恢复后 404 | 重试直到图层出现 | 停止；图层不在镜像里 |
| 恢复后 `revision` 只是 +1 | 以为只改了一张表 | 全库换成镜像内容，所有图层修订都重定基 |
| 用小页读到一半又换大页 | 担心重复或漏行 | `limit` 不在作用域内，游标照常推进 |

## 自查

```sh
go build ./...
python3 examples/paging_client.py            # 打印决策表 + 汇总 JSON
```

`examples/paging_client.py` 使用临时目录和系统分配端口，不触碰运行中的数据库；每次运行
输出逐字节一致，可当作接入前的回归基线。它断言了本文每一个"续页/从头读/停止"结论，
断言失败会直接给出事件名与实际观察。
