#!/usr/bin/env python3
"""Replay two preserved seam-query scenes against a persistent temporary database.

Scenes (seeded once into ./data, re-used on every run):

  A. sparse-verge  - two rectangles sharing the vertical edge x=4. The same
     8x8 viewport is panned so x=4 moves from the window's left edge to its
     right edge. The seam vanishes: half-open semantics, not a budget.

  B. dense-thicket - 460 overlapping "pad" rectangles plus 10 real seam pairs.
     POST /seams returns 422 at any limit (1..2000) while POST /join on the
     same window answers 200. An independent local count (features fetched
     through the window API, geometry recomputed here) shows 10 seams <= the
     2000 result cap, while the candidate work exceeds the 200,000 candidate
     budget: candidate exhaustion, not the result limit.

Usage:  python3 -I replay.py            (from this directory; keeps ./data)
"""
import json
from pathlib import Path
import select
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
BINARY = HERE / 'meridian'
DATA = HERE / 'data'
PREFIX = '/meridian/v1'

# Result cap and candidate budget enforced by the server
# (internal/spatial/join.go: MaxPairs / maxJoinCandidates).
MAX_PAIRS = 2000
MAX_CANDIDATES = 200_000

SPARSE = 'sparse-verge'
DENSE = 'dense-thicket'
PROBE = 'probe-slab'

# Scene A: shared vertical edge at x=4, y in [0,8).
WEST = dict(min_x=0, min_y=0, max_x=4, max_y=8)
EAST = dict(min_x=4, min_y=0, max_x=9, max_y=8)
VIEWPORT_LEFT = dict(min_x=4, min_y=0, max_x=12, max_y=8)   # x=4 is the left edge
VIEWPORT_RIGHT = dict(min_x=-4, min_y=0, max_x=4, max_y=8)  # x=4 is the right edge
VIEWPORT_WIDE = dict(min_x=0, min_y=0, max_x=12, max_y=8)   # x=4 is interior

# Scene B: one window over the dense cluster.
WINDOW_B = dict(min_x=800, min_y=0, max_x=840, max_y=24)
DENSE_BOUNDS = dict(min_x=0, min_y=0, max_x=900, max_y=900)
PADS = 460
SEAM_PAIRS = 10


def pad_box(i):
    s = i % 5
    return dict(min_x=s, min_y=s, max_x=800 + s, max_y=800 + s)


def dense_features():
    feats = [(f'pad-{i:03d}', pad_box(i)) for i in range(PADS)]
    for k in range(SEAM_PAIRS):
        feats.append((f'seam-w-{k}', dict(min_x=810, min_y=2 * k, max_x=820, max_y=2 * k + 2)))
        feats.append((f'seam-e-{k}', dict(min_x=820, min_y=2 * k, max_x=830, max_y=2 * k + 2)))
    return feats


def extents_overlap_positive(a, b):
    return (max(a['min_x'], b['min_x']) < min(a['max_x'], b['max_x'])
            and max(a['min_y'], b['min_y']) < min(a['max_y'], b['max_y']))


def may_meet(a, b):
    """Closed-envelope test, mirrors plane.Box.MayMeet (internal/plane/box.go)."""
    return (a['min_x'] <= b['max_x'] and b['min_x'] <= a['max_x']
            and a['min_y'] <= b['max_y'] and b['min_y'] <= a['max_y'])


def shared_boundary(a, b, window):
    """Local copy of plane.SharedBoundary (internal/plane/seam.go)."""
    if a['max_x'] == b['min_x'] or b['max_x'] == a['min_x']:
        x = max(a['min_x'], b['min_x'])
        lo = max(a['min_y'], b['min_y'], window['min_y'])
        hi = min(a['max_y'], b['max_y'], window['max_y'])
        if window['min_x'] <= x < window['max_x'] and lo < hi:
            return dict(axis='vertical', fixed=x, **{'from': lo}, to=hi)
    if a['max_y'] == b['min_y'] or b['max_y'] == a['min_y']:
        y = max(a['min_y'], b['min_y'])
        lo = max(a['min_x'], b['min_x'], window['min_x'])
        hi = min(a['max_x'], b['max_x'], window['max_x'])
        if window['min_y'] <= y < window['max_y'] and lo < hi:
            return dict(axis='horizontal', fixed=y, **{'from': lo}, to=hi)
    return None


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, expect=200):
        headers = {}
        data = None
        if isinstance(value, (dict, list)):
            data = json.dumps(value).encode()
            headers['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=data, method=method, headers=headers)
        try:
            resp = urlopen(req, timeout=30)
        except HTTPError as err:
            resp = err
        with resp:
            body = json.loads(resp.read())
            assert resp.status == expect, (method, path, resp.status, expect, body)
            return body

    def get(self, path, expect=200):
        return self.request('GET', path, expect=expect)

    def post(self, path, value, expect=200):
        return self.request('POST', path, value, expect)


def start_server():
    DATA.mkdir(exist_ok=True)
    child = subprocess.Popen(
        [str(BINARY), '-listen', '127.0.0.1:0', '-data', str(DATA)],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    readable, _, _ = select.select([child.stderr], [], [], 15)
    if not readable:
        raise RuntimeError('server did not start')
    startup = json.loads(child.stderr.readline())
    if 'address' not in startup:
        raise RuntimeError(startup)
    return child, Session('http://' + startup['address']), startup


def seed(s):
    existing = {row['name']: row['count'] for row in s.get('/strata')['layers']}
    want = {SPARSE: 2, DENSE: PADS + 2 * SEAM_PAIRS, PROBE: 1}
    if existing == want:
        return 'reused (layers already committed in ./data)'
    if existing:
        raise RuntimeError(f'data dir holds unexpected layers: {existing}')
    s.post('/strata', dict(name=SPARSE, kind='extent',
                           bounds=dict(min_x=-20, min_y=-20, max_x=20, max_y=20),
                           exclusive=False), expect=201)
    s.post('/strata', dict(name=DENSE, kind='extent', bounds=DENSE_BOUNDS,
                           exclusive=False), expect=201)
    s.post('/strata', dict(name=PROBE, kind='extent', bounds=DENSE_BOUNDS,
                           exclusive=False), expect=201)

    def op(layer, fid, shape):
        return dict(layer=layer, id=fid, mode='insert',
                    value=dict(id=fid, **{'class': 'fixture'}, shape=dict(extent=shape)))

    edits = [op(SPARSE, 'west', WEST), op(SPARSE, 'east', EAST)]
    edits += [op(DENSE, fid, shape) for fid, shape in dense_features()]
    edits.append(op(PROBE, 'slab', dict(min_x=805, min_y=0, max_x=825, max_y=24)))
    assert len(edits) <= 512, 'edit batch capacity'
    s.post('/edits', dict(edits=edits))
    return 'seeded fresh into ./data'


def scene_a(s):
    out = {}
    for label, window in [('left-edge', VIEWPORT_LEFT),
                          ('right-edge', VIEWPORT_RIGHT),
                          ('interior', VIEWPORT_WIDE)]:
        body = s.post(f'/strata/{SPARSE}/seams', dict(bounds=window, limit=MAX_PAIRS))
        out[label] = dict(window=window, seams=body['seams'], visit=body['visit'])
    # Both rectangles still exist and are index candidates under both viewports;
    # the window query (positive-area rule) shows only what the viewport owns.
    owned = s.post(f'/strata/{SPARSE}/window',
                   dict(bounds=VIEWPORT_RIGHT, limit=256))
    out['right-edge-window-features'] = [f['id'] for f in owned['features']]
    return out


def fetch_all_features(s, layer, bounds):
    feats, bookmark = [], None
    while True:
        query = dict(bounds=bounds, limit=256)
        if bookmark:
            query['bookmark'] = bookmark
        page = s.post(f'/strata/{layer}/window', query)
        feats.extend(page['features'])
        bookmark = page.get('next')
        if not bookmark:
            return feats


def scene_b(s):
    out = {}
    out['seams-limit-1'] = s.post(f'/strata/{DENSE}/seams',
                                  dict(bounds=WINDOW_B, limit=1), expect=422)
    out['seams-limit-2000'] = s.post(f'/strata/{DENSE}/seams',
                                     dict(bounds=WINDOW_B, limit=MAX_PAIRS), expect=422)
    out['seams-limit-2001'] = s.post(f'/strata/{DENSE}/seams',
                                     dict(bounds=WINDOW_B, limit=MAX_PAIRS + 1), expect=400)
    join_q = dict(bounds=WINDOW_B, limit=MAX_PAIRS)
    fwd = s.post('/join', dict(join_q, left=PROBE, right=DENSE))
    rev = s.post('/join', dict(join_q, left=DENSE, right=PROBE))
    out['join-probe-left'] = dict(pairs=len(fwd['pairs']), visit=fwd['visit'])
    out['join-dense-left'] = dict(pairs=len(rev['pairs']), visit=rev['visit'])

    # Independent evidence, not touching the seams code path: pull every
    # feature through the paginated window API and recompute geometry here.
    feats = [(f['id'], f['shape']['extent']) for f in fetch_all_features(s, DENSE, DENSE_BOUNDS)]
    assert len(feats) == PADS + 2 * SEAM_PAIRS

    seams = []
    for i, (id_a, a) in enumerate(feats):
        for id_b, b in feats[i + 1:]:
            seam = shared_boundary(a, b, WINDOW_B)
            if seam:
                seams.append((id_a, id_b, seam))

    # Exact candidate work under the server's counting rule: the window search
    # visits every feature whose closed envelope MayMeets the window; each such
    # feature then drives an envelope search that visits every feature whose
    # envelope MayMeets its own (packed.Tree.Search counts exactly these rows,
    # because node bounds are monotone unions).
    outer = [(fid, box) for fid, box in feats if may_meet(box, WINDOW_B)]
    inner = sum(1 for _, box in outer for _, other in feats if may_meet(other, box))
    out['independent'] = dict(
        features=len(feats),
        seam_count=len(seams),
        seam_result_cap=MAX_PAIRS,
        seam_pairs=[[a, b] for a, b, _ in seams],
        window_candidates=len(outer),
        envelope_pair_candidates=inner,
        total_candidate_work=len(outer) + inner,
        candidate_budget=MAX_CANDIDATES,
    )
    return out


def main():
    child, s, startup = start_server()
    report = {'data_dir': str(DATA), 'startup': startup}
    try:
        report['seed'] = seed(s)
        report['scene-a-half-open'] = scene_a(s)
        report['scene-b-candidate-exhaustion'] = scene_b(s)
        report['final-status'] = s.get('/status')
        report['strata'] = s.get('/strata')['layers']
    finally:
        child.terminate()
        child.wait(timeout=5)
    print(json.dumps(report, indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
