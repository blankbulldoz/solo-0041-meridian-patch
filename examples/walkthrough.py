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


def distribution(s):
    seed(s)
    query = dict(bounds=WINDOW, origin=point(0, 0), cell=4)
    result = s.query(FIELD, 'distribution', query)
    assert result['empty_digest'] == hashlib.sha256(b'').hexdigest()
    cells = {(cell['column'], cell['row']): cell for cell in result['cells']}
    assert sorted(cells) == [(0, 0), (1, 0)]
    assert cells[(0, 0)]['bounds'] == box(0, 0, 4, 4) and cells[(0, 0)]['count'] == 2
    assert cells[(1, 0)]['count'] == 2 and cells[(1, 0)]['digest'] != cells[(0, 0)]['digest']
    # Digests cover only class plus clipped geometry: the fixture values below
    # pin the canonical encoding so independent processes encode identically.
    assert cells[(0, 0)]['digest'] == 'bbf85a47a7beeead0cd44d250dc21a73281b9edfc774a97ef3501703014814a1'
    assert cells[(1, 0)]['digest'] == 'cbc0d0d9a13e33dba8656b7348af03314695346abf8508d897db9e6acd88d605'
    assert s.query(FIELD, 'distribution', query) == result

    # Another layer's write advances the whole-database revision while the
    # layer revision and every per-cell digest stay identical.
    s.edit(op(BED, 'shim', mode='remove'))
    later = s.query(FIELD, 'distribution', query)
    assert later['revision'] > result['revision']
    assert [cell['digest'] for cell in later['cells']] == [cell['digest'] for cell in result['cells']]

    # Removing and re-inserting identical geometry changes the layer revision
    # but not the spatial content, so the cells remain comparable.
    s.edit(op(FIELD, 'prong', mode='remove'))
    s.edit(op(FIELD, 'prong', box(0, 0, 4, 3)))
    again = s.query(FIELD, 'distribution', query)
    assert again['layer_revision'] > result['layer_revision']
    assert again['cells'] == result['cells'] and again['digest'] == result['digest']

    # A duplicate geometry under another ID changes count and digest.
    s.edit(op(FIELD, 'quinion', box(0, 0, 4, 3)))
    duplicated = s.query(FIELD, 'distribution', query)
    assert duplicated['cells'][0]['count'] == 3
    assert duplicated['cells'][0]['digest'] != result['cells'][0]['digest']
    s.edit(op(FIELD, 'quinion', mode='remove'))

    # A category change alone must change the digest of the touched cell.
    s.edit(op(FIELD, 'dowel', box(4, 0, 8, 3), 'sleeve', mode='replace'))
    recoloured = s.query(FIELD, 'distribution', query)
    assert recoloured['cells'][1]['digest'] != result['cells'][1]['digest']
    assert recoloured['cells'][1]['count'] == result['cells'][1]['count']

    # Marks use half-open single-cell ownership; empty cells are explicit.
    marks = s.query(PINS, 'distribution', dict(bounds=WINDOW, origin=point(0, 0), cell=2))
    mark_cells = {(cell['column'], cell['row']): cell for cell in marks['cells']}
    assert len(mark_cells) == 8
    assert (mark_cells[(2, 0)]['count'], mark_cells[(3, 0)]['count']) == (1, 1)
    assert mark_cells[(0, 0)]['count'] == 0
    assert mark_cells[(0, 0)]['digest'] == marks['empty_digest']

    # An empty window has a fixed expression: every cell equals empty_digest.
    empty = s.query(FIELD, 'distribution', dict(bounds=box(10, 10, 12, 12), origin=point(0, 0), cell=1))
    assert len(empty['cells']) == 4
    assert {cell['digest'] for cell in empty['cells']} == {empty['empty_digest']}
    assert [cell['count'] for cell in empty['cells']] == [0, 0, 0, 0]

    # A rectangle crossing a cell edge is clipped separately in each cell, and
    # cells below the origin use the same half-open lattice (negative columns).
    crossing = s.query(FIELD, 'distribution', dict(bounds=WINDOW, origin=point(1, 0), cell=4))
    assert [cell['column'] for cell in crossing['cells']] == [-1, 0, 1]
    assert [cell['count'] for cell in crossing['cells']] == [1, 3, 2]

    s.query(FIELD, 'distribution', dict(bounds=WINDOW, origin=point(0, 0), cell=-1), 400)
    s.query(FIELD, 'distribution', dict(bounds=box(0, 0, 1000, 1000), origin=point(0, 0), cell=1), 422)
    s.query(FIELD, 'distribution', dict(bounds=WINDOW, origin=point(0, 0), cell=4, **{'class': 'sleeve'}))
    s.query(FIELD, 'distribution', dict(bounds=box(0, 0, 200, 100), origin=point(0, 0)), 422)
    unit = s.query(FIELD, 'distribution', dict(bounds=box(0, 0, 3, 3), origin=point(0, 0)))
    assert unit['grid']['cell'] == 1 and len(unit['cells']) == 9
    s.query('missing', 'distribution', query, 404)
    return result


def crossprocess(binary, data):
    query = dict(bounds=WINDOW, origin=point(0, 0), cell=4)
    with running(binary, data/'origin') as first:
        seed(first)
        backup = first.get('/image')
        expected = first.query(FIELD, 'distribution', query)
        first.edit(op(BED, 'shim', mode='remove'))
        advanced = first.query(FIELD, 'distribution', query)
        assert advanced['revision'] > expected['revision']
        assert [cell['digest'] for cell in advanced['cells']] == [cell['digest'] for cell in expected['cells']]
    # A separate process at a different revision, restoring the same image,
    # reproduces every cell digest byte-for-byte.
    with running(binary, data/'replica') as second:
        assert second.restore(backup, 0)['revision'] == 1
        replica = second.query(FIELD, 'distribution', query)
        assert replica['revision'] != expected['revision']
        assert replica['revision'] != advanced['revision']
        assert replica['cells'] == expected['cells']
        assert replica['empty_digest'] == expected['empty_digest']
        return dict(origin_revision=expected['revision'], advanced_revision=advanced['revision'],
                    replica_revision=replica['revision'], cells=len(replica['cells']),
                    digest=replica['digest'])


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
                                     distribution, inspect, seams, relation, audit, export, restore, reopen,
                                     crossprocess)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', required=True, choices=WORKFLOWS)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='meridian-walk-') as temporary:
        root = Path(temporary)
        binary, data = root/'meridian', root/'space'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'], check=True, cwd=ROOT, timeout=90)
        if args.workflow in ('reopen', 'crossprocess'):
            result = WORKFLOWS[args.workflow](binary, data)
        else:
            with running(binary, data) as session:
                result = WORKFLOWS[args.workflow](session)
        print(json.dumps(dict(workflow=args.workflow, observed=result), sort_keys=True))


if __name__ == '__main__':
    main()
