#!/usr/bin/env python3
"""Exercise the normal HTTP surface in a clean temporary database."""
import argparse
from contextlib import contextmanager
import hashlib
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

    def restore(self, raw, revision, status=200, media=IMAGE):
        return self.request('PUT', '/image', raw, status, {
            'Content-Type': media, 'If-Meridian-Revision': str(revision),
        })


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
    assert first.startswith(b'MRDNMPB\x02')
    assert headers['Meridian-Image-Version'] == '2' and len(first) == int(headers['Content-Length'])
    assert s.get('/image') == first
    legacy, legacy_headers = s.request('GET', '/image?version=1')
    assert legacy.startswith(b'MRDNMPB\x01') and legacy_headers['Meridian-Image-Version'] == '1'
    assert s.request('GET', '/image', headers={'Accept': 'application/vnd.meridian.image.v1'})[0] == legacy
    assert legacy != first
    s.edit(op(BED, 'shim', mode='remove'))
    assert s.get('/image') != first
    return dict(revision=rev, bytes=len(first), legacy_bytes=len(legacy))


def _geometry(s):
    """A comparable read used by the image-format workflows.

    Revisions are dropped: they are expected to differ after a restore, while
    features, ordering, distances and areas must be identical.
    """
    def stable(value):
        if isinstance(value, dict):
            return {k: stable(v) for k, v in sorted(value.items())
                    if k not in ('revision', 'layer_revision')}
        if isinstance(value, list):
            return [stable(v) for v in value]
        return value

    return stable(dict(
        field=s.query(FIELD, 'window', dict(bounds=WINDOW, limit=8))['features'],
        bed=s.query(BED, 'locate', point(4, 0))['features'],
        coverage=s.query(FIELD, 'coverage', dict(bounds=WINDOW)),
        nearest=s.query(FIELD, 'nearest', dict(point=point(4, 0), limit=2))['neighbors'],
    ))


def imagecompat(s):
    # An empty database is a valid image in both versions.
    empty = s.get('/image')
    empty_legacy = s.get('/image?version=1')
    assert empty.startswith(b'MRDNMPB\x02') and empty_legacy.startswith(b'MRDNMPB\x01')
    restored, empty_headers = s.restore(empty, 0)
    assert empty_headers['Meridian-Image-Version'] == '2'
    assert restored['revision'] == 1 and restored['layers'] == []

    rev = seed(s)
    before = _geometry(s)
    legacy, headers = s.request('GET', '/image?version=1')
    current, current_headers = s.request('GET', '/image?version=2')
    assert legacy.startswith(b'MRDNMPB\x01') and headers['Meridian-Image-Version'] == '1'
    assert current.startswith(b'MRDNMPB\x02') and current_headers['Meridian-Image-Version'] == '2'
    assert legacy != current

    # Legacy deliveries are deterministic and selectable without a query too.
    assert s.request('GET', '/image', headers={
        'Accept': 'application/vnd.meridian.image.v1'})[0] == legacy
    s.request('GET', '/image?version=3', status=400)
    s.request('GET', '/image?version=1&version=2', status=400)
    s.request('GET', '/image', status=406, headers={'Accept': 'application/zip'})
    assert s.request('GET', '/image', headers={
        'Accept': 'application/vnd.meridian.image.v1, application/vnd.meridian.image'})[0] == current

    # Import both containers into the same database and compare geometry.
    edited = s.edit(op(BED, 'shim', mode='remove'))['revision']
    first, first_headers = s.restore(legacy, edited, media='application/vnd.meridian.image.v1')
    assert first_headers['Meridian-Image-Version'] == '1'
    assert first['revision'] == edited + 1
    assert {row['revision'] for row in first['layers']} == {edited + 1}
    assert _geometry(s) == before

    edited = s.edit(op(BED, 'shim', mode='remove'))['revision']
    second, second_headers = s.restore(current, edited)
    assert second_headers['Meridian-Image-Version'] == '2'
    assert second['revision'] == edited + 1
    assert {row['revision'] for row in second['layers']} == {edited + 1}
    assert _geometry(s) == before
    return dict(legacy_revision=first['revision'], current_revision=second['revision'],
                legacy_bytes=len(legacy), current_bytes=len(current))


def parse_v2(raw):
    """Split an MPB v2 container into revision and independently checkable segments."""
    assert raw[:8] == b'MRDNMPB\x02'
    payload = raw[44:]
    revision = int.from_bytes(payload[0:8], 'big')
    count = int.from_bytes(payload[8:12], 'big')
    segments, offset = [], 12
    for index in range(count):
        name_len = int.from_bytes(payload[offset:offset+4], 'big')
        body_len = int.from_bytes(payload[offset+4:offset+8], 'big')
        digest = payload[offset+8:offset+40]
        start = offset + 40
        name = payload[start:start+name_len]
        body_start = start + name_len
        body = payload[body_start:body_start+body_len]
        offset = body_start + body_len
        segments.append(dict(index=index + 1, name=name.decode(), body=body,
                             digest=digest, body_start=body_start,
                             valid=hashlib.sha256(name + b'\0' + body).digest() == digest))
    assert offset == len(payload)
    return revision, segments


def build_v2(revision, layers):
    """Assemble an MPB v2 container from (name, canonical body) pairs."""
    payload = revision.to_bytes(8, 'big') + len(layers).to_bytes(4, 'big')
    for name, body in layers:
        encoded = name.encode()
        payload += (len(encoded).to_bytes(4, 'big') + len(body).to_bytes(4, 'big')
                    + hashlib.sha256(encoded + b'\0' + body).digest() + encoded + body)
    return (b'MRDNMPB\x02' + len(payload).to_bytes(4, 'big')
            + hashlib.sha256(payload).digest() + payload)


def imagerecover(binary, data):
    with running(binary, data) as s:
        rev = seed(s)
        before = _geometry(s)
        raw, headers = s.request('GET', '/image')
        assert headers['Meridian-Image-Version'] == '2'
        revision, segments = parse_v2(raw)
        assert revision == rev and [s['name'] for s in segments] == sorted([BED, FIELD, PINS])
        assert all(s['valid'] for s in segments)

        # Corrupt only the final layer's body. Every earlier segment still
        # verifies, yet the whole image must be refused and the live view kept.
        damaged = bytearray(raw)
        target = segments[-1]
        damaged[44 + target['body_start'] + 3] ^= 1
        damaged = bytes(damaged)
        revised, parsed = parse_v2(damaged)
        assert revised == rev and [s['valid'] for s in parsed] == [True, True, False]

        error, _ = s.restore(damaged, rev, status=400)
        assert error['code'] == 'invalid'
        assert (error['image_version'], error['segment'], error['layer']) == (
            '2', len(segments), target['name'])
        assert s.get('/status')['revision'] == rev and _geometry(s) == before

        # Truncating the damaged tail is refused too, not partially installed.
        s.restore(damaged[:-1], rev, status=400)
        assert s.get('/status')['revision'] == rev and _geometry(s) == before

        # A well-checksummed segment can still violate geometry rules. The
        # damaged layer is still named and nothing replaces the live view.
        bodies = [(row['name'], row['body']) for row in segments]
        name, body = bodies[-1]
        bodies[-1] = (name, body.replace(b'"x":6', b'"x":600', 1))
        assert bodies[-1][1] != body
        semantic = build_v2(rev, bodies)
        assert all(row['valid'] for row in parse_v2(semantic)[1])
        error, _ = s.restore(semantic, rev, status=400)
        assert error['code'] == 'invalid' and error['layer'] == name
        assert b'outside' in error['message'].encode()
        assert s.get('/status')['revision'] == rev and _geometry(s) == before

    # Import the same damaged image into a genuinely empty database: the two
    # intact leading layers must not appear as a partial installation.
    with tempfile.TemporaryDirectory(prefix='meridian-target-') as target_dir:
        with running(binary, Path(target_dir)) as blank:
            assert blank.get('/status')['revision'] == 0
            for payload in (damaged, semantic):
                blank.restore(payload, 0, status=400)
                assert blank.get('/status')['revision'] == 0
                assert blank.get('/strata')['layers'] == []
    return dict(revision=rev, segments=len(segments), damaged_layer=target['name'],
                intact_before_damage=sum(row['valid'] for row in parsed))


def imagemigrate(binary, data):
    with running(binary, data) as s:
        rev = seed(s)
        before = _geometry(s)
        legacy = s.get('/image?version=1')
        assert legacy.startswith(b'MRDNMPB\x01')
    state = data / 'meridian-state.mpb'
    state.write_bytes(legacy)
    with running(binary, data) as s:
        assert s.get('/status')['revision'] == rev
        assert _geometry(s) == before
        assert s.get('/image?version=1') == legacy
    assert state.read_bytes()[:8] == b'MRDNMPB\x02'
    with running(binary, data) as s:
        assert s.get('/status')['revision'] == rev and _geometry(s) == before
    state.write_bytes(legacy[:-1])
    broken = subprocess.run([str(binary), '-listen', '127.0.0.1:0', '-data', str(data)],
                            capture_output=True, timeout=5)
    assert broken.returncode != 0 and b'reopen state' in broken.stderr
    return dict(revision=rev, upgraded_version=2, legacy_bytes=len(legacy))


def restore(s):
    rev = seed(s)
    backup = s.get('/image')
    edited = s.edit(op(BED, 'shim', mode='remove'))['revision']
    s.restore(backup, rev, 409)
    s.restore(backup[:-1] + bytes([backup[-1] ^ 1]), edited, 400)
    assert s.get('/status')['revision'] == edited
    restored, headers = s.request('PUT', '/image', backup, 200, {
        'Content-Type': IMAGE, 'If-Meridian-Revision': str(edited)})
    assert headers['Meridian-Image-Version'] == '2'
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
                                     inspect, seams, relation, audit, export, imagecompat, restore) if not f.__name__.startswith('_')}

SEPARATE = ('reopen', 'imagerecover', 'imagemigrate')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', required=True, choices=sorted(set(WORKFLOWS) | set(SEPARATE)))
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='meridian-walk-') as temporary:
        root = Path(temporary)
        binary, data = root/'meridian', root/'space'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'], check=True, cwd=ROOT, timeout=90)
        if args.workflow in SEPARATE:
            result = (reopen if args.workflow == 'reopen' else
                      imagerecover if args.workflow == 'imagerecover' else imagemigrate)(binary, data)
        else:
            with running(binary, data) as session:
                result = WORKFLOWS[args.workflow](session)
        print(json.dumps(dict(workflow=args.workflow, observed=result), sort_keys=True))


if __name__ == '__main__':
    main()
