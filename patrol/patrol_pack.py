#!/usr/bin/env python3
"""Build and re-check a deterministic spatial patrol sample pack.

The tool talks to a running Meridian Patch HTTP service.  It reads a candidate
probe file (duplicate coordinates and clustered hotspots are allowed), asks the
service to locate each distinct point and to list its nearest neighbors, then
classifies the probes into three patrol groups and picks a reproducible sample:

  edge_zero_unowned -- locate returns no owner while the closest feature has
                       closed distance zero (the point sits on an open edge)
  tie               -- the nearest-neighbor distance is shared by >=2 features
  unique            -- exactly one feature sits at the nearest distance

Each group first covers as many distinct feature classes as possible; any
remaining quota is filled by a fixed ordering rule.  Every choice is driven by
the service responses and probe IDs only, so re-running against the same
database revision produces byte-identical output.

Subcommands:
  seed       load a scenario JSON file into a running service
  generate   build a pack from --candidates against a live service
  verify     re-query the same service and validate a previously built pack
  demo       build the binary, seed a throwaway database, generate and verify
"""
import argparse
import hashlib
import json
import os
import select
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

PREFIX = '/meridian/v1'
COORD_LIMIT = 1_000_000_000
NEAREST_LIMIT = 128
RULE_VERSION = 'patrol-pack-r1'
GROUPS = ('edge_zero_unowned', 'tie', 'unique')

RAW_FILE = 'raw-probes.json'
EVIDENCE_FILE = 'evidence.json'
SELECTION_FILE = 'selection.json'
SHORTFALL_FILE = 'shortfalls.json'
MANIFEST_FILE = 'manifest.json'
VERDICT_FILE = 'verify-verdict.json'
OUTPUT_FILES = (RAW_FILE, EVIDENCE_FILE, SELECTION_FILE, SHORTFALL_FILE)


# --------------------------------------------------------------------------
# HTTP client
# --------------------------------------------------------------------------

class Client:
    def __init__(self, base_url):
        self.base_url = base_url.rstrip('/')

    def call(self, method, path, payload=None):
        data = None
        headers = {}
        if payload is not None:
            data = json.dumps(payload).encode()
            headers['Content-Type'] = 'application/json'
        request = Request(self.base_url + PREFIX + path, data=data,
                          method=method, headers=headers)
        try:
            with urlopen(request, timeout=10) as response:
                return json.loads(response.read())
        except HTTPError as error:
            body = error.read().decode('utf-8', 'replace')
            raise RuntimeError(f'{method} {path} -> {error.code}: {body[:300]}')

    def status(self):
        return self.call('GET', '/status')

    def strata(self):
        return self.call('GET', '/strata')

    def locate(self, layer, x, y):
        return self.call('POST', f'/strata/{layer}/locate', {'x': x, 'y': y})

    def nearest(self, layer, x, y):
        return self.call('POST', f'/strata/{layer}/nearest',
                         {'point': {'x': x, 'y': y}, 'limit': NEAREST_LIMIT})

    def create_layer(self, definition):
        return self.call('POST', '/strata', definition)

    def edit(self, edits, if_revision=None):
        payload = {'edits': edits}
        if if_revision is not None:
            payload['if_revision'] = if_revision
        return self.call('POST', '/edits', payload)


# --------------------------------------------------------------------------
# Candidate loading and validation
# --------------------------------------------------------------------------

def load_candidates(path, layer_override):
    with open(path, 'rb') as handle:
        raw = handle.read()
    document = json.loads(raw.decode())
    layer = layer_override or document.get('layer')
    if not layer:
        raise SystemExit('candidate file has no "layer" and --layer was not given')
    records = document.get('probes')
    if not isinstance(records, list) or not records:
        raise SystemExit('candidate file must contain a non-empty "probes" list')

    normalized, invalid, seen_ids = [], [], set()
    for index, record in enumerate(records):
        probe_id = record.get('probe_id') or f'probe-{index + 1:03d}'
        if probe_id in seen_ids:
            raise SystemExit(f'duplicate probe_id {probe_id!r} in candidate file')
        seen_ids.add(probe_id)
        entry = {
            'probe_id': probe_id,
            'x': record.get('x', 0),
            'y': record.get('y', 0),
            'note': record.get('note', ''),
            'input_index': index,
        }
        reason = validate_probe(entry)
        if reason:
            invalid.append({'probe_id': probe_id, 'x': entry['x'], 'y': entry['y'],
                            'reason': reason})
            entry['valid'] = False
            entry['reject_reason'] = reason
        else:
            entry['valid'] = True
        normalized.append(entry)
    return {
        'layer': layer,
        'source_sha256': hashlib.sha256(raw).hexdigest(),
        'records': normalized,
        'invalid': invalid,
    }


def validate_probe(entry):
    x, y = entry['x'], entry['y']
    if isinstance(x, bool) or isinstance(y, bool) or \
            not isinstance(x, int) or not isinstance(y, int):
        return 'coordinates must be JSON integers'
    if abs(x) > COORD_LIMIT or abs(y) > COORD_LIMIT:
        return f'coordinates must be within [-{COORD_LIMIT},{COORD_LIMIT}]'
    return None


# --------------------------------------------------------------------------
# Evidence gathering and classification
# --------------------------------------------------------------------------

def gather_evidence(client, layer, records):
    unique = {}
    duplicates = []
    for record in records:
        if not record['valid']:
            continue
        key = (record['x'], record['y'])
        if key in unique:
            duplicates.append({'coordinate': {'x': key[0], 'y': key[1]},
                               'kept_probe_id': unique[key],
                               'duplicate_probe_id': record['probe_id']})
        else:
            unique[key] = record['probe_id']

    evidence = []
    revisions = {}
    for (x, y), probe_id in sorted(unique.items(), key=lambda item: item[1]):
        locate = client.locate(layer, x, y)
        nearest = client.nearest(layer, x, y)
        revisions.setdefault('revision', locate['revision'])
        revisions.setdefault('layer_revision', locate['layer_revision'])
        owners = locate['features']
        neighbors = nearest['neighbors']
        labels = classify(owners, neighbors)
        minimum = neighbors[0]['distance_squared'] if neighbors else None
        closest = [row for row in neighbors
                   if minimum is not None and row['distance_squared'] == minimum]
        # Only nearest-distance evidence drives class coverage; far neighbors
        # stay in the payload but do not dilute the coverage signature.
        signature = sorted({row['class'] for row in owners}
                           | {row['feature']['class'] for row in closest})
        evidence.append({
            'probe_id': probe_id,
            'coordinate': {'x': x, 'y': y},
            'labels': labels,
            'primary_group': labels[0] if labels else 'unbounded',
            'nearest_distance_squared': minimum,
            'tied_nearest_ids': sorted(row['feature']['id'] for row in closest),
            'class_signature': signature,
            'locate': {
                'features': compact_features(owners),
                'visit': locate['visit'],
            },
            'nearest': {
                'neighbors': [
                    {'feature': compact_features([row['feature']])[0],
                     'distance_squared': row['distance_squared']}
                    for row in neighbors
                ],
                'visit': nearest['visit'],
            },
            'revision': locate['revision'],
            'layer_revision': locate['layer_revision'],
        })
    return evidence, duplicates, revisions


def compact_features(features):
    rows = []
    for feature in features:
        shape = feature['shape']
        if 'mark' in shape:
            geometry = {'mark': shape['mark']}
        else:
            geometry = {'extent': shape['extent']}
        rows.append({'id': feature['id'], 'class': feature['class'],
                     'shape': geometry})
    return rows


def classify(owners, neighbors):
    labels = []
    if not neighbors:
        return labels
    minimum = neighbors[0]['distance_squared']
    tied = [row for row in neighbors if row['distance_squared'] == minimum]
    if not owners and minimum == 0:
        labels.append('edge_zero_unowned')
    if len(tied) >= 2:
        labels.append('tie')
    if len(tied) == 1:
        labels.append('unique')
    return labels


# --------------------------------------------------------------------------
# Deterministic selection
# --------------------------------------------------------------------------

def quotas_for(total):
    base, remainder = divmod(total, len(GROUPS))
    return {group: base + (1 if index < remainder else 0)
            for index, group in enumerate(GROUPS)}


def select_sample(evidence, total):
    quotas = quotas_for(total)
    pools = {group: [item['probe_id'] for item in evidence
                     if item['primary_group'] == group]
             for group in GROUPS}
    by_id = {item['probe_id']: item for item in evidence}
    selected, group_report = [], []

    for group in GROUPS:
        picked, covered = [], set()
        candidates = sorted(pools[group])
        while len(picked) < quotas[group] and candidates:
            # Maximize newly covered classes; probe_id is the fixed tie-break.
            best = max(
                candidates,
                key=lambda pid: (
                    len(set(by_id[pid]['class_signature']) - covered),
                    _negative_id(pid),
                ),
            )
            candidates.remove(best)
            picked.append(best)
            covered.update(by_id[best]['class_signature'])
            selected.append(best)
        group_report.append({
            'group': group,
            'quota': quotas[group],
            'pool_size': len(pools[group]),
            'picked_probe_ids': picked,
            'covered_classes': sorted(covered),
            'shortfall': max(0, quotas[group] - len(picked)),
        })

    if len(selected) < total:
        chosen = set(selected)
        for pid in sorted(by_id):
            if len(selected) >= total:
                break
            if pid not in chosen:
                selected.append(pid)
                chosen.add(pid)

    ordered = []
    for group in GROUPS:
        ordered.extend(pid for pid in selected
                       if by_id[pid]['primary_group'] == group)
    return quotas, group_report, ordered


def _negative_id(pid):
    # Negate the lexical key so the smallest ID wins the max() tie-break.
    return tuple(-ord(char) for char in pid)


# --------------------------------------------------------------------------
# Pack writing
# --------------------------------------------------------------------------

def write_json(path, payload):
    with open(path, 'w', encoding='utf-8') as handle:
        json.dump(payload, handle, indent=2, sort_keys=True, ensure_ascii=False)
        handle.write('\n')


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as handle:
        for chunk in iter(lambda: handle.read(65536), b''):
            digest.update(chunk)
    return digest.hexdigest()


def build_pack(client, candidates_path, layer_override, total, out_dir):
    loaded = load_candidates(candidates_path, layer_override)
    layer = loaded['layer']
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)

    evidence, duplicates, revisions = gather_evidence(
        client, layer, loaded['records'])
    if not evidence:
        raise SystemExit(
            'no valid, in-range, distinct probe coordinates remain after '
            'validation; see the candidate file and shortfall report')
    quotas, group_report, ordered = select_sample(evidence, total)

    unbounded = [item['probe_id'] for item in evidence
                 if item['primary_group'] == 'unbounded']
    shortfalls = {
        'total_requested': total,
        'total_selected': len(ordered),
        'overall_shortfall': max(0, total - len(ordered)),
        'invalid_candidates': loaded['invalid'],
        'duplicate_coordinates': duplicates,
        'unbounded_probes': unbounded,
        'group_shortfalls': [
            {'group': row['group'], 'missing': row['shortfall'],
             'quota': row['quota'], 'pool_size': row['pool_size']}
            for row in group_report if row['shortfall']
        ],
    }
    raw_payload = {
        'layer': layer,
        'rule_version': RULE_VERSION,
        'source_sha256': loaded['source_sha256'],
        'probes': loaded['records'],
    }
    evidence_payload = {
        'layer': layer,
        'rule_version': RULE_VERSION,
        'revision': revisions.get('revision'),
        'layer_revision': revisions.get('layer_revision'),
        'nearest_limit': NEAREST_LIMIT,
        'coordinates': evidence,
    }
    selection_payload = {
        'layer': layer,
        'rule_version': RULE_VERSION,
        'total_requested': total,
        'group_quotas': quotas,
        'groups': group_report,
        'selected_probe_ids': ordered,
        'selection_rule': (
            'per group: maximize newly covered feature classes, tie-break by '
            'probe_id; remaining quota filled in global probe_id order'
        ),
    }

    paths = {name: out / name for name in OUTPUT_FILES}
    write_json(paths[RAW_FILE], raw_payload)
    write_json(paths[EVIDENCE_FILE], evidence_payload)
    write_json(paths[SELECTION_FILE], selection_payload)
    write_json(paths[SHORTFALL_FILE], shortfalls)

    layer_summary = next((row for row in client.strata()['layers']
                          if row['name'] == layer), None)
    manifest = {
        'rule_version': RULE_VERSION,
        'layer': layer,
        'candidate_source_sha256': loaded['source_sha256'],
        'revision': revisions.get('revision'),
        'layer_revision': revisions.get('layer_revision'),
        'service_status': client.status(),
        'layer_summary': layer_summary,
        'counts': {
            'candidate_records': len(loaded['records']),
            'valid_unique_coordinates': len(evidence),
            'invalid_records': len(loaded['invalid']),
            'duplicate_records': len(duplicates),
            'selected': len(ordered),
            'requested': total,
        },
        'output_sha256': {name: sha256_file(paths[name])
                          for name in OUTPUT_FILES},
    }
    write_json(out / MANIFEST_FILE, manifest)
    return manifest, shortfalls, selection_payload


# --------------------------------------------------------------------------
# Verification against the same (or an updated) database
# --------------------------------------------------------------------------

def verify_pack(client, pack_dir, layer_override):
    pack = Path(pack_dir)
    manifest = json.loads((pack / MANIFEST_FILE).read_text())
    mismatches, checks = [], []

    for name in OUTPUT_FILES:
        actual = sha256_file(pack / name)
        expected = manifest['output_sha256'][name]
        checks.append(f'hash:{name}')
        if actual != expected:
            mismatches.append(f'{name}: sha256 drift {expected} -> {actual}')

    layer = layer_override or manifest['layer']
    stored_evidence = json.loads((pack / EVIDENCE_FILE).read_text())
    live_evidence, _, _ = gather_evidence(
        client, layer, [
            {'probe_id': item['probe_id'], 'valid': True,
             'x': item['coordinate']['x'], 'y': item['coordinate']['y']}
            for item in stored_evidence['coordinates']
        ])
    stored_by_id = {item['probe_id']: item
                    for item in stored_evidence['coordinates']}
    for item in live_evidence:
        previous = stored_by_id.get(item['probe_id'])
        checks.append(f'evidence:{item["probe_id"]}')
        if previous is None:
            mismatches.append(f'{item["probe_id"]}: missing from stored evidence')
            continue
        for field in ('coordinate', 'labels', 'primary_group',
                      'class_signature', 'locate', 'nearest'):
            if previous[field] != item[field]:
                mismatches.append(f'{item["probe_id"]}: {field} changed')

    status = client.status()
    if status['revision'] != manifest['revision']:
        mismatches.append(
            f'revision drift: pack r{manifest["revision"]} -> '
            f'live r{status["revision"]}')

    selection = json.loads((pack / SELECTION_FILE).read_text())
    quotas, group_report, ordered = select_sample(
        stored_evidence['coordinates'], selection['total_requested'])
    checks.append('selection-rule')
    if ordered != selection['selected_probe_ids']:
        mismatches.append('selection rule no longer reproduces the same sample')
    if group_report != selection['groups']:
        mismatches.append('group quotas or coverage no longer reproduce')

    verdict = {
        'rule_version': RULE_VERSION,
        'layer': layer,
        'pack_revision': manifest['revision'],
        'live_revision': status['revision'],
        'checks_performed': sorted(set(checks)),
        'mismatches': mismatches,
        'ok': not mismatches,
    }
    write_json(pack / VERDICT_FILE, verdict)
    return verdict


# --------------------------------------------------------------------------
# Demo: build, seed a scratch database, generate and verify in one process
# --------------------------------------------------------------------------

def seed_scenario(client, scenario):
    for layer in scenario['layers']:
        client.create_layer({
            'name': layer['name'], 'kind': layer['kind'],
            'bounds': scenario['bounds'],
            'exclusive': layer['exclusive'],
        })
        edits = []
        for feature in layer['features']:
            shape_key = 'extent' if 'extent' in feature else 'mark'
            edits.append({
                'layer': layer['name'], 'id': feature['id'], 'mode': 'insert',
                'value': {'id': feature['id'], 'class': feature['class'],
                          'shape': {shape_key: feature[shape_key]}},
            })
        for start in range(0, len(edits), 500):
            client.edit(edits[start:start + 500])


def run_demo(args):
    repo_root = Path(args.root or Path(__file__).resolve().parents[1])
    go_binary = os.environ.get('GO_BIN') or 'go'
    with tempfile.TemporaryDirectory(prefix='meridian-patrol-') as temporary:
        root = Path(temporary)
        binary, data = root / 'meridian', root / 'space'
        subprocess.run([go_binary, 'build', '-o', str(binary), './cmd/meridian'],
                       check=True, cwd=repo_root, timeout=120)
        child = subprocess.Popen(
            [str(binary), '-listen', '127.0.0.1:0', '-data', str(data)],
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            readable, _, _ = select.select([child.stderr], [], [], 15)
            if not readable:
                raise RuntimeError('service did not start within 15 seconds')
            startup = json.loads(child.stderr.readline())
            if 'address' not in startup:
                raise RuntimeError(startup)
            client = Client('http://' + startup['address'])
            with open(args.scenario, 'rb') as handle:
                scenario = json.loads(handle.read().decode())
            seed_scenario(client, scenario)
            manifest, shortfalls, selection = build_pack(
                client, args.candidates, args.layer, args.total, args.out)
            verdict = verify_pack(client, args.out, args.layer)
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.communicate(timeout=5)
    return manifest, shortfalls, selection, verdict


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)

    gen = sub.add_parser('generate', help='build a patrol pack')
    gen.add_argument('--base-url', required=True)
    gen.add_argument('--candidates', required=True)
    gen.add_argument('--layer')
    gen.add_argument('--total', type=int, required=True)
    gen.add_argument('--out', required=True)

    check = sub.add_parser('verify', help='re-check a patrol pack')
    check.add_argument('--base-url', required=True)
    check.add_argument('--pack', required=True)
    check.add_argument('--layer')

    demo = sub.add_parser('demo', help='scratch database end-to-end run')
    demo.add_argument('--candidates', required=True)
    demo.add_argument('--scenario', required=True)
    demo.add_argument('--total', type=int, required=True)
    demo.add_argument('--out', required=True)
    demo.add_argument('--layer')
    demo.add_argument('--root', help='repository root containing cmd/meridian')

    seed = sub.add_parser('seed', help='load a scenario file into a service')
    seed.add_argument('--base-url', required=True)
    seed.add_argument('--scenario', required=True)

    args = parser.parse_args()
    if args.command == 'generate':
        if args.total < 1:
            raise SystemExit('--total must be positive')
        client = Client(args.base_url)
        manifest, shortfalls, _ = build_pack(
            client, args.candidates, args.layer, args.total, args.out)
        print(json.dumps({'manifest': manifest, 'shortfalls': shortfalls},
                         indent=2, sort_keys=True))
    elif args.command == 'verify':
        verdict = verify_pack(Client(args.base_url), args.pack, args.layer)
        print(json.dumps(verdict, indent=2, sort_keys=True))
        if not verdict['ok']:
            sys.exit(1)
    elif args.command == 'seed':
        with open(args.scenario, 'rb') as handle:
            scenario = json.loads(handle.read().decode())
        seed_scenario(Client(args.base_url), scenario)
        print(json.dumps(Client(args.base_url).status(), sort_keys=True))
    else:
        manifest, shortfalls, _, verdict = run_demo(args)
        print(json.dumps({'manifest': manifest, 'shortfalls': shortfalls,
                          'verdict': verdict}, indent=2, sort_keys=True))
        if not verdict['ok']:
            sys.exit(1)


if __name__ == '__main__':
    main()
