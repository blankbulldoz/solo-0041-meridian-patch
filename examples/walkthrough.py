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
DECK, POCKET = 'swatch-deck', 'pocket-deck'
DECK_BOUNDS = box(-40, -40, 40, 40)
POCKET_BOUNDS = box(-4, -4, 4, 4)
CELLS = box(1, 1, 7, 7)


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

    def layer(self, name, kind='extent', exclusive=False, status=201, bounds=None):
        return self.post('/strata', dict(name=name, kind=kind, bounds=bounds or BOUNDS, exclusive=exclusive), status)

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


def deck(s):
    """Two layouts over one 240-feature layer.

    Tile n sits in a 20x12 grid; its class is deterministic, except for three
    'quartz' tiles that form the rare class. The tiny window CELLS covers only
    tiles 210, 211, 230 and 231.
    """
    s.layer(DECK, bounds=DECK_BOUNDS)
    items = []
    for n in range(240):
        row, col = divmod(n, 20)
        x, y = -40 + col * 4, -40 + row * 4
        category = 'quartz' if n in (5, 210, 231) else f'c{n % 40:02d}'
        items.append(op(DECK, f'tile-{n:03d}', box(x, y, x + 3, y + 3), category))
    s.edit(*items)
    s.layer('blank-bed', 'mark')


def classes(s):
    deck(s)
    assert ids(s.query(DECK, 'window', dict(bounds=CELLS, limit=8))) == \
        ['tile-210', 'tile-211', 'tile-230', 'tile-231']

    # Layout A: a full-library window that allows one rare class. Geometry alone
    # admits every tile, so the class filter cannot shrink the candidate scan.
    rare = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=8, allow_classes=['quartz']))
    assert ids(rare) == ['tile-005', 'tile-210', 'tile-231']
    assert rare['visit'] == dict(nodes=rare['visit']['nodes'], candidates=240)
    assert rare['class_rejected'] == 237

    # Layout B: a tiny window that excludes sixty classes. The long exclude
    # list adds no candidates; only the four geometrically visible tiles are
    # weighed against the predicate.
    absent = [f'absent-{i:02d}' for i in range(60)]
    tight = s.query(DECK, 'window', dict(bounds=CELLS, limit=8,
                                        exclude_classes=absent + ['c11', 'c11']))
    assert ids(tight) == ['tile-210', 'tile-230', 'tile-231']
    assert tight['visit']['candidates'] == 4 and tight['class_rejected'] == 1
    assert tight['classes']['exclude_classes'] == sorted(absent + ['c11'])
    assert tight['classes']['allow_classes'] is None

    # Omitted allow and explicitly empty allow are different predicates.
    omitted = s.query(DECK, 'window', dict(bounds=CELLS, limit=8))
    empty = s.query(DECK, 'window', dict(bounds=CELLS, limit=8, allow_classes=[]))
    assert len(omitted['features']) == 4 and omitted['classes']['allow_classes'] is None
    assert empty['features'] == [] and empty['classes']['allow_classes'] == []
    assert empty['class_rejected'] == 4

    # Allow and exclude compose: "in allow and not in exclude".
    both = s.query(DECK, 'window', dict(bounds=CELLS, limit=8,
                                        allow_classes=['c11', 'quartz'], exclude_classes=['quartz']))
    assert ids(both) == ['tile-211']

    duplicate = s.query(DECK, 'window', dict(bounds=CELLS, limit=8,
                                             allow_classes=['c11', 'quartz']))
    assert duplicate['classes'] == dict(allow_classes=['c11', 'quartz'], exclude_classes=[])

    # The legacy single class is the one-item allow list and still composes
    # with an exclude list.
    legacy = s.query(DECK, 'window', dict(bounds=CELLS, limit=8,
                                          **{'class': 'quartz'}, exclude_classes=['quartz']))
    assert legacy['features'] == [] and legacy['classes']['allow_classes'] == []

    s.query(DECK, 'window', dict(bounds=CELLS, limit=8, allow_classes=['quartz'], **{'class': 'quartz'}), 400)
    s.query(DECK, 'window', dict(bounds=CELLS, limit=8, allow_classes=['Bad']), 400)
    s.query(DECK, 'window', dict(bounds=CELLS, limit=8, allow_classes=None), 400)
    s.query(DECK, 'window', dict(bounds=CELLS, limit=8,
                                 allow_classes=[f'k{i:03d}' for i in range(257)]), 400)

    # A long exclude list also stays inside the bookmark: the bound value is a
    # digest of the canonical predicate, so the continuation works and page
    # size can change without repeating or skipping an ID.
    long_first = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=2, exclude_classes=absent))
    assert len(long_first['features']) == 2 and long_first['next']
    assert len(long_first['next']) < 300
    long_next = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=7,
                                            exclude_classes=list(reversed(absent)) + absent[:1],
                                            bookmark=long_first['next']))
    assert [row['id'] for row in long_next['features']] == [f'tile-{n:03d}' for n in range(2, 9)]
    assert all(row['class'] != 'quartz' or row['id'] in ('tile-005', 'tile-210', 'tile-231')
               for row in long_next['features'])
    tampered = long_first['next'][:-1] + ('A' if long_first['next'][-1] != 'A' else 'B')
    s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=2, exclude_classes=absent,
                                 bookmark=tampered), 400)

    # A bookmark can be continued with the same predicate spelled differently,
    # and a changed page size must not repeat or skip an ID.
    first = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=5,
                                         allow_classes=['c03', 'c00', 'c01', 'c00']))
    assert first['classes']['allow_classes'] == ['c00', 'c01', 'c03']
    s.edit(op('blank-bed', 'plinth', point(0, 0)))
    second = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=2,
                                         allow_classes=['c01', 'c03', 'c00', 'c01'],
                                         bookmark=first['next']))
    assert len(second['features']) == 2 and second['next']
    third = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=9,
                                        allow_classes=['c00', 'c03', 'c01'],
                                        bookmark=second['next']))
    seen = ids(first) + ids(second) + ids(third)
    assert all(a < b for a, b in zip(seen, seen[1:])) and len(seen) == len(set(seen))
    assert {row['class'] for row in first['features'] + second['features'] + third['features']} <= {'c00', 'c01', 'c03'}

    # Changing the predicate itself rejects the continuation. An exclude entry
    # that removes nothing is not a change, so the same bookmark still works.
    equivalent = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=2,
                                             allow_classes=['c09', 'c03', 'c00', 'c01'],
                                             exclude_classes=['c09'],
                                             bookmark=first['next']))
    assert equivalent['features'] == second['features']

    # The legacy class field and a one-item allow list are the same predicate,
    # so a bookmark crosses the spelling change but not a window change.
    legacy_first = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=1, **{'class': 'c00'}))
    legacy_next = s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=4,
                                              allow_classes=['c00'], bookmark=legacy_first['next']))
    assert all(row['class'] == 'c00' for row in legacy_next['features'])
    s.query(DECK, 'window', dict(bounds=CELLS, limit=1, allow_classes=['c00'],
                                 bookmark=legacy_first['next']), 400)

    s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=5,
                                 allow_classes=['c00', 'c01'],
                                 bookmark=first['next']), 400)
    s.query(DECK, 'window', dict(bounds=DECK_BOUNDS, limit=5,
                                 allow_classes=['c00', 'c01', 'c03'],
                                 exclude_classes=['c03'],
                                 bookmark=first['next']), 400)

    # A same-layer write expires the bookmark even when it touches only a tile
    # outside the tiny window. Other layers do not.
    small = s.query(DECK, 'window', dict(bounds=CELLS, limit=1))
    assert ids(small) == ['tile-210']
    s.edit(op('blank-bed', 'plinth', point(1, 1), mode='replace'))
    continued = s.query(DECK, 'window', dict(bounds=CELLS, limit=3, bookmark=small['next']))
    assert ids(continued) == ['tile-211', 'tile-230', 'tile-231']
    s.edit(op(DECK, 'tile-000', box(-40, -40, -37, -37), 'c05', mode='replace'))
    expired = s.query(DECK, 'window', dict(bounds=CELLS, limit=1, bookmark=small['next']), 409)
    assert expired['code'] == 'conflict'

    return dict(
        rare=dict(features=ids(rare), candidates=rare['visit']['candidates'],
                  class_rejected=rare['class_rejected'], nodes=rare['visit']['nodes']),
        tight=dict(features=ids(tight), candidates=tight['visit']['candidates'],
                   class_rejected=tight['class_rejected'], nodes=tight['visit']['nodes'],
                   excluded=len(tight['classes']['exclude_classes'])),
        omitted_allows=len(omitted['features']), empty_allows=len(empty['features']),
        composed=ids(both), bookmark_pages=len(seen), expired=expired['code'],
    )


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


WORKFLOWS = {f.__name__: f for f in (register, edit, reframe, window, classes, locate, nearest, join, coverage,
                                     inspect, seams, relation, audit, export, restore, reopen)}


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
