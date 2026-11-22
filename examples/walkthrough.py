#!/usr/bin/env python3
"""Exercise the normal HTTP surface in a clean temporary database."""
import argparse
from contextlib import contextmanager
import json
from pathlib import Path
import select
import subprocess
import tempfile
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
FIELD, BED, PINS = 'ochre-field', 'mica-bed', 'plumb-marks'


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def point(x, y):
    return dict(x=x, y=y)


BOUNDS = box(-20, -20, 20, 20)
WINDOW = box(0, 0, 8, 4)


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

    def query(self, layer, rule, value, status=200):
        return self.post(f'/strata/{layer}/{rule}', value, status)

    def layer(self, name, kind='extent', exclusive=False, status=201):
        return self.post('/strata', dict(name=name, kind=kind, bounds=BOUNDS, exclusive=exclusive), status)

    def edit(self, *items, status=200, revision=None):
        request = dict(edits=list(items))
        if revision is not None:
            request['if_revision'] = revision
        return self.post('/edits', request, status)

    def restore(self, raw, revision, status=200):
        return self.request('PUT', '/image', raw, status, {
            'Content-Type': IMAGE, 'If-Meridian-Revision': str(revision),
        })[0]


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


def op(layer, feature, shape=None, category='fixture', mode='insert'):
    item = dict(layer=layer, id=feature, mode=mode)
    if shape is not None:
        kind = 'mark' if 'x' in shape else 'extent'
        item['value'] = dict(id=feature, **{'class': category}, shape={kind: shape})
    return item


def seed(s):
    s.layer(FIELD)
    s.layer(BED)
    s.layer(PINS, 'mark', True)
    return s.edit(
        op(FIELD, 'prong', box(0, 0, 4, 3)),
        op(FIELD, 'dowel', box(4, 0, 8, 3)),
        op(FIELD, 'ferrule', box(2, 1, 6, 4), 'sleeve'),
        op(BED, 'shim', box(3, 0, 5, 2)),
        op(PINS, 'brad', point(4, 1)),
        op(PINS, 'stud', point(6, 1)),
    )['revision']


def ids(result):
    return [row['id'] for row in result['features']]


def register(s):
    s.layer(BED)
    assert s.layer(FIELD)['revision'] == 2
    assert s.layer(BED, status=409)['code'] == 'conflict'
    assert s.post('/strata', dict(name='bad', kind='extent', bounds=box(2, 0, 1, 6)), 400)['code'] == 'invalid'
    assert [layer['name'] for layer in s.get('/strata')['layers']] == [BED, FIELD]
    return s.get('/strata')


def edit(s):
    s.layer(FIELD, exclusive=True)
    s.layer(PINS, 'mark', True)
    first = s.edit(op(FIELD, 'prong', box(0, 0, 2, 2)), op(FIELD, 'dowel', box(2, 0, 4, 2)))
    s.edit(op(PINS, 'brad', point(0, 0)), op(FIELD, 'ferrule', box(1, 0, 3, 2)), status=409)
    assert s.get('/status')['revision'] == first['revision']
    assert ids(s.query(PINS, 'locate', point(0, 0))) == []
    s.edit(op(FIELD, 'prong', box(2, 0, 4, 2), mode='replace'), op(FIELD, 'dowel', box(0, 0, 2, 2), mode='replace'))
    s.edit(op(FIELD, 'ferrule', box(8, 0, 9, 1)), status=409, revision=first['revision'])
    result = s.query(FIELD, 'locate', point(2, 0))
    assert ids(result) == ['prong']
    return result


def reframe(s):
    rev = seed(s)
    path = f'/strata/{FIELD}/layout'
    s.request('PUT', path, dict(bounds=BOUNDS, exclusive=True), 409)
    s.request('PUT', path, dict(bounds=box(0, 0, 5, 5)), 400)
    assert s.get('/status')['revision'] == rev
    result, _ = s.request('PUT', path, dict(bounds=WINDOW, if_revision=rev))
    assert result['layers'][0]['bounds'] == WINDOW and result['revision'] == rev+1
    same, _ = s.request('PUT', path, dict(bounds=WINDOW))
    assert same == result
    return result


def window(s):
    seed(s)
    query = dict(bounds=WINDOW, limit=1)
    first = s.query(FIELD, 'window', query)
    assert ids(first) == ['dowel'] and first['next']
    s.edit(op(BED, 'shim', mode='remove'))
    second = s.query(FIELD, 'window', dict(query, limit=2, bookmark=first['next']))
    assert ids(second) == ['ferrule', 'prong'] and 'next' not in second
    assert second['layer_revision'] == first['layer_revision'] and second['revision'] > first['revision']
    s.query(FIELD, 'window', dict(query, bookmark=first['next'], **{'class': 'sleeve'}), 400)
    s.edit(op(FIELD, 'prong', mode='remove'))
    s.query(FIELD, 'window', dict(query, bookmark=first['next']), 409)
    return second


def locate(s):
    seed(s)
    assert ids(s.query(FIELD, 'locate', point(4, 0))) == ['dowel']
    assert ids(s.query(PINS, 'locate', point(4, 1))) == ['brad']
    assert ids(s.query(FIELD, 'locate', point(8, 0))) == []
    s.query(FIELD, 'locate', point(1_000_000_001, 0), 400)
    result = s.query(FIELD, 'locate', point(4, 1))
    assert ids(result) == ['dowel', 'ferrule']
    return result


def nearest(s):
    seed(s)
    result = s.query(FIELD, 'nearest', dict(point=point(4, 0), limit=2))
    assert [(row['feature']['id'], row['distance_squared']) for row in result['neighbors']] == [('dowel', 0), ('prong', 0)]
    near = s.query(PINS, 'nearest', dict(point=point(5, 1), limit=2))
    assert [(row['feature']['id'], row['distance_squared']) for row in near['neighbors']] == [('brad', 1), ('stud', 1)]
    s.query(FIELD, 'nearest', dict(point=point(4, 0), limit=129), 400)
    return result


def join(s):
    seed(s)
    query = dict(left=FIELD, right=BED, bounds=box(4, 0, 8, 4), limit=10)
    result = s.post('/join', query)
    assert result['pairs'] == [dict(left='dowel', right='shim', intersection=box(4, 0, 5, 2)),
                               dict(left='ferrule', right='shim', intersection=box(4, 1, 5, 2))]
    s.post('/join', dict(query, limit=1), 422)
    assert s.post('/join', dict(query, right=PINS))['pairs'][0] == dict(left='dowel', right='brad')
    return result


def coverage(s):
    seed(s)
    query = dict(bounds=WINDOW)
    result = s.query(FIELD, 'coverage', query)
    assert (result['covered'], result['repeated'], result['uncovered']) == (28, 8, 4)
    empty = s.query(FIELD, 'coverage', dict(bounds=box(10, 10, 12, 12)))
    assert (empty['covered'], empty['uncovered']) == (0, 4)
    s.query(PINS, 'coverage', query, 400)
    return result


def seams(s):
    seed(s)
    result = s.query(FIELD, 'seams', dict(bounds=box(4, 1, 8, 4), limit=2))
    assert result['seams'] == [dict(first='dowel', second='prong', seam=dict(axis='vertical', fixed=4, **{'from': 1}, to=3))]
    assert s.query(FIELD, 'seams', dict(bounds=box(0, 0, 4, 4), limit=2))['seams'] == []
    s.query(PINS, 'seams', dict(bounds=WINDOW, limit=2), 400)
    return result


def relation(s):
    seed(s)
    pin = dict(layer=PINS, id='brad')
    prong = dict(layer=FIELD, id='prong')
    dowel = dict(layer=FIELD, id='dowel')
    edge = s.post('/relation', dict(left=prong, right=pin))
    assert not edge['intersects'] and edge['distance_squared'] == 0
    right = s.post('/relation', dict(left=dowel, right=pin))
    assert right['intersects'] and right['left_contains'] and not right['right_contains']
    s.post('/relation', dict(left=dowel, right=dict(layer=PINS, id='gone')), 404)
    return right


def load_index(s):
    s.layer(PINS, 'mark')
    s.edit(*(op(PINS, f'peg-{i}', point(i-12, i % 3)) for i in range(24)))


def inspect(s):
    load_index(s)
    result = s.get(f'/strata/{PINS}/index')
    assert (result['tree']['items'], result['tree']['nodes'], result['tree']['max_leaf']) == (24, 3, 12)
    s.get('/strata/missing/index', 404)
    return result


def audit(s):
    s.layer(PINS, 'mark')
    assert s.get(f'/strata/{PINS}/index-audit')['checked_nodes'] == 0
    s.edit(*(op(PINS, f'peg-{i}', point(i-12, i % 3)) for i in range(25)))
    result = s.get(f'/strata/{PINS}/index-audit')
    assert (result['records'], result['checked_nodes']) == (25, 5)
    s.get('/strata/missing/index-audit', 404)
    return result


def export(s):
    rev = seed(s)
    first, headers = s.request('GET', '/image')
    assert headers.get_content_type() == IMAGE and int(headers['Meridian-Revision']) == rev
    assert first.startswith(b'MRDNMPB\x01') and len(first) == int(headers['Content-Length'])
    assert s.get('/image') == first
    s.edit(op(BED, 'shim', mode='remove'))
    assert s.get('/image') != first
    return dict(revision=rev, bytes=len(first))


def decode_image(raw):
    assert raw[:8] == b'MRDNMPB\x01'
    import hashlib
    length = int.from_bytes(raw[8:12], 'big')
    payload = raw[44:]
    assert length == len(payload)
    assert hashlib.sha256(payload).digest() == raw[12:44]
    return json.loads(payload)


def image_layers(image):
    return {layer['definition']['name']: layer for layer in image['layers']}


def feature_ids(layer):
    return [feature['id'] for feature in layer['features']]


def export_scope(s):
    rev = seed(s)
    full, headers = s.request('GET', '/image')
    full_image = decode_image(full)
    assert int(headers['Meridian-Revision']) == full_image['revision'] == rev

    # Partial layer selection keeps sorted order, snapshot revisions and
    # deterministic bytes; absent parameters stay a full, identical export.
    partial, _ = s.request('GET', '/image?layers=plumb-marks&layers=ochre-field')
    partial_image = decode_image(partial)
    assert [layer['definition']['name'] for layer in partial_image['layers']] == [FIELD, PINS]
    assert partial_image['revision'] == rev
    for layer in partial_image['layers']:
        assert layer['revision'] <= rev
    assert s.request('GET', '/image?layers=ochre-field&layers=plumb-marks')[0] == partial
    assert s.request('GET', '/image?layers=plumb-marks')[0] != full

    # Extra geometry probes the half-open edges of the window.
    s.edit(
        op(FIELD, 'grazed', box(8, 0, 10, 2)),
        op(PINS, 'edge', point(8, 0)),
        op(PINS, 'top', point(5, 3)),
    )
    s.layer('void', 'mark', False)
    s.post('/strata', dict(name='nook', kind='extent', bounds=box(-20, -20, -16, -12), exclusive=False), 201)

    # Window [4,8) x [0,3): lower/left edges own, upper/right edges exclude;
    # rectangles are clipped to the positive-area intersection.
    scoped, headers = s.request('GET', '/image?min_x=4&min_y=0&max_x=8&max_y=3')
    scoped_image = decode_image(scoped)
    assert int(headers['Meridian-Revision']) == scoped_image['revision']
    layers = image_layers(scoped_image)
    for name in (FIELD, BED, PINS, 'void'):
        assert layers[name]['definition']['bounds'] == box(4, 0, 8, 3)
    assert layers['nook']['definition']['bounds'] == box(-20, -20, -16, -12)
    assert layers['nook']['features'] == []
    field = layers[FIELD]
    assert feature_ids(field) == ['dowel', 'ferrule']
    shapes = {feature['id']: feature['shape']['extent'] for feature in field['features']}
    assert shapes['dowel'] == box(4, 0, 8, 3)
    assert shapes['ferrule'] == box(4, 1, 6, 3)
    assert feature_ids(layers[BED]) == ['shim']
    assert layers[BED]['features'][0]['shape']['extent'] == box(4, 0, 5, 2)
    assert feature_ids(layers[PINS]) == ['brad', 'stud']
    pin_points = {feature['id']: feature['shape']['mark'] for feature in layers[PINS]['features']}
    assert pin_points == {'brad': point(4, 1), 'stud': point(6, 1)}
    assert s.request('GET', '/image?min_x=4&min_y=0&max_x=8&max_y=3')[0] == scoped

    # Layer subset and window combine; empty selected layers are still emitted.
    pins_only, _ = s.request('GET', '/image?layers=plumb-marks&min_x=4&min_y=0&max_x=8&max_y=3')
    pins_image = decode_image(pins_only)
    assert [layer['definition']['name'] for layer in pins_image['layers']] == [PINS]
    void_only, _ = s.request('GET', '/image?layers=void&min_x=4&min_y=0&max_x=8&max_y=3')
    void_layer = decode_image(void_only)['layers'][0]
    assert void_layer['definition']['name'] == 'void' and void_layer['features'] == []

    # Source bounds disjoint from the window keep the source bounds and emit
    # no rows; intersecting layers are reframed to the clipped bounds.
    disjoint, _ = s.request('GET', '/image?layers=nook&layers=ochre-field&min_x=12&min_y=12&max_x=16&max_y=16')
    disjoint_layers = image_layers(decode_image(disjoint))
    assert disjoint_layers['nook']['definition']['bounds'] == box(-20, -20, -16, -12)
    assert disjoint_layers['nook']['features'] == []
    assert disjoint_layers[FIELD]['definition']['bounds'] == box(12, 12, 16, 16)
    assert disjoint_layers[FIELD]['features'] == []

    # The scoped image goes through the existing recovery path unchanged.
    current = s.get('/status')['revision']
    restored = s.restore(scoped, current)
    assert restored['revision'] == current + 1
    names = [layer['name'] for layer in restored['layers']]
    assert names == sorted(layers)
    assert {row['revision'] for row in restored['layers']} == {current + 1}
    audit = s.get(f'/strata/{FIELD}/index-audit')
    assert audit['records'] == 2
    assert ids(s.query(FIELD, 'locate', point(4, 0))) == ['dowel']
    assert ids(s.query(PINS, 'locate', point(8, 0))) == []

    # A writer commit after export cannot be mixed into the frozen image: a
    # stale CAS is rejected and the old view survives, then the image restores.
    s.edit(op(PINS, 'brad', point(4, 2), mode='replace'))
    s.restore(scoped, current, 409)
    assert ids(s.query(PINS, 'locate', point(4, 2))) == ['brad']
    again = s.restore(scoped, s.get('/status')['revision'])
    assert ids(s.query(PINS, 'locate', point(4, 1))) == ['brad']
    assert s.get('/strata')['layers'] and len(again['layers']) == len(layers)

    # Malformed selection requests are rejected before any image is produced.
    s.request('GET', '/image?layers=missing-one', status=404)
    s.request('GET', '/image?layers=Bad-Name', status=400)
    s.request('GET', '/image?layers=ochre-field&layers=ochre-field', status=400)
    s.request('GET', '/image?min_x=4', status=400)
    s.request('GET', '/image?min_x=4&min_y=0&max_x=4&max_y=3', status=400)
    s.request('GET', '/image?min_x=4&min_y=0&max_x=x&max_y=3', status=400)
    return dict(revision=rev, layers=len(scoped_image['layers']))


def restore(s):
    rev = seed(s)
    backup = s.get('/image')
    edited = s.edit(op(BED, 'shim', mode='remove'))['revision']
    s.restore(backup, rev, 409)
    s.restore(backup[:-1] + bytes([backup[-1] ^ 1]), edited, 400)
    assert s.get('/status')['revision'] == edited
    restored = s.restore(backup, edited)
    assert restored['revision'] == edited + 1
    assert {row['revision'] for row in restored['layers']} == {edited + 1}
    assert ids(s.query(BED, 'locate', point(4, 0))) == ['shim']
    return restored


def reopen(binary, data):
    with running(binary, data) as s:
        rev = seed(s)
        token = s.query(FIELD, 'window', dict(bounds=WINDOW, limit=1))['next']
        contender = subprocess.run([str(binary), '-listen', '127.0.0.1:0', '-data', str(data)], capture_output=True, timeout=5)
        assert contender.returncode != 0 and b'acquire data directory' in contender.stderr
    with running(binary, data) as s:
        result = s.get(f'/strata/{FIELD}/index-audit')
        assert result['revision'] == rev and result['records'] == 3
        s.query(FIELD, 'window', dict(bounds=WINDOW, limit=1, bookmark=token), 400)
    state = data / 'meridian-state.mpb'
    state.write_bytes(state.read_bytes()[:-1])
    corrupt = subprocess.run([str(binary), '-listen', '127.0.0.1:0', '-data', str(data)], capture_output=True, timeout=5)
    assert corrupt.returncode != 0 and b'reopen state' in corrupt.stderr
    return result


WORKFLOWS = {f.__name__: f for f in (register, edit, reframe, window, locate, nearest, join, coverage,
                                     inspect, seams, relation, audit, export, export_scope, restore, reopen)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', required=True, choices=WORKFLOWS)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='meridian-walk-') as temporary:
        root = Path(temporary)
        binary, data = root/'meridian', root/'space'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'], check=True, cwd=ROOT, timeout=90)
        if args.workflow == 'reopen':
            result = reopen(binary, data)
        else:
            with running(binary, data) as session:
                result = WORKFLOWS[args.workflow](session)
        print(json.dumps(dict(workflow=args.workflow, observed=result), sort_keys=True))


if __name__ == '__main__':
    main()
