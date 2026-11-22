#!/usr/bin/env python3
"""Compose REPORT.md from params, payload-derived geometry and index results."""
import importlib.util
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent


def load_geometry():
    spec = importlib.util.spec_from_file_location('verify_geometry', HERE / 'verify_geometry.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    geometry = load_geometry()
    params = json.loads((HERE / 'params.json').read_text())
    windows = json.loads((HERE / 'windows.json').read_text())
    results = {s: json.loads((HERE / 'results' / f'sample-{s}.json').read_text()) for s in ('a', 'b')}
    derived = {}
    for s in ('a', 'b'):
        body, features, _ = geometry.load(s)
        derived[s] = {
            w['name']: dict(
                candidates=sorted(fid for fid, _, r in features
                                  if geometry.may_meet(r, w['request']['bounds'])),
                hits=sorted(fid for fid, _, r in features
                            if geometry.overlap(r, w['request']['bounds'])),
            ) for w in windows
        }
        for w in windows:
            visit = next(x for x in results[s]['windows'] if x['window'] == w['name'])
            assert visit['visit']['candidates'] == len(derived[s][w['name']]['candidates']), \
                (s, w['name'], 'visit candidates disagree with offline geometry')
            assert visit['feature_ids'] == derived[s][w['name']]['hits'], \
                (s, w['name'], 'returned features disagree with offline hits')

    lines = []
    add = lines.append
    add('# 狭窗索引候选样本：交付说明')
    add('')
    add('两份样本用同一套输入参数、同一批请求生成，几何上只有 180 个零面积接边矩形'
        '（fringe）的位置不同。样本 A 把这些 fringe 贴在每个查询窗的边上，样本 B 把它们'
        '停到远离所有窗的区块。因此狭窗的真实命中完全相同，包围候选数从 33 降到 3。')
    add('')
    add('## 交付物')
    add('')
    add('| 文件 | 用途 |')
    add('| --- | --- |')
    add('| `payloads/sample-a.json` / `payloads/sample-b.json` | `POST /strata` + `POST /edits` 的完整输入（各 400 次 insert，单批） |')
    add('| `images/sample-a.mpb` / `images/sample-b.mpb` | MPB v1 镜像，可直接 `PUT /image` 导入 |')
    add('| `windows.json` | 六个查询窗，两份样本共用同一文件 |')
    add('| `params.json` | 图层定义、数量、类别比例、角色构成 |')
    add('| `results/sample-a.json` / `results/sample-b.json` | 导入后的索引统计、每窗 visit、命中清单、请求哈希 |')
    add('| `build_samples.py` | 生成 payload 与归一化镜像 |')
    add('| `run_index.py` | 导入镜像并采集索引统计与每窗结果 |')
    add('| `verify_geometry.py` | 不经过服务器，用纯区间运算独立重算候选/命中 |')
    add('| `verify_images.py` | 直接解码两份 MPB 镜像并逐条对比目录内容 |')
    add('| `verify_payload_import.py` | 仅凭 payload JSON 重建镜像并逐字节比对 |')
    add('')
    add('## 输入参数')
    add('')
    layer = params['layer']
    add('```json')
    add(json.dumps(layer, indent=2, sort_keys=True))
    add('```')
    add('')
    add(f"- 地物总数：{params['feature_count']}，两份样本相同。")
    add(f"- 类别比例：core {params['class_counts']['core']} / halo {params['class_counts']['halo']}"
        f" / edge {params['class_counts']['edge']}，两份样本的每一条地物 ID→类别映射完全相同。")
    add(f"- 角色构成：anchor {params['roles']['anchor']}、hit {params['roles']['hit']}、"
        f"fringe {params['roles']['fringe']}、background {params['roles']['background']}。")
    add('- 查询窗口（`windows.json`，两份样本共用）：')
    add('')
    add('| 窗 | 请求体 | 请求 SHA-256 |')
    add('| --- | --- | --- |')
    for w, entry in zip(windows, results['a']['windows']):
        add(f"| {w['name']} | `{json.dumps(w['request'], sort_keys=True, separators=(',', ':'))}` | "
            f"`{entry['request_sha256'][:16]}…` |")
    add('')
    add('窗均为细长或小方窗；每个窗恰好 3 条正面积真实命中，命中几何在两份样本中完全相同。')
    add('')
    add('## 导入后的索引统计')
    add('')
    add('| 指标 | 样本 A | 样本 B |')
    add('| --- | --- | --- |')
    rows = [
        ('图层条目数 count', lambda r: r['layer']['count']),
        ('图层 revision', lambda r: r['layer']['revision']),
        ('库 revision', lambda r: r['index']['revision']),
        ('树 nodes', lambda r: r['index']['tree']['nodes']),
        ('树 leaves', lambda r: r['index']['tree']['leaves']),
        ('树 depth', lambda r: r['index']['tree']['depth']),
        ('树 items', lambda r: r['index']['tree']['items']),
        ('树 max_leaf', lambda r: r['index']['tree']['max_leaf']),
        ('root_bounds', lambda r: json.dumps(r['index']['tree']['root_bounds'], sort_keys=True)),
        ('index-audit records', lambda r: r['audit']['records']),
        ('index-audit checked_nodes', lambda r: r['audit']['checked_nodes']),
        ('导入后导出与镜像逐字节一致', lambda r: r['import']['export_is_byte_identical']),
        ('镜像 SHA-256', lambda r: '`' + r['import']['export_sha256'][:16] + '…`'),
        ('六窗候选总数', lambda r: sum(w['visit']['candidates'] for w in r['windows'])),
        ('六窗真实命中总数', lambda r: sum(len(w['feature_ids']) for w in r['windows'])),
        ('六窗访问节点总数', lambda r: sum(w['visit']['nodes'] for w in r['windows'])),
    ]
    for label, get in rows:
        add(f"| {label} | {get(results['a'])} | {get(results['b'])} |")
    add('')
    add('两份样本的图层边界（±80）、条目数（400）、类别比例、树形计数与 root_bounds 全部一致；'
        '差异只在狭窗所触及的节点候选上。')
    add('')
    add('## 每条窗的候选与真实命中')
    add('')
    for s in ('a', 'b'):
        add(f'### 样本 {s.upper()}')
        add('')
        add('| 窗 | visit.candidates | visit.nodes | 真实命中 | 返回 feature IDs |')
        add('| --- | --- | --- | --- | --- |')
        for entry in results[s]['windows']:
            hits = ', '.join(f'`{x}`' for x in entry['feature_ids'])
            add(f"| {entry['window']} | {entry['visit']['candidates']} | {entry['visit']['nodes']} | "
                f"{len(entry['feature_ids'])} | {hits} |")
        add('')
        add('候选 ID 清单（离线几何按同一 `MayMeet` 闭包重算，与 visit.candidates 逐窗相等）：')
        add('')
        for w in windows:
            cands = derived[s][w['name']]['candidates']
            shown = ', '.join(f'`{x}`' for x in cands)
            add(f"- **{w['name']}**（{len(cands)}）：{shown}")
        add('')
        probes = results[s]['class_probes']
        probe_text = ', '.join(f"{p['window']}={p['visit']['candidates']}" for p in probes)
        if s == 'a':
            add(f"补充探针 `class=halo`（同 bounds、同 limit）：候选 {probe_text}，命中均为 0。"
                '`visit.candidates` 在类别判定之前按包围盒计数，类别过滤不会压低候选数；'
                '结合上方候选 ID 清单可见，每个窗多出的 30 条全部是 `fringe-*`，真实命中始终只有 3 条。')
        else:
            add(f"补充探针 `class=halo`（同 bounds、同 limit）：候选 {probe_text}，命中均为 0。"
                '`visit.candidates` 在类别判定之前按包围盒计数，所以类别过滤不会把候选降到 0；'
                'B 每个窗的候选数已经等于真实命中数，没有额外的包围候选可被过滤。')
        add('')
    add('## 如何验证两份差异不是靠改请求获得')
    add('')
    add('1. **请求逐字节相同。** `results/sample-X.json` 每窗记录 `request_sha256`；上表两列的'
        '六窗哈希完全相等，`class_probes` 的哈希也相等。请求体只含 `bounds` 与 `limit`。')
    add('2. **窗口定义共用一份文件。** `windows.json` 被两份样本的构建与运行脚本共同读取，'
        '`build_samples.py` 中不存在按样本改写窗口的分支。')
    add('3. **数量与类别比例独立重算。** `verify_geometry.py --sample a|b` 不连接服务器，'
        '仅凭 payload 用半开区间运算核对 400 条、core/halo/edge = 200/120/80，并断言两份样本的'
        '非 fringe 记录逐字段相同、每窗命中 ID 列表相同。')
    add('4. **差异只允许出现在 fringe 位置。** 同一脚本断言恰好 180 条 `fringe-*` 记录在 A/B 间'
        '坐标不同，其余 220 条完全相同。')
    add('5. **payload 足以重建镜像。** `verify_payload_import.py --sample a|b` 直接从 JSON 创建图层'
        '并单批写入 400 条，导出、归一化后与 `images/sample-X.mpb` 逐字节相等（SHA-256 见上表）。')
    add('6. **镜像内容逐条对比。** `verify_images.py` 直接解码两份 MPB（校验签名、长度和 '
        'SHA-256），确认图层定义、修订、400 个 ID 与 ID→类别映射完全相同，并断言恰好 180 条 '
        '`fringe-*` 几何不同、其余 220 条逐字节相同（输出见 `results/image-comparison.json`）。')
    add('7. **运行可重复。** 对同一二进制重复执行 `run_index.py`，两次 `results/` 目录逐文件相同。')
    add('8. **索引自洽。** 两份样本的 `index-audit` 均报告 400 条、95 个节点；导入后立即导出与'
        '镜像逐字节一致，说明运行观察来自磁盘上的同一份数据。')
    add('')
    add('## 复现步骤')
    add('')
    add('```sh')
    add('go build -o /tmp/meridian ./cmd/meridian')
    add('python3 -I samples/narrow-window-index/build_samples.py --binary /tmp/meridian')
    add('python3 -I samples/narrow-window-index/verify_geometry.py --sample a')
    add('python3 -I samples/narrow-window-index/verify_geometry.py --sample b')
    add('python3 -I samples/narrow-window-index/verify_images.py')
    add('python3 -I samples/narrow-window-index/verify_payload_import.py --binary /tmp/meridian --sample a')
    add('python3 -I samples/narrow-window-index/verify_payload_import.py --binary /tmp/meridian --sample b')
    add('python3 -I samples/narrow-window-index/run_index.py --binary /tmp/meridian')
    add('```')
    add('')
    add('索引同事也可以只导入镜像：')
    add('')
    add('```sh')
    add('curl -sS -X PUT http://127.0.0.1:8741/meridian/v1/image \\')
    add('  -H "Content-Type: application/vnd.meridian.image" \\')
    add('  -H "If-Meridian-Revision: 0" \\')
    add('  --data-binary @samples/narrow-window-index/images/sample-a.mpb')
    add('```')
    add('')
    add('导入后库 revision 为 1，图层 revision 也为 1，因此两份镜像可以在空目录上互换导入而'
        '不需要改请求或改数据。')
    add('')
    (HERE / 'REPORT.md').write_text('\n'.join(lines) + '\n')


if __name__ == '__main__':
    main()
