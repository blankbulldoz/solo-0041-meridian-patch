#!/usr/bin/env python3
"""Reconcile an external survey point table against Meridian locate results.

Seeds a throwaway Meridian instance from reconcile/layers.json, locates every
record of reconcile/points.csv on every layer, and writes a deterministic
reconciliation checklist (report.json + report.md). Rerun with --check to
verify the committed baseline still reproduces byte for byte.
"""
import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import select
import shutil
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
DATA = Path(__file__).resolve().parent / 'reconcile'
PREFIX = '/meridian/v1'
BUCKETS = ('unassigned', 'unique', 'multiple')
BUCKET_LABEL = {'unassigned': '无归属', 'unique': '唯一归属', 'multiple': '多重归属'}


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, status=200, headers=None):
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            args['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=value, method=method, headers=args)
        try:
            response = urlopen(req, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: got {response.status}, want {status}: {raw[:512]}')
            return json.loads(raw) if response.headers.get_content_type() == 'application/json' else raw

    def get(self, path, status=200):
        return self.request('GET', path, status=status)

    def post(self, path, value, status=200):
        return self.request('POST', path, value, status)


def go_binary():
    found = shutil.which('go')
    if found:
        return found
    candidate = Path('/tmp/go/bin/go')
    if candidate.exists():
        return str(candidate)
    raise SystemExit('go toolchain not found on PATH or at /tmp/go/bin/go')


def build(binary):
    env = dict(os.environ, CGO_ENABLED='0')
    subprocess.run([go_binary(), 'build', '-o', str(binary), './cmd/meridian'],
                   check=True, cwd=ROOT, env=env, timeout=120)


def running(binary, data):
    args = [str(binary), '-listen', '127.0.0.1:0', '-data', str(data)]
    child = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        readable, _, _ = select.select([child.stderr], [], [], 10)
        if not readable:
            raise RuntimeError('service did not start within 10 seconds')
        startup = json.loads(child.stderr.readline())
        if 'address' not in startup:
            raise RuntimeError(startup)
        session = Session('http://' + startup['address'])
        assert session.get('/status')['revision'] == startup['revision']
        return child, session
    except Exception:
        child.kill()
        child.communicate(timeout=5)
        raise


def stop(child):
    if child.poll() is None:
        child.terminate()
    try:
        child.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        child.kill()
        child.communicate(timeout=5)


def seed(session, layers):
    """Create layers in file order, then commit one edit packet per layer."""
    for layer in layers:
        definition = {key: layer[key] for key in ('name', 'kind', 'bounds', 'exclusive')}
        session.post('/strata', definition, status=201)
    for layer in layers:
        edits = [dict(layer=layer['name'], id=feature['id'], mode='insert', value=feature)
                 for feature in layer['features']]
        session.post('/edits', dict(edits=edits))


def load_points(path):
    with open(path, newline='', encoding='utf-8') as handle:
        rows = list(csv.reader(handle))
    header, body = rows[0], rows[1:]
    expected = ['record_id', 'point_id', 'x', 'y', 'observed_at', 'crew', 'reading']
    if header != expected:
        raise SystemExit(f'{path}: header {header} does not match {expected}')
    records = []
    for offset, row in enumerate(body):
        line = offset + 2  # header occupies line 1
        if len(row) != len(expected):
            raise SystemExit(f'{path}:{line}: expected {len(expected)} fields, got {len(row)}')
        record = dict(zip(expected, row))
        record['line'] = line
        record['x'] = int(record['x'])
        record['y'] = int(record['y'])
        records.append(record)
    return records


def bucket_of(features):
    return BUCKETS[min(len(features), 2)]


def reconcile(session, layer_names, records):
    """Locate every record on every layer and assemble the reconciliation."""
    located = []
    for record in records:
        per_layer = {}
        for name in layer_names:
            result = session.post(f'/strata/{name}/locate', {'x': record['x'], 'y': record['y']})
            features = [row['id'] for row in result['features']]
            per_layer[name] = {
                'features': features,
                'bucket': bucket_of(features),
                'layer_revision': result['layer_revision'],
                'revision': result['revision'],
            }
        located.append(dict(record, locate=per_layer))

    buckets = {name: {bucket: [] for bucket in BUCKETS} for name in layer_names}
    for record in located:
        for name in layer_names:
            buckets[name][record['locate'][name]['bucket']].append(record['record_id'])

    by_point = {}
    for record in located:
        by_point.setdefault(record['point_id'], []).append(record)
    duplicates = []
    for point_id in sorted(by_point):
        group = by_point[point_id]
        if len(group) < 2:
            continue
        by_layer, divergent_layers = {}, []
        for name in layer_names:
            assignments = {row['record_id']: row['locate'][name]['features'] for row in group}
            by_layer[name] = assignments
            if len({tuple(ids) for ids in assignments.values()}) > 1:
                divergent_layers.append(name)
        duplicates.append({
            'point_id': point_id,
            'records': [row['record_id'] for row in group],
            'divergent': bool(divergent_layers),
            'divergent_layers': divergent_layers,
            'by_layer': by_layer,
        })

    by_coordinate = {}
    for record in located:
        by_coordinate.setdefault((record['x'], record['y']), []).append(record)
    co_located = []
    for (x, y), group in sorted(by_coordinate.items()):
        if len(group) < 2:
            continue
        consistent = all(
            len({tuple(row['locate'][name]['features']) for row in group}) == 1
            for name in layer_names
        )
        co_located.append({
            'x': x, 'y': y,
            'records': [row['record_id'] for row in group],
            'consistent': consistent,
        })
    return located, buckets, duplicates, co_located


def check_invariants(located, buckets, duplicates, co_located, catalog, layer_names):
    invariants = []

    same_coordinate = all(group['consistent'] for group in co_located)
    invariants.append({
        'name': 'same-coordinate-same-assignment',
        'label': '同坐标记录的定位结果一致',
        'status': 'pass' if same_coordinate else 'fail',
    })

    catalog_revision = {layer['name']: layer['revision'] for layer in catalog}
    stable = all(
        record['locate'][name]['revision'] == located[0]['locate'][layer_names[0]]['revision']
        and record['locate'][name]['layer_revision'] == catalog_revision[name]
        for record in located for name in layer_names
    )
    invariants.append({
        'name': 'stable-revision-across-locates',
        'label': '定位期间全库与图层修订稳定',
        'status': 'pass' if stable else 'fail',
    })

    complete = all(
        sorted(rid for bucket in BUCKETS for rid in buckets[name][bucket])
        == sorted(record['record_id'] for record in located)
        for name in layer_names
    )
    invariants.append({
        'name': 'every-record-bucketed-once-per-layer',
        'label': '每笔记录在每层恰好落入一个归属桶',
        'status': 'pass' if complete else 'fail',
    })
    return invariants


def fmt_features(features):
    return ' + '.join(features) if features else '—'


def fmt_bounds(bounds):
    return f"[{bounds['min_x']},{bounds['min_y']},{bounds['max_x']},{bounds['max_y']})"


def render_markdown(report, csv_text):
    layer_names = [layer['name'] for layer in report['seed_order']]
    catalog = {layer['name']: layer for layer in report['database']['layers']}
    lines = [
        '# 实测点表对账清单',
        '',
        '本清单由 `examples/reconcile.py` 自动生成：以 `examples/reconcile/layers.json` 为种子搭建临时',
        'Meridian 实例，对 `examples/reconcile/points.csv` 的每笔记录逐层调用',
        '`POST /meridian/v1/strata/{layer}/locate`，按半开归属规则（左下包含、右上排除）判定归属。',
        '复跑 `python3 examples/reconcile.py --check` 可验证本清单与 `report.json` 逐字节重现。',
        '',
        f"- 输入：`points.csv`，{report['source']['records']} 笔记录，sha256 `{report['source']['sha256']}`",
        f"- 数据库修订：{report['database']['revision']}（全部定位调用在同一视图下完成）",
        '- 边界记法：`[min_x,min_y,max_x,max_y)` 表示左下包含、右上排除',
        '',
        '## 图层修订对照表',
        '',
        '| 图层 | 类型 | 排他 | 边界 | 图层修订 | 地物数 |',
        '| --- | --- | --- | --- | --- | --- |',
    ]
    for layer in report['database']['layers']:
        lines.append(
            f"| {layer['name']} | {layer['kind']} | {'是' if layer['exclusive'] else '否'} "
            f"| {fmt_bounds(layer['bounds'])} | {layer['revision']} | {layer['count']} |"
        )
    lines += [
        '',
        '## 输入样例（points.csv 原文）',
        '',
        '```csv',
        csv_text.rstrip('\n'),
        '```',
        '',
        '## 归属分桶',
        '',
    ]
    records_by_id = {record['record_id']: record for record in report['records']}
    for name in layer_names:
        lines.append(f"### {name}（图层修订 {catalog[name]['revision']}）")
        lines.append('')
        for bucket in BUCKETS:
            ids = report['buckets'][name][bucket]
            lines.append(f"**{BUCKET_LABEL[bucket]}（{len(ids)}）**")
            lines.append('')
            if ids:
                lines.append('| 记录 | 测点编号 | 坐标 | CSV 行 | 归属地物 |')
                lines.append('| --- | --- | --- | --- | --- |')
                for rid in ids:
                    record = records_by_id[rid]
                    features = record['locate'][name]['features']
                    lines.append(
                        f"| {rid} | {record['point_id']} | ({record['x']},{record['y']}) "
                        f"| {record['line']} | {fmt_features(features)} |"
                    )
            else:
                lines.append('（无）')
            lines.append('')

    lines += [
        '## 重复编号几何分歧',
        '',
        '同一测点编号出现多笔记录时，逐层比较其归属地物集合；任一层不一致即记为几何分歧。',
        '',
        '| 测点编号 | 记录 | 几何分歧 | 分歧图层 |',
        '| --- | --- | --- | --- |',
    ]
    for group in report['duplicate_point_ids']:
        divergent = '是' if group['divergent'] else '否'
        layers = ', '.join(group['divergent_layers']) if group['divergent_layers'] else '—'
        lines.append(f"| {group['point_id']} | {', '.join(group['records'])} | {divergent} | {layers} |")
    lines.append('')
    for group in report['duplicate_point_ids']:
        lines.append(f"### {group['point_id']} 组明细")
        lines.append('')
        header = '| 记录 | 坐标 | ' + ' | '.join(layer_names) + ' |'
        lines.append(header)
        lines.append('| --- | --- |' + ' --- |' * len(layer_names))
        for rid in group['records']:
            record = records_by_id[rid]
            cells = ' | '.join(fmt_features(record['locate'][name]['features']) for name in layer_names)
            lines.append(f"| {rid} | ({record['x']},{record['y']}) | {cells} |")
        lines.append('')

    lines += [
        '## 同坐标观测',
        '',
        '同一坐标的不同观测在同一数据库视图下必须得到相同归属；下表列出全部同坐标组并核验一致性。',
        '',
        '| 坐标 | 记录 | 归属一致 |',
        '| --- | --- | --- |',
    ]
    for group in report['co_located']:
        consistent = '是' if group['consistent'] else '否'
        lines.append(f"| ({group['x']},{group['y']}) | {', '.join(group['records'])} | {consistent} |")
    lines += [
        '',
        '## 不变量核验',
        '',
        '| 不变量 | 结果 |',
        '| --- | --- |',
    ]
    for invariant in report['invariants']:
        status = '通过' if invariant['status'] == 'pass' else '失败'
        lines.append(f"| {invariant['label']} | {status} |")
    lines += [
        '',
        '## 全部记录明细',
        '',
        '| CSV 行 | 记录 | 测点编号 | 坐标 | ' + ' | '.join(layer_names) + ' |',
        '| --- | --- | --- | --- |' + ' --- |' * len(layer_names),
    ]
    for record in report['records']:
        cells = ' | '.join(fmt_features(record['locate'][name]['features']) for name in layer_names)
        lines.append(
            f"| {record['line']} | {record['record_id']} | {record['point_id']} "
            f"| ({record['x']},{record['y']}) | {cells} |"
        )
    lines.append('')
    return '\n'.join(lines)


def produce(out_dir):
    layers_doc = json.loads((DATA / 'layers.json').read_text(encoding='utf-8'))
    layers = layers_doc['layers']
    layer_names = [layer['name'] for layer in layers]
    csv_bytes = (DATA / 'points.csv').read_bytes()
    csv_text = csv_bytes.decode('utf-8')
    records = load_points(DATA / 'points.csv')

    with tempfile.TemporaryDirectory(prefix='meridian-reconcile-') as temporary:
        binary = Path(temporary) / 'meridian'
        build(binary)
        child, session = running(binary, Path(temporary) / 'space')
        try:
            seed(session, layers)
            catalog = session.get('/strata')['layers']
            revision = session.get('/status')['revision']
            located, buckets, duplicates, co_located = reconcile(session, layer_names, records)
        finally:
            stop(child)

    invariants = check_invariants(located, buckets, duplicates, co_located, catalog, layer_names)
    report = {
        'tool': 'examples/reconcile.py',
        'source': {
            'file': 'points.csv',
            'sha256': hashlib.sha256(csv_bytes).hexdigest(),
            'records': len(records),
        },
        'database': {
            'revision': revision,
            'layers': [
                {key: layer[key] for key in ('name', 'kind', 'bounds', 'exclusive', 'revision', 'count')}
                for layer in catalog
            ],
        },
        'seed_order': [{'name': name} for name in layer_names],
        'records': [
            {
                'record_id': record['record_id'],
                'line': record['line'],
                'point_id': record['point_id'],
                'x': record['x'],
                'y': record['y'],
                'observed_at': record['observed_at'],
                'crew': record['crew'],
                'reading': record['reading'],
                'locate': record['locate'],
            }
            for record in located
        ],
        'buckets': buckets,
        'duplicate_point_ids': duplicates,
        'co_located': co_located,
        'invariants': invariants,
        'summary': {
            'records': len(records),
            'duplicate_groups': len(duplicates),
            'divergent_groups': sum(1 for group in duplicates if group['divergent']),
            'per_layer': {
                name: {bucket: len(buckets[name][bucket]) for bucket in BUCKETS}
                for name in layer_names
            },
        },
    }

    out_dir.mkdir(parents=True, exist_ok=True)
    json_text = json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + '\n'
    md_text = render_markdown(report, csv_text)
    (out_dir / 'report.json').write_text(json_text, encoding='utf-8')
    (out_dir / 'report.md').write_text(md_text, encoding='utf-8')
    return report


def compare(generated, baseline):
    import difflib
    ok = True
    for name in ('report.json', 'report.md'):
        want = (baseline / name).read_text(encoding='utf-8').splitlines()
        got = (generated / name).read_text(encoding='utf-8').splitlines()
        if want == got:
            print(f'check ok: {name} reproduces {baseline / name}')
            continue
        ok = False
        print(f'check FAILED: {name} differs from {baseline / name}')
        diff = difflib.unified_diff(want, got, fromfile=str(baseline / name), tofile=name, lineterm='')
        for line in list(diff)[:40]:
            print(line)
    return ok


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true',
                        help='regenerate and compare against the committed baseline')
    parser.add_argument('--out', type=Path, default=None,
                        help='output directory (default: examples/reconcile)')
    args = parser.parse_args()

    if args.check:
        with tempfile.TemporaryDirectory(prefix='meridian-reconcile-check-') as temporary:
            report = produce(Path(temporary))
            ok = compare(Path(temporary), DATA)
    else:
        out_dir = args.out or DATA
        report = produce(out_dir)
        print(f'wrote {out_dir / "report.json"} and {out_dir / "report.md"}')
        ok = True

    failed = [item['name'] for item in report['invariants'] if item['status'] != 'pass']
    if failed:
        print(f'invariants FAILED: {", ".join(failed)}')
        ok = False
    summary = report['summary']
    print(json.dumps({'records': summary['records'],
                      'duplicate_groups': summary['duplicate_groups'],
                      'divergent_groups': summary['divergent_groups'],
                      'per_layer': summary['per_layer']}, ensure_ascii=False, sort_keys=True))
    sys.exit(0 if ok else 1)


if __name__ == '__main__':
    main()
