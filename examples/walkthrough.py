#!/usr/bin/env python3
"""Exercise the normal HTTP surface in a clean temporary database."""
import argparse
from contextlib import contextmanager
import json
from pathlib import Path
import select
import subprocess
import tempfile
import time
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
SESSION = 'Meridian-Session'
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

    def post(self, path, value, status=200, headers=None):
        return self.request('POST', path, value, status, headers)[0]

    def query(self, layer, rule, value, status=200, headers=None):
        return self.post(f'/strata/{layer}/{rule}', value, status, headers)

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
def running(binary, data, extra=()):
    args = [str(binary), '-listen', '127.0.0.1:0', '-data', str(data), *extra]
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


def review(binary, data):
    with running(binary, data, ['-review-max', '2', '-review-ttl', '30s']) as s:
        rev = seed(s)
        opened = s.post('/reviews', {}, status=201)
        token = opened['session']
        assert opened['revision'] == rev and time.time() < opened['expires_at'] <= time.time() + 30
        head = {SESSION: token}
        query = dict(bounds=WINDOW, limit=1)
        first = s.query(FIELD, 'window', query, headers=head)
        assert ids(first) == ['dowel'] and first['revision'] == rev and first['next']
        # writes between session reads move the head but not the pinned view
        s.edit(op(FIELD, 'dowel', mode='remove'))
        s.edit(op(BED, 'shim', mode='remove'))
        assert s.get('/status')['revision'] == rev + 2
        second = s.query(FIELD, 'window', dict(query, limit=2, bookmark=first['next']), headers=head)
        assert ids(second) == ['ferrule', 'prong'] and 'next' not in second
        assert second['revision'] == rev and second['layer_revision'] == first['layer_revision']
        # without the session the same bookmark expired with the layer revision
        s.query(FIELD, 'window', dict(query, bookmark=first['next']), status=409)
        assert s.query(FIELD, 'window', query)['revision'] == rev + 2
        # session locate and join still see the pinned features
        assert ids(s.query(FIELD, 'locate', point(4, 0), headers=head)) == ['dowel']
        assert ids(s.query(FIELD, 'locate', point(4, 0))) == []
        join_query = dict(left=FIELD, right=BED, bounds=box(4, 0, 8, 4), limit=10)
        pinned = s.post('/join', join_query, headers=head)
        assert pinned['revision'] == rev
        assert pinned['pairs'] == [dict(left='dowel', right='shim', intersection=box(4, 0, 5, 2)),
                                   dict(left='ferrule', right='shim', intersection=box(4, 1, 5, 2))]
        assert s.post('/join', join_query)['pairs'] == []
        # layers created after the session opened are absent from its view
        s.layer('late-layer')
        s.query('late-layer', 'locate', point(0, 0), status=404, headers=head)
        assert ids(s.query('late-layer', 'locate', point(0, 0))) == []
        # a session opened later pins the new view
        head_rev = s.get('/status')['revision']
        later = s.post('/reviews', {}, status=201)
        assert later['revision'] == head_rev
        assert ids(s.query(FIELD, 'locate', point(4, 0), headers={SESSION: later['session']})) == []
        # capacity: two live sessions, a third is refused until one closes
        assert s.post('/reviews', {}, status=422)['code'] == 'capacity'
        closed = s.request('DELETE', f'/reviews/{token}', status=200)[0]
        assert closed == dict(session=token, closed=True)
        assert s.post('/reviews', {}, status=201)['revision'] == head_rev
        # closed or unknown sessions report one explicit terminal result
        assert s.query(FIELD, 'window', query, status=410, headers=head)['code'] == 'session_gone'
        assert s.request('DELETE', f'/reviews/{token}', status=410)[0]['code'] == 'session_gone'
        assert s.query(FIELD, 'locate', point(4, 0), status=410, headers={SESSION: 'not-a-session'})['code'] == 'session_gone'
    with running(binary, data, ['-review-ttl', '1s']) as s:
        assert s.get('/status')['revision'] == head_rev
        expiring = s.post('/reviews', {}, status=201)
        head = {SESSION: expiring['session']}
        assert ids(s.query(FIELD, 'locate', point(2, 1), headers=head)) == ['ferrule', 'prong']
        time.sleep(1.4)
        # expiry gives the same clear result and frees capacity for new sessions
        assert s.query(FIELD, 'locate', point(2, 1), status=410, headers=head)['code'] == 'session_gone'
        assert s.request('DELETE', f"/reviews/{expiring['session']}", status=410)[0]['code'] == 'session_gone'
        assert s.post('/reviews', {}, status=201)['revision'] == head_rev
    return dict(pinned_revision=rev, head_revision=head_rev)


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
                                     inspect, seams, relation, audit, export, restore, reopen, review)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', required=True, choices=WORKFLOWS)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='meridian-walk-') as temporary:
        root = Path(temporary)
        binary, data = root/'meridian', root/'space'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'], check=True, cwd=ROOT, timeout=90)
        detached = dict(reopen=reopen, review=review)
        if args.workflow in detached:
            result = detached[args.workflow](binary, data)
        else:
            with running(binary, data) as session:
                result = WORKFLOWS[args.workflow](session)
        print(json.dumps(dict(workflow=args.workflow, observed=result), sort_keys=True))


if __name__ == '__main__':
    main()
