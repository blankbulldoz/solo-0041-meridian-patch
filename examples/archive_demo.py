#!/usr/bin/env python3
"""Prove plain-data archives keep row identity across real exports.

The script exercises the real HTTP service to produce four MPB images:

  a.mpb  real export after seeding (revision 4)
  b.mpb  real export after restoring a.mpb back (revision 5, same content)
  d.mpb  real export after a layer re-layout (revision 6, same features)
  c.mpb  a legally re-encoded image whose feature content changed, carrying
         a database revision beyond the JavaScript safe integer range

It then unpacks each image into a plain-data archive with mpbarchive and
checks that row identity is verifiable, that big integers survive as exact
decimal text, and that compare classifies the pairs as revision-only,
relayout, and content-changed.
"""
import csv
import hashlib
import json
from pathlib import Path
import select
import shutil
import struct
import subprocess
import tempfile
from contextlib import contextmanager
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
FIELD, BED, PINS = 'ochre-field', 'mica-bed', 'plumb-marks'
SAFE_INTEGER = 2**53 - 1

BIG = 10**9
BOUNDS_BIG = dict(min_x=-BIG, min_y=-BIG, max_x=BIG, max_y=BIG)
BOUNDS_BED = dict(min_x=-20, min_y=-20, max_x=20, max_y=20)
BOUNDS_BED_WIDE = dict(min_x=-25, min_y=-25, max_x=25, max_y=25)

# Coordinate-derived quantities that exceed the JavaScript safe integer range.
VAST_AREA = (2 * BIG) ** 2                      # 4000000000000000000 > 2^53-1
BRAD_ODIST = 2 * (BIG - 1) ** 2                 # 1999999996000000002 > 2^53-1
assert VAST_AREA > SAFE_INTEGER and BRAD_ODIST > SAFE_INTEGER
HUGE_REVISION = 2**53 + 5                       # beyond the safe integer range


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def op(layer, feature, shape=None, category='fixture', mode='insert'):
    item = dict(layer=layer, id=feature, mode=mode)
    if shape is not None:
        kind = 'mark' if 'x' in shape else 'extent'
        item['value'] = dict(id=feature, **{'class': category}, shape={kind: shape})
    return item


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
            assert response.status == status, (method, path, response.status, status, raw[:512])
            body = json.loads(raw) if response.headers.get_content_type() == 'application/json' else raw
            return body, response.headers

    def get(self, path, status=200):
        return self.request('GET', path, status=status)[0]

    def post(self, path, value, status=200):
        return self.request('POST', path, value, status)[0]


@contextmanager
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
        yield session
    finally:
        if child.poll() is None:
            child.terminate()
        try:
            child.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.communicate(timeout=5)


def seed(s):
    s.post('/strata', dict(name=FIELD, kind='extent', bounds=BOUNDS_BIG, exclusive=False), 201)
    s.post('/strata', dict(name=BED, kind='extent', bounds=BOUNDS_BED, exclusive=False), 201)
    s.post('/strata', dict(name=PINS, kind='mark', bounds=BOUNDS_BIG, exclusive=True), 201)
    return s.post('/edits', dict(edits=[
        op(FIELD, 'prong', box(0, 0, 4, 3)),
        op(FIELD, 'dowel', box(4, 0, 8, 3)),
        op(FIELD, 'vast', box(-BIG, -BIG, BIG, BIG), 'basin'),
        op(BED, 'shim', box(3, 0, 5, 2)),
        op(PINS, 'brad', dict(x=BIG - 1, y=BIG - 1)),
        op(PINS, 'stud', dict(x=6, y=1), 'sleeve'),
    ]))['revision']


def export_image(s, path):
    raw, headers = s.request('GET', '/image')
    assert headers.get_content_type() == IMAGE
    path.write_bytes(raw)
    return int(headers['Meridian-Revision'])


def restore_image(s, raw, revision, status=200):
    return s.request('PUT', '/image', raw, status, {
        'Content-Type': IMAGE, 'If-Meridian-Revision': str(revision),
    })[0]


def craft_modified(image_a):
    """Re-encode image A as a legal MPB whose feature content changed."""
    raw = image_a.read_bytes()
    assert raw[:8] == b'MRDNMPB\x01'
    length = struct.unpack('>I', raw[8:12])[0]
    payload = raw[44:]
    assert length == len(payload) and hashlib.sha256(payload).digest() == raw[12:44]
    image = json.loads(payload)
    image['revision'] = HUGE_REVISION
    for layer in image['layers']:
        layer['revision'] = HUGE_REVISION - 1
        if layer['definition']['name'] == FIELD:
            for feature in layer['features']:
                if feature['id'] == 'dowel':
                    feature['shape']['extent']['max_x'] = 9  # geometry changed
        if layer['definition']['name'] == PINS:
            for feature in layer['features']:
                if feature['id'] == 'stud':
                    feature['class'] = 'anchor'              # class changed
        if layer['definition']['name'] == BED:
            layer['features'] = []                           # shim removed
    payload = json.dumps(image, separators=(',', ':')).encode()
    return b'MRDNMPB\x01' + struct.pack('>I', len(payload)) + hashlib.sha256(payload).digest() + payload


def run(tool, *args, expect=0):
    result = subprocess.run([str(tool), *args], capture_output=True, text=True)
    assert result.returncode == expect, (args, result.returncode, result.stdout, result.stderr)
    return result


def compare(tool, a, b):
    result = run(tool, 'compare', str(a), str(b), '--json')
    return json.loads(result.stdout)


def feature_rows(archive_dir):
    with open(archive_dir / 'features.csv', newline='') as handle:
        return {(row['layer'], row['feature_id']): row
                for row in csv.DictReader(handle)}


def main():
    with tempfile.TemporaryDirectory(prefix='meridian-archive-') as temporary:
        root = Path(temporary)
        meridian, tool = root / 'meridian', root / 'mpbarchive'
        subprocess.run(['go', 'build', '-o', str(meridian), './cmd/meridian'], check=True, cwd=ROOT, timeout=90)
        subprocess.run(['go', 'build', '-o', str(tool), './cmd/mpbarchive'], check=True, cwd=ROOT, timeout=90)
        images, archives = root / 'images', root / 'archives'
        images.mkdir()

        with running(meridian, root / 'space') as s:
            rev_a = seed(s)
            assert export_image(s, images / 'a.mpb') == rev_a
            bumped = restore_image(s, (images / 'a.mpb').read_bytes(), rev_a)
            assert bumped['revision'] == rev_a + 1
            assert export_image(s, images / 'b.mpb') == rev_a + 1
            assert (images / 'a.mpb').read_bytes() != (images / 'b.mpb').read_bytes()
            relaid = s.request('PUT', f'/strata/{BED}/layout',
                               dict(bounds=BOUNDS_BED_WIDE, exclusive=False))[0]
            assert relaid['revision'] == rev_a + 2
            assert export_image(s, images / 'd.mpb') == rev_a + 2

        # A legal image whose feature content changed, proved restorable.
        (images / 'c.mpb').write_bytes(craft_modified(images / 'a.mpb'))
        with running(meridian, root / 'space-check') as s:
            restored = restore_image(s, (images / 'c.mpb').read_bytes(), 0)
            assert restored['revision'] == 1 and len(restored['layers']) == 3
            located = s.post(f'/strata/{FIELD}/locate', dict(x=8, y=0))
            # The widened dowel (max_x 8 -> 9) now owns x=8; in a.mpb it did not.
            assert [row['id'] for row in located['features']] == ['dowel', 'vast']
            window = s.post(f'/strata/{PINS}/window', dict(bounds=BOUNDS_BIG, limit=10, **{'class': 'anchor'}))
            assert [row['id'] for row in window['features']] == ['stud']

        for name in 'abcd':
            run(tool, 'unpack', images / f'{name}.mpb', archives / name)
        run(tool, 'unpack', images / 'a.mpb', archives / 'a-again')
        for name in ('manifest.json', 'layers.csv', 'features.csv', 'classes.csv'):
            assert (archives / 'a' / name).read_bytes() == (archives / 'a-again' / name).read_bytes()

        verdicts = {
            'a==a': compare(tool, archives / 'a', archives / 'a'),
            'a~b': compare(tool, archives / 'a', archives / 'b'),
            'a~d': compare(tool, archives / 'a', archives / 'd'),
            'a~c': compare(tool, archives / 'a', archives / 'c'),
            'mpb~mpb': compare(tool, images / 'a.mpb', images / 'b.mpb'),
        }
        assert verdicts['a==a']['verdict'] == 'identical'
        assert verdicts['a~b']['verdict'] == 'revision-only', verdicts['a~b']
        assert verdicts['mpb~mpb']['verdict'] == 'revision-only'
        assert verdicts['a~d']['verdict'] == 'relayout', verdicts['a~d']
        assert verdicts['a~c']['verdict'] == 'content-changed', verdicts['a~c']

        only = verdicts['a~b']
        assert (only['revision_a'], only['revision_b']) == (str(rev_a), str(rev_a + 1))
        assert only['matched_features'] == 6 and 'feature_changes' not in only

        relaid = verdicts['a~d']
        assert 'feature_changes' not in relaid and relaid['matched_features'] == 6
        assert len(relaid['layout_changes']) == 1
        change = relaid['layout_changes'][0]
        assert change['layer'] == BED
        assert (change['bounds_a'], change['bounds_b']) == ('-20,-20,20,20', '-25,-25,25,25')

        changed = verdicts['a~c']
        assert changed['revision_b'] == str(HUGE_REVISION)
        assert {(row['layer'], row['id'], row['change']) for row in changed['feature_changes']} == {
            (FIELD, 'dowel', 'changed'), (PINS, 'stud', 'changed'), (BED, 'shim', 'removed')}

        # Row identity is checkable from the plain tables alone: every row of
        # the two real exports matches by digest, while c.mpb differs exactly
        # at the modified rows.
        rows_a, rows_b, rows_c = (feature_rows(archives / name) for name in 'abc')
        assert set(rows_a) == set(rows_b)
        assert all(rows_a[key]['row_digest'] == rows_b[key]['row_digest'] for key in rows_a)
        assert set(rows_c) == set(rows_a) - {(BED, 'shim')}
        for key in set(rows_a) & set(rows_c):
            same = rows_a[key]['row_digest'] == rows_c[key]['row_digest']
            assert same == (key not in {(FIELD, 'dowel'), (PINS, 'stud')}), key

        # Revisions and coordinate-derived quantities beyond 2^53-1 survive
        # as exact decimal text.
        assert rows_a[(FIELD, 'vast')]['area'] == str(VAST_AREA)
        assert rows_a[(PINS, 'brad')]['origin_distance_squared'] == str(BRAD_ODIST)
        manifest_c = json.loads((archives / 'c' / 'manifest.json').read_text())
        assert manifest_c['database_revision'] == str(HUGE_REVISION)
        assert isinstance(manifest_c['database_revision'], str)
        manifest_a = json.loads((archives / 'a' / 'manifest.json').read_text())
        assert manifest_a['total_extent_area'] == str(VAST_AREA + 12 + 12 + 4)

        with open(archives / 'a' / 'classes.csv', newline='') as handle:
            groups = {(row['class'], row['kind']): row for row in csv.DictReader(handle)}
        assert groups[('basin', 'extent')]['total_area'] == str(VAST_AREA)
        assert groups[('fixture', 'extent')]['total_area'] == str(12 + 12 + 4)
        assert groups[('fixture', 'extent')]['layers'] == f'{BED};{FIELD}'
        assert groups[('fixture', 'mark')]['max_origin_distance_squared'] == str(BRAD_ODIST)
        assert groups[('sleeve', 'mark')]['feature_count'] == '1'

        # A tampered row is detected when the archive is read back.
        shutil.copytree(archives / 'a', archives / 'tampered')
        table = archives / 'tampered' / 'features.csv'
        table.write_text(table.read_text().replace(
            'dowel,fixture,extent,4,0,8,3', 'dowel,fixture,extent,4,0,7,3'))
        rejected = run(tool, 'verify', archives / 'tampered', expect=1)
        assert 'digest mismatch' in rejected.stderr
        run(tool, 'verify', archives / 'a')
        run(tool, 'verify', images / 'c.mpb')

        print(json.dumps(dict(
            workflow='archive',
            observed=dict(
                revisions=dict(a=rev_a, b=rev_a + 1, d=rev_a + 2, c=HUGE_REVISION),
                verdicts={key: value['verdict'] for key, value in verdicts.items()},
                matched_features_revision_only=only['matched_features'],
                relayout_layer=change['layer'],
                content_changes=sorted(
                    f"{row['layer']}/{row['id']}:{row['change']}" for row in changed['feature_changes']),
                vast_area=rows_a[(FIELD, 'vast')]['area'],
                brad_origin_distance_squared=rows_a[(PINS, 'brad')]['origin_distance_squared'],
                c_database_revision=manifest_c['database_revision'],
                tamper_check=rejected.stderr.strip().splitlines()[-1],
            ),
        ), sort_keys=True))


if __name__ == '__main__':
    main()
