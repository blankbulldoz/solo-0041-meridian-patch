#!/usr/bin/env python3
"""Right-class occupancy ledger for two extent layers in one query window.

For every left-layer rectangle the occupied area (left rectangle clipped to the
window) is partitioned by the set of right-layer *classes* covering each point:

  exclusive   exactly one distinct right class covers the point
  multi       two or more distinct right classes cover the point
  uncovered   no right rectangle covers the point

Overlaps between features of the same right class never raise the class count.
The window is split whenever a /join request would exceed its pair budget, and
the per-left class decomposition is always rebuilt as a union of clipped
intersections, so a split seam cannot duplicate area.

The script builds a self-made dataset, drives the production HTTP API, checks
the result against an independent geometry computation, and writes CSV detail,
CSV summary and JSON metadata into an output directory.
"""
import argparse
import bisect
import csv
import hashlib
import json
import os
import select
import shutil
import subprocess
import sys
import tempfile
from collections import Counter, defaultdict
from contextlib import contextmanager
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
SCHEMA_VERSION = 1
SCRIPT_VERSION = 1

LEFT = 'left-lattice'
RIGHT = 'right-lattice'
LAYER_BOUNDS = (-10, -10, 4100, 200)
WINDOW = (0, 0, 4000, 100)
PAIR_LIMIT = 2000
MAX_PAIR_LIMIT = 2000
PAGE = 256
MAX_EDITS = 512
EMPTY = frozenset()

# Filler geometry: right tile j is [2j,2j+2) x [0,20); left tile i is
# [2i+1,2i+3) x [0,20). Every left tile overlaps right tiles i and i+1, both of
# class "filler", so the tile is exclusively covered by exactly one class even
# though two same-class right features overlap inside it. 1998 left tiles give
# 3996 filler pairs, enough to exceed the 2000-pair /join budget at the root.
LEFT_TILES = 1998
RIGHT_TILES = 2000

PROBES = [
    # left id, left class, left extent, [(right id, right class, right extent), ...]
    ('probe-alpha', 'probe', (10, 30, 50, 60),
     [('right-alpha', 'alpha', (10, 30, 30, 60))]),
    ('probe-same', 'probe', (60, 30, 120, 60),
     [('right-beta-one', 'beta', (60, 30, 100, 60)),
      ('right-beta-two', 'beta', (80, 30, 120, 60))]),
    ('probe-multi', 'probe', (130, 30, 200, 60),
     [('right-gamma-one', 'gamma', (130, 30, 170, 60)),
      ('right-delta-one', 'delta', (150, 30, 200, 60))]),
    ('probe-none', 'probe', (210, 30, 250, 60), []),
    ('probe-triple', 'probe', (260, 30, 320, 60),
     [('right-epsilon', 'epsilon', (260, 30, 300, 60)),
      ('right-zeta', 'zeta', (280, 30, 320, 60)),
      ('right-eta', 'eta', (290, 30, 310, 60))]),
    ('probe-mixed', 'probe', (330, 30, 400, 60),
     [('right-gamma-two', 'gamma', (330, 30, 370, 60)),
      ('right-gamma-three', 'gamma', (350, 30, 390, 60)),
      ('right-delta-two', 'delta', (380, 30, 400, 60))]),
    ('probe-edge', 'probe', (410, 30, 450, 60),
     [('right-edge', 'beta', (450, 30, 470, 60))]),
    ('probe-seam', 'probe', (1980, 35, 2020, 55),
     [('right-seam-red', 'red', (1980, 35, 2005, 55)),
      ('right-seam-blue', 'blue', (1998, 35, 2020, 55))]),
]
SPAN_TILE = ('right-span', 'filler', (1990, 0, 2010, 20))


# ---------------------------------------------------------------------------
# Integer rectangles. Boxes are half-open [min_x,max_x) x [min_y,max_y).
# ---------------------------------------------------------------------------

def box_json(box):
    return dict(min_x=box[0], min_y=box[1], max_x=box[2], max_y=box[3])


def box_of(raw):
    return (raw['min_x'], raw['min_y'], raw['max_x'], raw['max_y'])


def area(box):
    return (box[2] - box[0]) * (box[3] - box[1])


def clip(a, b):
    out = (max(a[0], b[0]), max(a[1], b[1]), min(a[2], b[2]), min(a[3], b[3]))
    return out if out[2] > out[0] and out[3] > out[1] else None


def intersects(a, b):
    return a[0] < b[2] and b[0] < a[2] and a[1] < b[3] and b[1] < a[3]


def sweep(bounds, boxes):
    """Area inside bounds per distinct-class set.

    boxes is [(box, class)]; every box must lie inside bounds. The result maps
    frozenset(class) -> area, including the empty set for uncovered subareas.
    Same-class multiplicity collapses because only the set of class names is
    accumulated.
    """
    x0, y0, x1, y1 = bounds
    xs = {x0, x1}
    for b, _ in boxes:
        xs.add(b[0])
        xs.add(b[2])
    xs = sorted(xs)
    out = defaultdict(int)
    for i in range(len(xs) - 1):
        sx0, sx1 = xs[i], xs[i + 1]
        active = [(b, c) for b, c in boxes if b[0] <= sx0 and b[2] >= sx1]
        if not active:
            out[EMPTY] += (sx1 - sx0) * (y1 - y0)
            continue
        ys = sorted({v for b, _ in active for v in (b[1], b[3])})
        for j in range(len(ys) - 1):
            sy0, sy1 = ys[j], ys[j + 1]
            classes = frozenset(c for b, c in active if b[1] <= sy0 and b[3] >= sy1)
            out[classes] += (sx1 - sx0) * (sy1 - sy0)
    return dict(out)


# ---------------------------------------------------------------------------
# HTTP client
# ---------------------------------------------------------------------------

class Session:
    def __init__(self, url):
        self.url = url

    def call(self, method, path, value=None, headers=None):
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            args['Content-Type'] = 'application/json'
        request = Request(self.url + PREFIX + path, data=value, method=method, headers=args)
        try:
            response = urlopen(request, timeout=30)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            if response.headers.get_content_type() == 'application/json':
                body = json.loads(raw)
            else:
                body = raw
            return response.status, body, response.headers

    def ok(self, method, path, value=None, expected=200):
        status, body, headers = self.call(method, path, value)
        if status != expected:
            raise RuntimeError(f'{method} {path} returned {status}: {body!r}')
        return body, headers

    def join(self, bounds, limit):
        return self.call('POST', '/join', dict(left=LEFT, right=RIGHT,
                                               bounds=box_json(bounds), limit=limit))


@contextmanager
def running(binary, data):
    child = subprocess.Popen([str(binary), '-listen', '127.0.0.1:0', '-data', str(data)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        readable, _, _ = select.select([child.stderr], [], [], 20)
        if not readable:
            raise RuntimeError('service did not start within 20 seconds')
        startup = json.loads(child.stderr.readline())
        if 'address' not in startup:
            raise RuntimeError(startup)
        session = Session('http://' + startup['address'])
        status, body, _ = session.call('GET', '/status')
        if status != 200 or body['revision'] != startup['revision']:
            raise RuntimeError(f'unexpected startup status: {status} {body!r}')
        yield session
    finally:
        if child.poll() is None:
            child.terminate()
        try:
            child.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            child.kill()
            child.communicate(timeout=10)


# ---------------------------------------------------------------------------
# Dataset
# ---------------------------------------------------------------------------

def dataset():
    left = [(f'l{i:04d}', 'tile', (2 * i + 1, 0, 2 * i + 3, 20)) for i in range(LEFT_TILES)]
    right = [(f'r{j:04d}', 'filler', (2 * j, 0, 2 * j + 2, 20)) for j in range(RIGHT_TILES)]
    right.append(SPAN_TILE)
    for left_id, left_class, extent, neighbours in PROBES:
        left.append((left_id, left_class, extent))
        right.extend(neighbours)
    return left, right


def feature_value(feature_id, feature_class, extent):
    return dict(id=feature_id, **{'class': feature_class},
                shape={'extent': box_json(extent)})


def batches(items, size):
    for start in range(0, len(items), size):
        yield items[start:start + size]


def seed(session):
    for name in (LEFT, RIGHT):
        session.ok('POST', '/strata', {'name': name, 'kind': 'extent',
                                       'bounds': box_json(LAYER_BOUNDS), 'exclusive': False},
                   expected=201)
    left, right = dataset()
    for layer, rows in ((LEFT, left), (RIGHT, right)):
        for group in batches(rows, MAX_EDITS):
            edits = [dict(layer=layer, id=row[0], mode='insert',
                          value=feature_value(row[0], row[1], row[2])) for row in group]
            session.ok('POST', '/edits', dict(edits=edits))
    _, status, _ = session.call('GET', '/status')
    summaries, _ = session.ok('GET', '/strata')
    revisions = {summary['name']: summary['revision'] for summary in summaries['layers']}
    return status['revision'], revisions[LEFT], revisions[RIGHT], left, right

def fetch_window(session, layer, bounds):
    rows = []
    bookmark = None
    stamp = None
    pages = 0
    while True:
        request = dict(bounds=box_json(bounds), limit=PAGE)
        if bookmark is not None:
            request['bookmark'] = bookmark
        body, _ = session.ok('POST', f'/strata/{layer}/window', request)
        pages += 1
        current = (body['revision'], body['layer_revision'])
        if stamp is None:
            stamp = current
        elif current != stamp:
            raise RuntimeError(f'{layer} window revision changed during paging: {stamp} -> {current}')
        rows.extend(body['features'])
        bookmark = body.get('next')
        if bookmark is None:
            return rows, stamp, pages


# ---------------------------------------------------------------------------
# Pair collection with window splitting
# ---------------------------------------------------------------------------

def split_box(bounds):
    x0, y0, x1, y1 = bounds
    width, height = x1 - x0, y1 - y0
    if width >= height and width >= 2:
        mid = x0 + width // 2
        return (x0, y0, mid, y1), (mid, y0, x1, y1)
    if height >= 2:
        mid = y0 + height // 2
        return (x0, y0, x1, mid), (x0, mid, x1, y1)
    raise RuntimeError(f'join budget exhausted in unsplittable window {bounds}')


def collect_pairs(session, pair_limit):
    occurrences = []
    leaves = []
    splits = []
    visits = []
    revisions = set()

    def walk(bounds, depth):
        status, body, _ = session.join(bounds, pair_limit)
        if status == 200:
            revisions.add((body['revision'], body['left_revision'], body['right_revision']))
            leaves.append(dict(bounds=list(bounds), pairs=len(body['pairs']),
                               nodes=body['visit']['nodes'], candidates=body['visit']['candidates']))
            visits.append(body['visit'])
            for pair in body['pairs']:
                if pair.get('intersection') is None:
                    raise RuntimeError(f'join pair without a positive-area intersection: {pair!r}')
                occurrences.append(dict(leaf=bounds, left=pair['left'], right=pair['right'],
                                        box=box_of(pair['intersection'])))
            return
        if status == 422 and isinstance(body, dict) and body.get('code') == 'capacity':
            if depth >= 32:
                raise RuntimeError(f'join splitting exceeded depth at {bounds}')
            first, second = split_box(bounds)
            splits.append(dict(bounds=list(bounds), axis='x' if first[2] != bounds[2] else 'y',
                               at=first[2] if first[2] != bounds[2] else first[3],
                               depth=depth, reason=body.get('message', 'capacity')))
            walk(first, depth + 1)
            walk(second, depth + 1)
            return
        raise RuntimeError(f'join failed at {bounds}: {status} {body!r}')

    walk(WINDOW, 0)
    return occurrences, leaves, splits, visits, revisions


def sha256_bytes(raw):
    return hashlib.sha256(raw).hexdigest()


def decode_image(raw, expected_revision):
    """Decode the MPB v1 envelope far enough to pin the layer revisions."""
    if len(raw) < 44 or raw[:8] != b'MRDNMPB\x01':
        raise RuntimeError('image signature is not MPB v1')
    payload = raw[44:]
    if int.from_bytes(raw[8:12], 'big') != len(payload):
        raise RuntimeError('image payload length mismatch')
    if sha256_bytes(payload) != raw[12:44].hex():
        raise RuntimeError('image checksum mismatch')
    image = json.loads(payload)
    if image.get('revision') != expected_revision:
        raise RuntimeError('image payload revision differs from the header')
    return {layer['definition']['name']: layer for layer in image['layers']}


# ---------------------------------------------------------------------------
# Analysis
# ---------------------------------------------------------------------------

def analyse(session, pair_limit):
    database_revision, left_revision, right_revision, seeded_left, seeded_right = seed(session)
    left_rows, left_stamp, left_pages = fetch_window(session, LEFT, WINDOW)
    right_rows, right_stamp, right_pages = fetch_window(session, RIGHT, WINDOW)

    if left_stamp != (database_revision, left_revision):
        raise RuntimeError(f'left window stamp {left_stamp} != seeded '
                           f'{(database_revision, left_revision)}')
    if right_stamp != (database_revision, right_revision):
        raise RuntimeError(f'right window stamp {right_stamp} != seeded '
                           f'{(database_revision, right_revision)}')

    left = {}
    for row in left_rows:
        if 'extent' not in row['shape']:
            raise RuntimeError(f"{LEFT} feature {row['id']} is not an extent")
        if row['id'] in left:
            raise RuntimeError(f'duplicate left feature {row["id"]}')
        left[row['id']] = (row['class'], box_of(row['shape']['extent']))
    right = {}
    for row in right_rows:
        if 'extent' not in row['shape']:
            raise RuntimeError(f"{RIGHT} feature {row['id']} is not an extent")
        if row['id'] in right:
            raise RuntimeError(f'duplicate right feature {row["id"]}')
        right[row['id']] = (row['class'], box_of(row['shape']['extent']))

    occurrences, leaves, splits, visits, join_stamps = collect_pairs(session, pair_limit)
    if join_stamps != {(database_revision, left_revision, right_revision)}:
        raise RuntimeError(f'join revisions {join_stamps} differ from '
                           f'{(database_revision, left_revision, right_revision)}')

    status, image, headers = session.call('GET', '/image')
    if status != 200 or headers.get_content_type() != 'application/vnd.meridian.image':
        raise RuntimeError(f'image export failed: {status}')
    if headers.get('Meridian-Revision') != str(database_revision):
        raise RuntimeError('image revision differs from the analysed revision')
    if len(image) != int(headers['Content-Length']):
        raise RuntimeError('image length differs from Content-Length')
    image_layers = decode_image(image, database_revision)
    if (image_layers[LEFT]['revision'], image_layers[RIGHT]['revision']) != (left_revision, right_revision):
        raise RuntimeError(f'image layer revisions {image_layers[LEFT]["revision"]}/'
                           f'{image_layers[RIGHT]["revision"]} differ from '
                           f'{left_revision}/{right_revision}')
    for name, listing in ((LEFT, left), (RIGHT, right)):
        exported = {row['id']: row for row in image_layers[name]['features']}
        for feature_id, (feature_class, extent) in listing.items():
            row = exported.get(feature_id)
            if row is None or row['class'] != feature_class or \
                    box_of(row['shape']['extent']) != extent:
                raise RuntimeError(f'image feature {name}/{feature_id} differs from window listing')

    # Join-derived class-set boxes per left feature. Every clipped pair
    # intersection is a piece of left ∩ right ∩ window; taking the union via
    # the sweep removes seam duplicates and same-class multiplicity.
    joined = defaultdict(list)
    pair_counts = Counter()
    for item in occurrences:
        if item['left'] not in left:
            raise RuntimeError(f"join left {item['left']} is not in the left window listing")
        if item['right'] not in right:
            raise RuntimeError(f"join right {item['right']} is not in the right window listing")
        left_class, left_box = left[item['left']]
        right_class, right_box = right[item['right']]
        if clip(item['box'], left_box) != item['box']:
            raise RuntimeError(f"pair box {item['box']} is not inside left {item['left']}")
        if clip(item['box'], right_box) != item['box']:
            raise RuntimeError(f"pair box {item['box']} is not inside right {item['right']}")
        joined[item['left']].append((item['box'], right_class))
        pair_counts[(item['left'], item['right'])] += 1

    # Independent ground truth from the window listings, with no reliance on
    # join paging or splitting.
    right_clipped = []
    for right_id, (right_class, right_box) in right.items():
        piece = clip(right_box, WINDOW)
        if piece is not None:
            right_clipped.append((piece, right_class, right_id))
    right_clipped.sort(key=lambda item: item[0][0])
    starts = [item[0][0] for item in right_clipped]

    truth = {}
    truth_pairs = {}
    for left_id, (left_class, left_box) in left.items():
        left_clip = clip(left_box, WINDOW)
        if left_clip is None:
            continue
        pieces = []
        expected = []
        stop = bisect.bisect_left(starts, left_clip[2])
        for piece, right_class, right_id in right_clipped[:stop]:
            if piece[2] <= left_clip[0] or not intersects(piece, left_clip):
                continue
            overlap = clip(piece, left_clip)
            if overlap is None:
                continue
            pieces.append((overlap, right_class))
            expected.append((right_id, overlap))
        truth[left_id] = (left_class, left_clip, sweep(left_clip, pieces))
        truth_pairs[left_id] = expected

    # Ground truth pair occurrences: a pair's true intersection is clipped by
    # every leaf window it crosses, exactly like /join returns it.
    expected_counts = Counter()
    for left_id, expected in truth_pairs.items():
        for right_id, overlap in expected:
            expected_counts[(left_id, right_id)] = sum(
                1 for leaf in leaves if clip(overlap, tuple(leaf['bounds'])) is not None)
    if expected_counts != pair_counts:
        missing = expected_counts - pair_counts
        extra = pair_counts - expected_counts
        raise RuntimeError(f'pair occurrence mismatch; missing={dict(missing)!r} extra={dict(extra)!r}')

    # Per-left feature comparison of the API-derived decomposition with the
    # independent listing-derived decomposition.
    mismatches = []
    for left_id, (left_class, left_clip, expected_areas) in truth.items():
        got = sweep(left_clip, joined.get(left_id, []))
        if got != expected_areas:
            mismatches.append(dict(left=left_id, expected=dict(sorted(
                ([list(k), v] for k, v in expected_areas.items()), key=lambda kv: kv[0])),
                got=dict(sorted(([list(k), v] for k, v in got.items()), key=lambda kv: kv[0]))))
    if mismatches:
        raise RuntimeError(f'join decomposition diverges from geometry: {mismatches[:3]!r}')

    # Duplicate pairs across split seams: every occurrence must be a disjoint
    # clip of the true intersection and their union must equal it.
    duplicate_pairs = []
    for (left_id, right_id), count in sorted(pair_counts.items()):
        if count == 1:
            continue
        pieces = [item['box'] for item in occurrences
                  if item['left'] == left_id and item['right'] == right_id]
        true_overlap = dict(truth_pairs[left_id])[right_id]
        union = sweep(true_overlap, [(piece, 'same') for piece in pieces])
        union_area = sum(union.values())
        overlaps = 0
        for i in range(len(pieces)):
            for j in range(i + 1, len(pieces)):
                both = clip(pieces[i], pieces[j])
                if both is not None:
                    overlaps += area(both)
        duplicate_pairs.append(dict(left=left_id, right=right_id, occurrences=count,
                                    union_area=union_area,
                                    ground_truth_area=area(true_overlap),
                                    intra_pair_overlap_area=overlaps))
        if union_area != area(true_overlap) or overlaps != 0:
            raise RuntimeError(f'seam duplicate for {left_id}/{right_id} double counts area')

    detail_rows = []
    class_set_rows = []
    class_set_totals = defaultdict(int)
    exclusive_totals = defaultdict(int)
    uncovered_total = 0
    total_left_area = 0
    for left_id in sorted(truth):
        left_class, left_clip, areas = truth[left_id]
        exclusive_classes = set()
        exclusive_area = 0
        multi_area = 0
        uncovered_area = 0
        classes = set()
        for class_set, value in sorted(areas.items(), key=lambda item: (len(item[0]), sorted(item[0]))):
            if value == 0:
                continue
            class_set_rows.append(dict(left_id=left_id, left_class=left_class,
                                       category=('uncovered' if not class_set else
                                                 'exclusive' if len(class_set) == 1 else 'multi'),
                                       class_set='|'.join(sorted(class_set)),
                                       class_count=len(class_set), area=value))
            class_set_totals[class_set] += value
            classes.update(class_set)
            if len(class_set) == 1:
                only = next(iter(class_set))
                exclusive_area += value
                exclusive_classes.add(only)
                exclusive_totals[only] += value
            elif len(class_set) >= 2:
                multi_area += value
            else:
                uncovered_area += value
        uncovered_total += uncovered_area
        left_area = area(left_clip)
        if exclusive_area + multi_area + uncovered_area != left_area:
            raise RuntimeError(f'partition does not close for {left_id}')
        total_left_area += left_area
        detail_rows.append(dict(
            left_id=left_id, left_class=left_class, left_area=left_area,
            exclusive_area=exclusive_area, multi_area=multi_area,
            uncovered_area=uncovered_area,
            exclusive_classes='|'.join(sorted(exclusive_classes)),
            classes='|'.join(sorted(classes)),
        ))
    multi_total = sum(value for class_set, value in class_set_totals.items() if len(class_set) >= 2)

    if sum(exclusive_totals.values()) + multi_total + uncovered_total != total_left_area:
        raise RuntimeError('summary does not reconcile with total left area')
    # Named semantic probes.
    def probe_areas(left_id):
        return {tuple(sorted(k)): v for k, v in truth[left_id][2].items() if v}

    same = probe_areas('probe-same')
    multi = probe_areas('probe-multi')
    none = probe_areas('probe-none')
    edge = probe_areas('probe-edge')
    seam = probe_areas('probe-seam')

    partition_ok = all(row['exclusive_area'] + row['multi_area'] + row['uncovered_area']
                       == row['left_area'] for row in detail_rows)
    totals_ok = (sum(exclusive_totals.values()) + multi_total + uncovered_total == total_left_area
                 and sum(row['area'] for row in class_set_rows) == total_left_area)
    checks = [
        dict(name='layer_revisions_stable', ok=True,
             detail=f'every read and join used database {database_revision}, '
                    f'left layer {left_revision}, right layer {right_revision}'),
        dict(name='join_required_window_split', ok=len(leaves) > 1,
             detail=f'{len(splits)} split(s) produced {len(leaves)} leaf window(s)'),
        dict(name='pair_occurrences_match_geometry', ok=expected_counts == pair_counts,
             detail=f'{sum(pair_counts.values())} clipped pair occurrences over '
                    f'{len(pair_counts)} unique pairs; recomputed from layer listings'),
        dict(name='seam_duplicates_union_exact', ok=bool(duplicate_pairs) and all(
            item['union_area'] == item['ground_truth_area'] and item['intra_pair_overlap_area'] == 0
            for item in duplicate_pairs),
             detail=f'{len(duplicate_pairs)} pair(s) reported in more than one leaf; '
                    'clipped occurrences are disjoint and their union equals the true intersection'),
        dict(name='same_class_overlap_is_one_class', ok=same == {('beta',): 1800},
             detail='two overlapping beta rectangles inside probe-same classify as beta only, '
                    'area 1800 of 1800'),
        dict(name='join_matches_independent_geometry', ok=not mismatches,
             detail=f'{len(truth)} per-left decompositions from /join equal the '
                    'listing-derived sweep'),
        dict(name='per_feature_partition_closes', ok=partition_ok,
             detail='exclusive + multi + uncovered == clipped left area for every feature'),
        dict(name='summary_reconciles', ok=totals_ok,
             detail=f'{total_left_area} total left area partitioned by detail and class-set rows'),
    ]
    failed = [check['name'] for check in checks if not check['ok']]
    if failed:
        raise RuntimeError(f'checks failed: {failed!r}')
    if multi != {('delta',): 900, ('gamma',): 600, ('delta', 'gamma'): 600}:
        raise RuntimeError(f'multi-class probe is wrong: {multi!r}')
    if none != {(): 1200}:
        raise RuntimeError(f'uncovered probe is wrong: {none!r}')
    if edge != {(): 1200}:
        raise RuntimeError(f'edge-touch probe is wrong: {edge!r}')
    if seam != {('red',): 360, ('blue',): 300, ('blue', 'red'): 140}:
        raise RuntimeError(f'seam probe is wrong: {seam!r}')
    if not duplicate_pairs or len(leaves) < 2:
        raise RuntimeError('expected the pair budget to force a split that clips a shared pair')

    metadata = dict(
        schema_version=SCHEMA_VERSION,
        generator=dict(script='examples/occupancy.py', version=SCRIPT_VERSION,
                       method='production HTTP API; window listing plus budget-split /join'),
        window=box_json(WINDOW),
        window_area=area(WINDOW),
        pair_limit=pair_limit,
        database_revision=database_revision,
        layers=dict(
            left=dict(name=LEFT, kind='extent', revision=left_revision,
                      features_in_window=len(left), features_seeded=len(seeded_left),
                      bounds=box_json(LAYER_BOUNDS)),
            right=dict(name=RIGHT, kind='extent', revision=right_revision,
                       features_in_window=len(right), features_seeded=len(seeded_right),
                       bounds=box_json(LAYER_BOUNDS)),
        ),
        dataset=dict(
            left_tiles=LEFT_TILES, right_tiles=RIGHT_TILES,
            tile_geometry=dict(left='[2i+1,2i+3) x [0,20)', right='[2j,2j+2) x [0,20)',
                               right_class='filler', left_class='tile'),
            span_tile=dict(id=SPAN_TILE[0], **{'class': SPAN_TILE[1]},
                           extent=box_json(SPAN_TILE[2])),
            probes=[dict(left=left_id, left_class=left_class, extent=box_json(extent),
                         rights=[dict(id=right_id, **{'class': right_class},
                                      extent=box_json(right_extent))
                                 for right_id, right_class, right_extent in neighbours])
                    for left_id, left_class, extent, neighbours in PROBES],
        ),
        queries=dict(
            window_pages=dict(left=left_pages, right=right_pages),
            join_queries=len(leaves) + len(splits),
            join_leaves=leaves,
            join_splits=splits,
            pair_occurrences=sum(pair_counts.values()),
            unique_pairs=len(pair_counts),
            duplicate_pairs=[dict(item) for item in duplicate_pairs],
            visit=dict(nodes=sum(v['nodes'] for v in visits),
                       candidates=sum(v['candidates'] for v in visits)),
        ),
        image=dict(media_type='application/vnd.meridian.image', revision=database_revision,
                   bytes=len(image), sha256=sha256_bytes(image),
                   path='meridian-revision-%d.mpb' % database_revision),
        semantics=dict(
            exclusive='area covered by right rectangles of exactly one distinct class',
            multi='area covered by right rectangles of two or more distinct classes',
            uncovered='area covered by no right rectangle',
            same_class='overlapping right rectangles of one class keep class count 1',
            seams='class areas are unions of clipped intersections, never sums of leaf fragments',
        ),
        checks=checks,
        totals=dict(
            left_features=len(truth),
            left_area=total_left_area,
            exclusive_area=sum(exclusive_totals.values()),
            multi_area=multi_total,
            uncovered_area=uncovered_total,
            right_classes=sorted({cls for cls, _ in right.values()}),
        ),
    )
    return metadata, detail_rows, class_set_rows, image, dict(
        exclusive_totals=dict(exclusive_totals),
        class_set_totals=dict(class_set_totals),
        multi_total=multi_total,
        uncovered_total=uncovered_total,
        total_left_area=total_left_area,
    )


# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

def sha256(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as handle:
        for chunk in iter(lambda: handle.read(1 << 16), b''):
            digest.update(chunk)
    return digest.hexdigest()


def write_csv(path, header, rows):
    with open(path, 'w', newline='', encoding='utf-8') as handle:
        writer = csv.writer(handle, lineterminator='\n')
        writer.writerow(header)
        for row in rows:
            writer.writerow(row)


README = """# Right-class occupancy ledger

Generated by `python3 examples/occupancy.py`; regenerate instead of editing.
Requires Go 1.22 for `cmd/meridian`. Every number below came from the production
HTTP API against a freshly seeded temporary database.

## Run

- Window `{window}` (area {window_area}); `/join` pair limit {pair_limit}.
- Database revision {revision}; `{left_name}` revision {left_revision} with {left_count}
  features in the window; `{right_name}` revision {right_revision} with {right_count}
  features in the window.
- Join needed {splits} split(s) from the {pair_limit}-pair budget, covering {leaves}
  leaf windows; {occurrences} clipped pair occurrences over {pairs} unique pairs;
  {duplicates} pair(s) straddled a split seam.
- Totals: exclusive {exclusive}, multi {multi}, uncovered {uncovered}; left area {left_area}.
- Snapshot `{image}` is the canonical MPB v1 image of revision {revision}.

## Semantics

A left feature's occupied area (its extent clipped to the window) is split by the
set of distinct **right classes** covering each point:

- `exclusive`: exactly one right class covers the point;
- `multi`: two or more distinct right classes cover the point;
- `uncovered`: no right rectangle covers the point.

Overlapping right features of the **same** class keep the class count at one, so
they never turn a point into `multi`. Seam splits do not duplicate area: each left
feature's class areas are rebuilt as the union of clipped pair intersections, and a
pair reported in two leaf windows contributes two disjoint fragments whose union
equals its true intersection.

## Files

| File | Content |
| --- | --- |
| `occupancy_detail.csv` | one row per left feature: `left_area`, `exclusive_area`, `multi_area`, `uncovered_area`, `exclusive_classes`, `classes` |
| `occupancy_class_sets.csv` | one row per (left feature, distinct right-class set): exact area, `class_count` (0 = uncovered, 1 = exclusive, >=2 = multi) |
| `occupancy_summary.csv` | exclusive per right class, multi per class combination, uncovered, and the all-features total |
| `metadata.json` | revisions, dataset recipe, split/leaf log, duplicate-pair audit, image hash, self-checks |
| `{image}` | MPB v1 image of revision {revision}, verified to hold the two analysed layers |

Every CSV row carries `revision`, `left_revision` and `right_revision`; reject
rows whose stamps do not match the analysis you want. Asserts made before writing:
`exclusive + multi + uncovered == left_area` per feature; class-set and summary
areas both sum to the total left area; `/join` results equal an independent sweep
over the two window listings.
"""


def feature_count(class_set_rows, category, key):
    """Distinct left features with a row of this category (and key, if given)."""
    return len({row['left_id'] for row in class_set_rows
                if row['category'] == category and (key is None or row['class_set'] == key)})


def emit(directory, metadata, detail_rows, class_set_rows, image, totals):
    directory.mkdir(parents=True, exist_ok=True)
    revision = metadata['database_revision']
    left = metadata['layers']['left']
    right = metadata['layers']['right']
    exclusive_totals = totals['exclusive_totals']
    class_set_totals = totals['class_set_totals']
    multi_total = totals['multi_total']
    uncovered_total = totals['uncovered_total']
    total_left_area = totals['total_left_area']
    if (sum(exclusive_totals.values()) + multi_total + uncovered_total != total_left_area
            or sum(row['area'] for row in class_set_rows) != total_left_area):
        raise RuntimeError('summary rows do not reconcile with total left area')

    detail = directory / 'occupancy_detail.csv'
    write_csv(detail,
              ['left_layer', 'left_id', 'left_class', 'left_area', 'exclusive_area',
               'multi_area', 'uncovered_area', 'exclusive_classes', 'classes',
               'revision', 'left_revision', 'right_revision', 'right_layer'],
              [[LEFT, row['left_id'], row['left_class'], row['left_area'],
                row['exclusive_area'], row['multi_area'], row['uncovered_area'],
                row['exclusive_classes'], row['classes'],
                revision, left['revision'], right['revision'], RIGHT]
               for row in detail_rows])

    sets = directory / 'occupancy_class_sets.csv'
    write_csv(sets,
              ['left_layer', 'left_id', 'left_class', 'category', 'class_set', 'class_count',
               'area', 'revision', 'left_revision', 'right_revision'],
              [[LEFT, row['left_id'], row['left_class'], row['category'], row['class_set'],
                row['class_count'], row['area'], revision, left['revision'], right['revision']]
               for row in class_set_rows])

    summary_rows = []
    for right_class in sorted(exclusive_totals):
        summary_rows.append(['exclusive', right_class, exclusive_totals[right_class],
                             feature_count(class_set_rows, 'exclusive', right_class)])
    summary_rows.append(['multi_total', '', multi_total,
                         feature_count(class_set_rows, 'multi', None)])
    for class_set, value in sorted(class_set_totals.items(), key=lambda item: (len(item[0]), sorted(item[0]))):
        if len(class_set) >= 2:
            summary_rows.append(['multi_combo', '|'.join(sorted(class_set)), value,
                                 feature_count(class_set_rows, 'multi', '|'.join(sorted(class_set)))])
    summary_rows.append(['uncovered', '', uncovered_total,
                         feature_count(class_set_rows, 'uncovered', None)])
    summary_rows.append(['all', '', total_left_area, len(detail_rows)])
    summary = directory / 'occupancy_summary.csv'
    write_csv(summary,
              ['category', 'right_class', 'area', 'feature_count',
               'revision', 'left_layer', 'left_revision', 'right_layer', 'right_revision'],
              [row + [revision, LEFT, left['revision'], RIGHT, right['revision']]
               for row in summary_rows])

    image_path = directory / metadata['image']['path']
    image_path.write_bytes(image)

    outputs = {}
    for path, kind in ((detail, 'csv'), (sets, 'csv'), (summary, 'csv'), (image_path, 'image')):
        entry = dict(sha256=sha256(path), bytes=path.stat().st_size)
        if kind == 'csv':
            with open(path, encoding='utf-8') as handle:
                entry['rows'] = sum(1 for _ in handle) - 1
        outputs[path.name] = entry
    metadata['outputs'] = outputs
    queries = metadata['queries']
    readme = directory / 'README.md'
    readme.write_text(README.format(
        window='[{min_x},{max_x}) x [{min_y},{max_y})'.format(**metadata['window']),
        window_area=metadata['window_area'],
        pair_limit=metadata['pair_limit'], revision=revision,
        left_name=left['name'], left_revision=left['revision'],
        left_count=left['features_in_window'],
        right_name=right['name'], right_revision=right['revision'],
        right_count=right['features_in_window'],
        splits=len(queries['join_splits']), leaves=len(queries['join_leaves']),
        occurrences=queries['pair_occurrences'], pairs=queries['unique_pairs'],
        duplicates=len(queries['duplicate_pairs']),
        exclusive=metadata['totals']['exclusive_area'], multi=multi_total,
        uncovered=uncovered_total, left_area=total_left_area,
        image=metadata['image']['path']), encoding='utf-8')
    meta = directory / 'metadata.json'
    with open(meta, 'w', encoding='utf-8') as handle:
        json.dump(metadata, handle, indent=2, sort_keys=True)
        handle.write('\n')
    return meta


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--output', default=str(ROOT / 'deliverables' / 'occupancy'),
                        help='directory for CSV/JSON deliverables')
    parser.add_argument('--go', default=os.environ.get('GO', 'go'),
                        help='Go launcher used to build ./cmd/meridian')
    parser.add_argument('--pair-limit', type=int, default=PAIR_LIMIT,
                        help=f'/join pair limit per request (1..{MAX_PAIR_LIMIT}); '
                             'the window is split whenever it is exceeded')
    parser.add_argument('--keep-data', action='store_true',
                        help='keep the temporary data directory and print its path')
    args = parser.parse_args()
    if not 1 <= args.pair_limit <= MAX_PAIR_LIMIT:
        parser.error(f'--pair-limit must be 1..{MAX_PAIR_LIMIT}')

    with tempfile.TemporaryDirectory(prefix='meridian-occupancy-') as temporary:
        root = Path(temporary)
        binary, data = root / 'meridian', root / 'space'
        build = subprocess.run([args.go, 'build', '-o', str(binary), './cmd/meridian'],
                               cwd=ROOT, capture_output=True, text=True, timeout=180)
        if build.returncode != 0:
            print(build.stderr, file=sys.stderr)
            raise SystemExit('failed to build cmd/meridian (use --go to point at a Go toolchain)')
        with running(binary, data) as session:
            result = analyse(session, args.pair_limit)
        metadata = result[0]
        path = emit(Path(args.output), *result)
        if args.keep_data:
            kept = Path(args.output) / 'seeded-data'
            if kept.exists():
                shutil.rmtree(kept)
            shutil.copytree(data, kept)
            metadata['seeded_data'] = str(kept.relative_to(Path(args.output)))
            with open(path, 'w', encoding='utf-8') as handle:
                json.dump(metadata, handle, indent=2, sort_keys=True)
                handle.write('\n')
    print(json.dumps(dict(output=str(Path(args.output)), revision=metadata['database_revision'],
                          left_revision=metadata['layers']['left']['revision'],
                          right_revision=metadata['layers']['right']['revision'],
                          pairs=metadata['queries']['unique_pairs'],
                          leaves=len(metadata['queries']['join_leaves']),
                          checks=[check['name'] for check in metadata['checks']]),
                     sort_keys=True))


if __name__ == '__main__':
    main()
