#!/usr/bin/env python3
"""Exercise the normal HTTP surface in a clean temporary database."""
import argparse
from contextlib import contextmanager
import base64
import json
from pathlib import Path
import select
import subprocess
import tempfile
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from hashlib import sha256

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


def transplant(s):
    """Move a clipped, translated region of a stored backup into the live catalog."""
    source = box(-50, -50, 50, 50)
    target = box(-200, -200, 200, 200)
    window = box(0, 0, 8, 4)
    offset = dict(x=100, y=50)

    # The backup carries two source layers. One extent straddles the window edge,
    # one touches it only along x=8, and one sits entirely outside.
    for name, kind, exclusive in (
        ('quarry-field', 'extent', False),
        ('quarry-pins', 'mark', False),
        ('swap-field', 'extent', False),
    ):
        s.post('/strata', dict(name=name, kind=kind, bounds=source, exclusive=exclusive), 201)
    s.edit(
        op('quarry-field', 'prong', box(0, 0, 4, 2)),
        op('quarry-field', 'dowel', box(4, 0, 8, 2)),
        op('quarry-field', 'ferrule', box(6, 2, 8, 4), 'sleeve'),
        op('quarry-field', 'ledger', box(-3, 2, 3, 4)),
        op('quarry-field', 'outside-right', box(8, 0, 10, 2)),
        op('quarry-field', 'outlier', box(20, 20, 24, 24)),
        op('quarry-pins', 'brad', point(4, 1)),
        op('quarry-pins', 'stud', point(6, 1)),
        op('quarry-pins', 'rim', point(8, 1)),
        op('swap-field', 'left', box(2, 0, 4, 2)),
        op('swap-field', 'right', box(0, 0, 2, 2)),
    )
    backup, headers = s.request('GET', '/image')
    backup_revision = int(headers['Meridian-Revision'])
    encoded = base64.b64encode(backup).decode()

    # The live targets already hold data before the transplant.
    for name, kind in (('mosaic-field', 'extent'), ('pin-field', 'mark'),
                       ('lattice-field', 'extent'), ('drift-field', 'extent'),
                       ('swap-target', 'extent')):
        s.post('/strata', dict(name=name, kind=kind, bounds=target, exclusive=True), 201)
    s.post('/strata', dict(name='spill-field', kind='extent', bounds=target, exclusive=False), 201)
    s.edit(op('mosaic-field', 'tile', box(10, 10, 12, 12)))

    def summary(name):
        return next(row for row in s.get('/strata')['layers'] if row['name'] == name)

    def preview(**overrides):
        request = dict(source=encoded, source_layer='quarry-field', target='mosaic-field',
                       bounds=window, offset=offset, mode='insert')
        request.update(overrides)
        return s.post('/transplant/preview', request)

    # Half-open window selection: the touching rectangle is rejected, the
    # straddling rectangle is clipped before translation, and both move by the
    # integer offset. Feature ids travel unchanged.
    first = preview()
    assert first['conflicts'] == [] and first['plan']
    assert first['excluded'] == dict(marks=0, extents=1)
    assert first['source_revision'] == backup_revision and first['source_digest'] == sha256(backup).hexdigest()
    assert [f['id'] for f in first['features']] == ['dowel', 'ferrule', 'ledger', 'prong']
    moved = {f['id']: f['shape']['extent'] for f in first['features']}
    assert moved == {
        'prong': box(100, 50, 104, 52),
        'dowel': box(104, 50, 108, 52),
        'ferrule': box(106, 52, 108, 54),
        'ledger': box(100, 52, 103, 54),  # clipped from (-3,2,3,4) before translation
    }

    # A plan is pinned to the target layer revision, so an unrelated layer's
    # commit between preview and execute does not invalidate it.
    s.edit(op('spill-field', 'grain', box(0, 0, 1, 1)))
    committed = s.post('/transplant/commit', dict(plan=first['plan']))
    assert committed['transplanted'] == 4 and committed['source_digest'] == first['source_digest']
    assert committed['target'] == 'mosaic-field' and committed['layers'][0]['count'] == 5
    assert ids(s.query('mosaic-field', 'locate', point(101, 51))) == ['prong']
    assert ids(s.query('mosaic-field', 'locate', point(105, 51))) == ['dowel']
    assert ids(s.query('mosaic-field', 'locate', point(11, 11))) == ['tile']

    # Marks use the same half-open ownership rule: a pin exactly on the right
    # edge is owned by neither the window nor the translated batch.
    pin = preview(source_layer='quarry-pins', target='pin-field')
    assert pin['excluded'] == dict(marks=1, extents=0) and pin['conflicts'] == []
    assert [f['shape']['mark'] for f in pin['features']] == [point(104, 51), point(106, 51)]
    assert s.post('/transplant/commit', dict(plan=pin['plan']))['transplanted'] == 2
    assert ids(s.query('pin-field', 'locate', point(104, 51))) == ['brad']

    # Insert refuses ids that already exist; replace refuses ids that do not.
    duplicate = preview(target='pin-field', source_layer='quarry-pins')
    assert [c['kind'] for c in duplicate['conflicts']] == ['duplicate'] and 'plan' not in duplicate
    missing = preview(target='lattice-field', mode='replace')
    assert [c['kind'] for c in missing['conflicts']] == ['missing'] and 'plan' not in missing

    # The translated region must still fit the target bounds and exclusivity.
    overshoot = preview(target='lattice-field', offset=dict(x=1000, y=1000))
    assert [c['kind'] for c in overshoot['conflicts']] == ['bounds'] and 'plan' not in overshoot
    s.edit(op('lattice-field', 'blocker', box(102, 50, 106, 52)))
    overlap = preview(target='lattice-field')
    assert [c['kind'] for c in overlap['conflicts']] == ['exclusive'] and 'plan' not in overlap
    assert overlap['conflicts'][0]['ids'] == ['blocker', 'dowel']

    # Final-state exclusivity: exchanging two rectangles' shapes between ids is
    # accepted even though each new shape overlaps its own old footprint.
    s.edit(op('swap-target', 'left', box(0, 0, 2, 2)), op('swap-target', 'right', box(2, 0, 4, 2)))
    swap = preview(source_layer='swap-field', target='swap-target', bounds=box(0, 0, 4, 2),
                   offset=dict(x=0, y=0), mode='replace')
    assert swap['conflicts'] == [] and swap['plan']
    assert s.post('/transplant/commit', dict(plan=swap['plan']))['transplanted'] == 2
    assert ids(s.query('swap-target', 'locate', point(1, 1))) == ['right']
    assert ids(s.query('swap-target', 'locate', point(3, 1))) == ['left']

    # Target drift between preview and execute. A later batch that conflicts
    # with the previewed region only at commit time proves nothing landed: the
    # commit is refused whole and the catalog keeps only the intervening edit.
    drift = preview(target='drift-field')
    assert drift['conflicts'] == [] and drift['plan']
    after_edit = s.edit(op('drift-field', 'plug', box(102, 50, 106, 52)))['revision']
    refused = s.post('/transplant/commit', dict(plan=drift['plan']), 409)
    assert refused['code'] == 'conflict' and 'changed between preview' in refused['message']
    assert s.get('/status')['revision'] == after_edit
    assert summary('drift-field')['count'] == 1
    assert ids(s.query('drift-field', 'locate', point(101, 51))) == []
    assert ids(s.query('drift-field', 'locate', point(103, 51))) == ['plug']
    assert 'plan' not in preview(target='drift-field')
    assert [c['kind'] for c in preview(target='drift-field')['conflicts']] == ['exclusive']

    # An already-committed plan cannot replay: its pinned target revision moved.
    replay = s.post('/transplant/commit', dict(plan=pin['plan']), 409)
    assert replay['code'] == 'conflict' and 'changed between preview' in replay['message']

    # A window that selects nothing is a preview conflict, not an error, and it
    # carries no plan.
    empty = preview(bounds=box(40, 40, 44, 44))
    assert [c['kind'] for c in empty['conflicts']] == ['empty'] and 'plan' not in empty
    assert empty['features'] == []

    # A tampered plan and a stale global candidate are refused without writing.
    tampered = drift['plan'][:-1] + ('A' if drift['plan'][-1] != 'A' else 'B')
    s.post('/transplant/commit', dict(plan=tampered), 400)
    s.post('/transplant/commit', dict(plan=first['plan'], if_revision=0), 409)
    assert s.get('/status')['revision'] == after_edit

    return dict(transplanted=committed['transplanted'], source_revision=backup_revision,
                excluded=first['excluded'], features=[f['id'] for f in first['features']])


def transplant_drift(s):
    """A conflict that appears only after a restore leaves no partial batch."""
    bounds = box(-100, -100, 100, 100)
    s.post('/strata', dict(name='drift-source', kind='extent', bounds=bounds, exclusive=False), 201)
    s.post('/strata', dict(name='drift-target', kind='extent', bounds=bounds, exclusive=True), 201)
    s.edit(op('drift-source', 'core', box(0, 0, 4, 2)),
           op('drift-source', 'wing', box(4, 0, 8, 2)),
           op('drift-target', 'blocker', box(20, 10, 24, 12)))

    # Snapshot with the blocker in the target, then clear it from the live copy
    # so an insert into that region previews cleanly.
    snapshot, headers = s.request('GET', '/image')
    snapshot_revision = int(headers['Meridian-Revision'])
    encoded = base64.b64encode(snapshot).decode()
    s.edit(op('drift-target', 'blocker', mode='remove'))
    preview = s.post('/transplant/preview', dict(
        source=encoded, source_layer='drift-source', target='drift-target',
        bounds=box(0, 0, 8, 2), offset=dict(x=20, y=10), mode='insert'))
    assert preview['conflicts'] == [] and preview['plan']
    assert ids(s.query('drift-target', 'locate', point(21, 11))) == []

    # Restoring the snapshot brings the blocker back. The batch now overlaps it,
    # so the same plan can neither be applied nor partly applied.
    restored = s.restore(snapshot, s.get('/status')['revision'])
    assert restored['revision'] > snapshot_revision
    refused = s.post('/transplant/commit', dict(plan=preview['plan']), 409)
    assert refused['code'] == 'conflict'
    assert 'changed between preview and execute' in refused['message']
    assert ids(s.query('drift-target', 'locate', point(21, 11))) == ['blocker']
    assert ids(s.query('drift-target', 'locate', point(24, 10))) == []
    layer = next(row for row in s.get('/strata')['layers'] if row['name'] == 'drift-target')
    assert layer['count'] == 1  # only the restored blocker, none of the batch

    # Re-previewing the same region now reports the overlap instead of a plan.
    again = s.post('/transplant/preview', dict(
        source=encoded, source_layer='drift-source', target='drift-target',
        bounds=box(0, 0, 8, 2), offset=dict(x=20, y=10), mode='insert'))
    assert [c['kind'] for c in again['conflicts']] == ['exclusive'] and 'plan' not in again
    assert again['conflicts'][0]['ids'] == ['blocker', 'core']
    return dict(refused=refused['code'], target_count=layer['count'])


def transplant_capacity(s):
    """A capacity-only conflict raised at commit although the target is unchanged."""
    bounds = box(-1000000, -1000000, 1000000, 1000000)

    def bulk(name, count):
        s.post('/strata', dict(name=name, kind='mark', bounds=bounds, exclusive=False), 201)
        for base in range(0, count, 512):
            size = min(512, count - base)
            s.edit(*(op(name, f'p{base + i:04d}', point(base + i, 0)) for i in range(size)))

    bulk('bulk-source', 4096)
    bulk('bulk-fill-a', 4096)
    bulk('bulk-fill-b', 4096)
    s.post('/strata', dict(name='bulk-target', kind='mark', bounds=bounds, exclusive=False), 201)
    backup = base64.b64encode(s.get('/image')).decode()

    preview = s.post('/transplant/preview', dict(
        source=backup, source_layer='bulk-source', target='bulk-target',
        bounds=box(-1, -1, 4096, 1), offset=dict(x=0, y=7), mode='insert'))
    assert preview['conflicts'] == [] and preview['plan'] and len(preview['features']) == 4096

    # One extra feature in a brand-new layer pushes the database over its global
    # capacity without touching the target layer revision the plan pinned.
    s.post('/strata', dict(name='bulk-overflow', kind='mark', bounds=bounds, exclusive=False), 201)
    s.edit(op('bulk-overflow', 'grain', point(0, 0)))
    target_revision = next(row['revision'] for row in s.get('/strata')['layers']
                           if row['name'] == 'bulk-target')
    assert target_revision == preview['target_revision']
    after_edit = s.get('/status')['revision']

    refused = s.post('/transplant/commit', dict(plan=preview['plan']), 422)
    assert refused['code'] == 'capacity' and 'capacity' in refused['message']
    target = next(row for row in s.get('/strata')['layers'] if row['name'] == 'bulk-target')
    assert target['count'] == 0 and target['revision'] == target_revision
    assert s.get('/status')['revision'] == after_edit
    return dict(refused=refused['code'], target_count=target['count'])


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
                                     inspect, seams, relation, audit, export, restore, reopen, transplant,
                                     transplant_drift, transplant_capacity)}


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
