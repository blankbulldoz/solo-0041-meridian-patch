#!/usr/bin/env python3
"""Build the two deterministic narrow-window data samples.

Sample A places 30 zero-area "fringe" rectangles against every query window,
so the window sees many bounding-box candidates but only the 3 real hits.
Sample B stores the same 400 features (same IDs, classes, anchors, hits and
background) but moves every fringe rectangle into a remote block, so the very
same windows see only the 3 real hits.

The script talks to a real meridian server, exports an MPB v1 image, then
imports that image into a second empty server and exports again.  The second
export is a byte-stable fixed point (all revisions become 1), which is what we
ship.  Nothing about the geometry changes on import.
"""
import argparse
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
LAYER = 'candidate-field'
BOUNDS = dict(min_x=-80, min_y=-80, max_x=80, max_y=80)

# Query windows.  They are shared by both samples and stored once in
# windows.json.  All are narrow slits: 2 units thick or a 4x4 square.
WINDOW_DEFS = [
    ('w1', dict(min_x=-60, min_y=-36, max_x=-10, max_y=-34)),
    ('w2', dict(min_x=-60, min_y=-8, max_x=-10, max_y=-6)),
    ('w3', dict(min_x=-60, min_y=24, max_x=-10, max_y=26)),
    ('w4', dict(min_x=10, min_y=-60, max_x=12, max_y=60)),
    ('w5', dict(min_x=40, min_y=-60, max_x=42, max_y=60)),
    ('w6', dict(min_x=-2, min_y=-2, max_x=2, max_y=2)),
]

# Three true hits per window.  Every one is a positive-area overlap with its
# own window and avoids every other window's interior.
HITS = {
    'w1': [(-54, -37, -48, -33), (-32, -35, -26, -33), (-18, -36, -12, -34)],
    'w2': [(-52, -9, -46, -5), (-30, -8, -24, -6), (-16, -10, -12, -6)],
    'w3': [(-56, 23, -50, 27), (-34, 24, -28, 26), (-20, 23, -14, 27)],
    'w4': [(9, -48, 13, -42), (10, -12, 12, -6), (10, 20, 12, 26)],
    'w5': [(39, -54, 43, -48), (40, 4, 42, 10), (38, 34, 44, 40)],
    'w6': [(-3, -3, 1, 1), (0, -1, 2, 1), (-1, 0, 3, 2)],
}

ANCHORS = [
    ('anchor-0', (-80, -80, -74, -74)),
    ('anchor-1', (74, -80, 80, -74)),
    ('anchor-2', (-80, 74, -74, 80)),
    ('anchor-3', (74, 74, 80, 80)),
]


def rect(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def feature(fid, cls, r):
    return dict(id=fid, **{'class': cls}, shape=dict(extent=rect(*r)))


def fringe_rects(window_id, bounds):
    """30 rectangles whose envelopes touch `bounds` but have zero-area overlap."""
    x0, y0, x1, y1 = bounds['min_x'], bounds['min_y'], bounds['max_x'], bounds['max_y']
    out = []
    if window_id in ('w1', 'w2', 'w3'):
        # long horizontal slit: 15 below (max_y == min_y) and 15 above
        # (min_y == max_y).  All x positions lie inside the slit's x span.
        for j in range(15):
            x = -60 + 3 * j
            out.append((x, y0 - 2, x + 2, y0))
        for j in range(15):
            x = -60 + 3 * j
            out.append((x, y1, x + 2, y1 + 2))
    elif window_id in ('w4', 'w5'):
        # long vertical slit: 15 left (max_x == min_x) and 15 right
        # (min_x == max_x).  All y positions lie inside the slit's y span.
        for j in range(15):
            y = -60 + 8 * j
            out.append((x0 - 2, y, x0, y + 1))
        for j in range(15):
            y = -60 + 8 * j
            out.append((x1, y, x1 + 2, y + 1))
    else:
        # 4x4 square: eight rectangles on each side.  Side rectangles touch
        # the window with a zero-width / zero-height edge.
        for j in range(8):
            y = -2 + (j % 4)
            out.append((-4 - (j // 4), y, -2, y + 1))   # left
        for j in range(8):
            y = -2 + (j % 4)
            out.append((2, y, 4 + (j // 4), y + 1))    # right
        for j in range(7):
            x = -2 + (j % 4)
            out.append((x, -4 - (j // 4), x + 1, -2))  # bottom
        for j in range(7):
            x = -2 + (j % 4)
            out.append((x, 2, x + 1, 4 + (j // 4)))    # top
    return out


def background_rects():
    """198 rectangles in two bands outside every window's envelope."""
    out = []
    for band, y0 in (('top', 68), ('bottom', -74)):
        for k in range(99):
            col, row = k % 33, k // 33
            x = -70 + 4 * col
            y = y0 + 2 * row
            out.append((x, y, x + 2, y + 1))
    return out


def remote_block_rects(count):
    """Sample B parking block for all 180 fringe rectangles.

    x in [-72,-54], y in [50,69]; away from every window envelope.
    """
    out = []
    for k in range(count):
        col, row = k % 18, k // 18
        x = -72 + col
        y = 50 + 2 * row
        out.append((x, y, x + 1, y + 1))
    return out


def layout(sample):
    """Return ordered (id, role, rect) triples; IDs are identical in A and B."""
    rows = []
    for fid, r in ANCHORS:
        rows.append((fid, 'anchor', r))
    for name, _ in WINDOW_DEFS:
        for k, r in enumerate(HITS[name]):
            rows.append((f'hit-{name}-{k}', 'hit', r))
    fringe_count = 0
    for name, bounds in WINDOW_DEFS:
        for k, r in enumerate(fringe_rects(name, bounds)):
            if sample == 'a':
                rows.append((f'fringe-{name}-{k:02d}', 'fringe', r))
            else:
                rows.append((f'fringe-{name}-{k:02d}', 'fringe', None))
        fringe_count += len(fringe_rects(name, bounds))
    assert fringe_count == 180
    if sample == 'b':
        parked = remote_block_rects(fringe_count)
        idx = 0
        for i, (fid, role, r) in enumerate(rows):
            if role == 'fringe':
                rows[i] = (fid, role, parked[idx])
                idx += 1
    for k, r in enumerate(background_rects()):
        rows.append((f'bg-{k:03d}', 'background', r))
    return rows


def assign_classes(rows):
    """Exact 200 core / 120 halo / 80 edge, identical for both samples."""
    classes = {}
    for fid, role, _ in rows:
        if role in ('anchor', 'hit'):
            classes[fid] = 'core'
    fringe_ids = sorted(fid for fid, role, _ in rows if role == 'fringe')
    for n, fid in enumerate(fringe_ids):
        classes[fid] = 'core' if n < 120 else 'halo'
    background_ids = sorted(fid for fid, role, _ in rows if role == 'background')
    for n, fid in enumerate(background_ids):
        if n < 58:
            classes[fid] = 'core'
        elif n < 118:
            classes[fid] = 'halo'
        else:
            classes[fid] = 'edge'
    return classes


def payload(sample):
    rows = layout(sample)
    classes = assign_classes(rows)
    assert len(rows) == 400
    edits = [dict(layer=LAYER, id=fid, mode='insert',
                  value=feature(fid, classes[fid], r)) for fid, role, r in rows]
    layer = dict(name=LAYER, kind='extent', bounds=BOUNDS, exclusive=False)
    return dict(layer=layer, edits=edits)


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, status=200, headers=None):
        body = value
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            body = json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
            args['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=body, method=method, headers=args)
        try:
            response = urlopen(req, timeout=15)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            assert response.status == status, (method, path, response.status, status, raw[:512])
            body = json.loads(raw) if response.headers.get_content_type() == 'application/json' else raw
            return body, response.headers


def start(binary, data):
    args = [str(binary), '-listen', '127.0.0.1:0', '-data', str(data)]
    child = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    import select
    readable, _, _ = select.select([child.stderr], [], [], 15)
    if not readable:
        child.kill()
        raise RuntimeError('service did not start within 15 seconds')
    startup = json.loads(child.stderr.readline())
    if 'address' not in startup:
        raise RuntimeError(startup)
    return child, Session('http://' + startup['address'])


def stop(child):
    if child.poll() is None:
        child.terminate()
    try:
        child.communicate(timeout=10)
    except subprocess.TimeoutExpired:
        child.kill()
        child.communicate(timeout=10)


def import_image(session, raw):
    return session.request('PUT', '/image', raw, 200, {
        'Content-Type': IMAGE, 'If-Meridian-Revision': '0',
    })[0]


def build_one(binary, sample, work):
    body = payload(sample)
    built = work / f'built-{sample}'
    child, session = start(binary, built)
    try:
        session.request('POST', '/strata', body['layer'], 201)
        # One batch of 400 inserts is below the 512-op batch limit.
        commit = session.request('POST', '/edits', dict(edits=body['edits']), 200)[0]
        assert commit['revision'] == 2, commit
        raw, headers = session.request('GET', '/image')
        assert int(headers['Meridian-Revision']) == 2
    finally:
        stop(child)

    normalized = work / f'normalized-{sample}'
    child, session = start(binary, normalized)
    try:
        restored = import_image(session, raw)
        assert restored['revision'] == 1, restored
        fixed, headers = session.request('GET', '/image')
        assert int(headers['Meridian-Revision']) == 1
        assert fixed != raw
    finally:
        stop(child)

    # Fixed point: importing the normalized image reproduces it byte for byte.
    check = work / f'check-{sample}'
    child, session = start(binary, check)
    try:
        import_image(session, fixed)
        again, _ = session.request('GET', '/image')
        assert again == fixed, 'normalized image is not a fixed point'
    finally:
        stop(child)

    return body, fixed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', help='meridian binary (default: go build ./cmd/meridian)')
    args = parser.parse_args()

    for sub in ('payloads', 'images'):
        (HERE / sub).mkdir(exist_ok=True)

    binary = Path(args.binary) if args.binary else None
    with tempfile.TemporaryDirectory(prefix='meridian-samples-') as temporary:
        work = Path(temporary)
        if binary is None:
            go = shutil.which('go')
            if go is None:
                sys.exit('go toolchain not found; pass --binary')
            binary = work / 'meridian'
            subprocess.run([go, 'build', '-o', str(binary), './cmd/meridian'],
                           check=True, cwd=ROOT, timeout=180)
        for sample in ('a', 'b'):
            body, image = build_one(binary, sample, work)
            (HERE / 'payloads' / f'sample-{sample}.json').write_text(
                json.dumps(body, indent=2, sort_keys=True) + '\n')
            (HERE / 'images' / f'sample-{sample}.mpb').write_bytes(image)
            print(json.dumps(dict(
                sample=sample,
                features=len(body['edits']),
                image_bytes=len(image),
                image_sha256=hashlib.sha256(image).hexdigest(),
            ), sort_keys=True))

    windows = [dict(name=name, request=dict(bounds=bounds, limit=256))
               for name, bounds in WINDOW_DEFS]
    (HERE / 'windows.json').write_text(json.dumps(windows, indent=2, sort_keys=True) + '\n')
    params = dict(
        layer=dict(name=LAYER, kind='extent', bounds=BOUNDS, exclusive=False),
        feature_count=400,
        class_counts=dict(core=200, halo=120, edge=80),
        roles=dict(anchor=4, hit=18, fringe=180, background=198),
        windows_file='windows.json',
        sample_a_layout='30 fringe rectangles touch each window with zero-area overlap',
        sample_b_layout='the same 180 fringe rectangles parked in x[-72,-54], y[50,69]',
    )
    (HERE / 'params.json').write_text(json.dumps(params, indent=2, sort_keys=True) + '\n')


if __name__ == '__main__':
    main()
