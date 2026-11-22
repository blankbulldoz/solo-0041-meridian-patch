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
GRID = 'gridded-blocks'


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


def seam_partitions(s):
    s.layer(GRID)
    s.edit(
        # rail is one long edge against three stacked short blocks.
        op(GRID, 'rail', box(0, 0, 2, 9)),
        op(GRID, 'short-a', box(2, 0, 4, 3)),
        op(GRID, 'short-b', box(2, 3, 4, 6)),
        op(GRID, 'short-c', box(2, 6, 4, 9)),
        # A chain whose middle link is outside the first window, so the window
        # cuts the originally complete connection into two partitions.
        op(GRID, 'link-l', box(4, 0, 6, 3)),
        op(GRID, 'link-m', box(6, 0, 8, 3)),
        op(GRID, 'link-r', box(8, 0, 10, 3)),
        # Corner-only contact with short-c: never connects.
        op(GRID, 'corner', box(4, 9, 6, 11)),
        # Fully isolated feature inside the layer bounds.
        op(GRID, 'loner', box(12, 12, 14, 14)),
    )

    full = s.query(GRID, 'partitions', dict(bounds=box(0, 0, 16, 16)))
    by_members = {tuple(part['members']): part for part in full['partitions']}
    assert list(by_members) == [
        ('corner',),
        ('link-l', 'link-m', 'link-r', 'rail', 'short-a', 'short-b', 'short-c'),
        ('loner',),
    ]
    chain = by_members[('link-l', 'link-m', 'link-r', 'rail', 'short-a', 'short-b', 'short-c')]
    # Seams sort by ID pair; the long rail proves adjacency to every short
    # block, and every other shared edge completes the connectivity proof.
    assert chain['seams'] == [
        dict(first='link-l', second='link-m', seam=dict(axis='vertical', fixed=6, **{'from': 0}, to=3)),
        dict(first='link-l', second='short-a', seam=dict(axis='vertical', fixed=4, **{'from': 0}, to=3)),
        dict(first='link-m', second='link-r', seam=dict(axis='vertical', fixed=8, **{'from': 0}, to=3)),
        dict(first='rail', second='short-a', seam=dict(axis='vertical', fixed=2, **{'from': 0}, to=3)),
        dict(first='rail', second='short-b', seam=dict(axis='vertical', fixed=2, **{'from': 3}, to=6)),
        dict(first='rail', second='short-c', seam=dict(axis='vertical', fixed=2, **{'from': 6}, to=9)),
        dict(first='short-a', second='short-b', seam=dict(axis='horizontal', fixed=3, **{'from': 2}, to=4)),
        dict(first='short-b', second='short-c', seam=dict(axis='horizontal', fixed=6, **{'from': 2}, to=4)),
    ]
    assert by_members[('corner',)]['seams'] == [] and by_members[('loner',)]['seams'] == []

    # A window ending at x=6 (open) cuts the chain: link-r drops out entirely
    # and the seam at x=6 between link-l and link-m is on the excluded side, so
    # link-m becomes a singleton while the rail component stays intact.
    clipped = s.query(GRID, 'partitions', dict(bounds=box(0, 0, 6, 12)))
    clipped_groups = [tuple(part['members']) for part in clipped['partitions']]
    assert clipped_groups == [
        ('corner',),
        ('link-l', 'rail', 'short-a', 'short-b', 'short-c'),
        ('link-m',),
    ]
    singleton = next(part for part in clipped['partitions'] if part['members'] == ['link-m'])
    assert singleton['seams'] == []
    rail_group = next(part for part in clipped['partitions'] if 'rail' in part['members'])
    assert all(seam['first'] != 'link-l' or seam['second'] != 'link-m'
               for seam in rail_group['seams'])
    # link-m still meets the closed window envelope at x=6, proving inclusion
    # comes from the seam rule rather than from positive-area window overlap.
    assert ('link-m',) in clipped_groups

    # Recomputing on the same layer revision and window is byte-for-byte stable.
    again = s.query(GRID, 'partitions', dict(bounds=box(0, 0, 16, 16)))
    assert again == full
    s.layer(PINS, 'mark', True)
    s.query(PINS, 'partitions', dict(bounds=WINDOW), 400)
    s.query(GRID, 'partitions', dict(bounds=box(4, 4, 4, 8)), 400)
    return full


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
                                     inspect, seams, seam_partitions, relation, audit, export, restore, reopen)}


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
