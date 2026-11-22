#!/usr/bin/env python3
"""Exercise the normal HTTP surface in a clean temporary database."""
import argparse
from contextlib import contextmanager
import heapq
import json
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import threading
import time
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

    def exchange(self, method, path, value=None, headers=None):
        """Issue one request without asserting, keeping its timing interval."""
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            args['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=value, method=method, headers=args)
        started = time.monotonic_ns()
        try:
            response = urlopen(req, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            ended = time.monotonic_ns()
            kind = response.headers.get_content_type()
            body = json.loads(raw) if kind == 'application/json' else raw
            return dict(status=response.status, body=body, headers=response.headers,
                        raw=raw, started=started, ended=ended)

    def request(self, method, path, value=None, status=200, headers=None):
        result = self.exchange(method, path, value, headers)
        assert result['status'] == status, (method, path, result['status'], status, result['raw'][:512])
        return result['body'], result['headers']

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


SWAP = 'swap-yard'
PROBE = 'probe-yard'
ALPHA, BRAVO = 'alpha', 'bravo'
ANCHOR = 'anchor'
LEFT = box(0, 0, 2, 2)
RIGHT = box(2, 0, 4, 2)
JOIN_BOUNDS = box(0, 0, 4, 2)
CONCURRENCY_ROUNDS = 24
CONCURRENCY_READERS = 3
CONCURRENCY_CYCLE_LIMIT = 400


class Journal:
    """Keep every request's body, outcome and [start, end] interval."""

    def __init__(self):
        self.lock = threading.Lock()
        self.origin = time.monotonic_ns()
        self.entries = []

    def note(self, thread, label, request, result):
        body = result['body']
        entry = dict(
            thread=thread, label=label, request=request,
            status=result['status'],
            started_ms=round((result['started'] - self.origin) / 1e6, 3),
            ended_ms=round((result['ended'] - self.origin) / 1e6, 3),
        )
        if isinstance(body, dict):
            for field in ('revision', 'layer_revision', 'left_revision', 'right_revision',
                          'code', 'writable'):
                if field in body:
                    entry[field] = body[field]
            if 'features' in body:
                entry['ids'] = [row['id'] for row in body['features']]
            if 'pairs' in body:
                entry['pairs'] = body['pairs']
        with self.lock:
            self.entries.append(entry)

    def render(self):
        with self.lock:
            entries = sorted(self.entries, key=lambda row: (row['started_ms'], row['thread']))
        return entries


def probe(journal, session, thread, label, method, path, value=None):
    result = session.exchange(method, path, value)
    journal.note(thread, label, dict(method=method, path=path, body=value), result)
    return result


def swap_edits(occupancy):
    """Move both adjacent rectangles to the other's slot in one atomic batch."""
    targets = {occupancy['right']: LEFT, occupancy['left']: RIGHT}
    edits = [op(SWAP, feature, slot, mode='replace') for feature, slot in sorted(targets.items())]
    return edits, dict(left=occupancy['right'], right=occupancy['left'])


def swap_batch(occupancy):
    edits, _ = swap_edits(occupancy)
    return dict(edits=edits)


def setup_swap(s, journal):
    created = probe(journal, s, 'setup', 'create-swap-layer', 'POST', '/strata',
                    dict(name=SWAP, kind='extent', bounds=BOUNDS, exclusive=True))
    assert created['status'] == 201, created
    created = probe(journal, s, 'setup', 'create-probe-layer', 'POST', '/strata',
                    dict(name=PROBE, kind='extent', bounds=BOUNDS, exclusive=False))
    assert created['status'] == 201, created
    seeded = probe(journal, s, 'setup', 'seed', 'POST', '/edits', dict(edits=[
        op(SWAP, ALPHA, LEFT), op(SWAP, BRAVO, RIGHT), op(PROBE, ANCHOR, RIGHT, 'anchor'),
    ]))
    assert seeded['status'] == 200, seeded
    revision = seeded['body']['revision']
    listing = probe(journal, s, 'setup', 'catalog', 'GET', '/strata')
    assert listing['status'] == 200, listing
    layers = {row['name']: row for row in listing['body']['layers']}
    assert layers[SWAP]['revision'] == revision and layers[PROBE]['revision'] == revision
    return revision


def observe(journal, session, thread, observations):
    """One status/locate/join cycle; every result is verified after threads stop."""
    status = probe(journal, session, thread, 'status', 'GET', '/status')
    assert status['status'] == 200, status['body']
    observations.append(dict(kind='status', thread=thread, revision=status['body']['revision'],
                             writable=status['body']['writable'],
                             started=status['started'], ended=status['ended']))
    for x in (1, 3, 2):
        located = probe(journal, session, thread, f'locate-x{x}', 'POST',
                        f'/strata/{SWAP}/locate', point(x, 1))
        assert located['status'] == 200, located['body']
        observations.append(dict(kind='locate', thread=thread, x=x,
                                 ids=[row['id'] for row in located['body']['features']],
                                 revision=located['body']['revision'],
                                 layer_revision=located['body']['layer_revision'],
                                 started=located['started'], ended=located['ended']))
    joined = probe(journal, session, thread, 'join', 'POST', '/join',
                   dict(left=SWAP, right=PROBE, bounds=JOIN_BOUNDS, limit=10))
    assert joined['status'] == 200, joined['body']
    observations.append(dict(kind='join', thread=thread, pairs=joined['body']['pairs'],
                             revision=joined['body']['revision'],
                             left_revision=joined['body']['left_revision'],
                             right_revision=joined['body']['right_revision'],
                             started=joined['started'], ended=joined['ended']))


def expected_layout(views, revision, observation):
    assert revision in views, ('read reports a revision that is not a committed view', observation)
    return views[revision]


def verify_views(views, observations, seed_revision, swap_revision, last_revision):
    """Prove every read's evidence belongs to one legal committed layout."""
    expected = set(range(seed_revision, last_revision + 1))
    assert set(views) == expected, ('committed revisions are not contiguous', sorted(views))
    for revision, layout in views.items():
        assert layout['left'] != layout['right'], ('both rectangles share one slot', layout)
        assert {layout['left'], layout['right']} == {ALPHA, BRAVO}, ('layout lost a rectangle', layout)
        assert (revision - seed_revision) % 2 == (0 if layout['right'] == BRAVO else 1), (
            'layout parity does not match the swap history', revision, layout)
    for row in observations:
        layout = expected_layout(views, row['revision'], row)
        if row['kind'] == 'status':
            assert row['writable'] is True, ('writer fence observed during swap reads', row)
        elif row['kind'] == 'locate':
            slot = 'left' if row['x'] == 1 else 'right'
            assert row['layer_revision'] == row['revision'], (
                'locate mixes a layer revision with another database revision', row)
            assert row['ids'] == [layout[slot]], (
                'half-open ownership disagrees with the committed slot', slot, row, layout)
        else:
            assert row['left_revision'] == row['revision'], (
                'join pairs layers from different reads of the same layer', row)
            assert row['right_revision'] == seed_revision, (
                'join reflects a change to the untouched probe layer', row)
            assert row['pairs'] == [dict(left=layout['right'], right=ANCHOR, intersection=RIGHT)], (
                'window pairing disagrees with the committed slot', row, layout)
    verify_order(observations)
    verify_interleaving(observations, seed_revision, swap_revision)


def verify_interleaving(observations, seed_revision, swap_revision):
    """Confirm the readers really overlapped commits instead of observing one view."""
    per_thread = {}
    for row in sorted(observations, key=lambda item: item['started']):
        previous = per_thread.get(row['thread'])
        assert previous is None or row['revision'] >= previous, (
            'one client observed a revision going backwards', row['thread'], previous, row)
        per_thread[row['thread']] = row['revision']
    readers = {}
    for row in observations:
        if row['thread'].startswith('reader-'):
            readers.setdefault(row['thread'], []).append(row['revision'])
    assert readers, ('no reader observations were captured', sorted(per_thread))
    for name, seen in readers.items():
        observed = sorted(set(seen))
        assert observed != [seed_revision], ('reader only ever saw the seed view', name, observed)
        assert seed_revision in seen, ('reader missed the pre-swap seed view', name, observed)
        assert swap_revision in seen, ('reader stopped before the last swap landed', name, observed)


def verify_order(observations):
    """A read that finished before another started cannot report a newer revision."""
    finished = []
    latest = -1
    for row in sorted(observations, key=lambda item: item['started']):
        while finished and finished[0][0] <= row['started']:
            _, revision = heapq.heappop(finished)
            latest = max(latest, revision)
        assert latest <= row['revision'], (
            'an earlier completed read saw a newer revision than a later read', latest, row)
        heapq.heappush(finished, (row['ended'], row['revision']))


def concurrency(s, rounds=None):
    rounds = CONCURRENCY_ROUNDS if rounds is None else rounds
    journal = Journal()
    views = {}
    try:
        return run_concurrency(s, journal, views, rounds)
    except BaseException as error:
        report = dict(workflow='concurrency', failure=repr(error),
                      committed_views=dict(sorted(views.items())),
                      journal=journal.render())
        sys.stderr.write(json.dumps(report, sort_keys=True) + '\n')
        raise


def run_concurrency(s, journal, views, rounds):
    races = max(4, rounds // 2)
    observations = []
    observations_lock = threading.Lock()
    failures = []
    seed_revision = setup_swap(s, journal)
    final_revision = seed_revision + rounds
    views[seed_revision] = dict(left=ALPHA, right=BRAVO)
    occupancy = dict(left=ALPHA, right=BRAVO)
    barrier = threading.Barrier(1 + CONCURRENCY_READERS)
    warm = threading.Barrier(1 + CONCURRENCY_READERS)

    def record_failure(name, error):
        with observations_lock:
            failures.append((name, repr(error)))

    def reader(name):
        session = Session(s.url)
        seen = []
        try:
            barrier.wait(timeout=10)
            # Take one read before any swap so each client is known to hold the
            # seed view; then run alongside the writer until the last swap shows.
            cycle = []
            observe(journal, session, name, cycle)
            with observations_lock:
                observations.extend(cycle)
            seen.extend(row['revision'] for row in cycle)
            warm.wait(timeout=10)
            for _ in range(CONCURRENCY_CYCLE_LIMIT):
                if max(seen, default=0) >= final_revision:
                    return
                cycle = []
                observe(journal, session, name, cycle)
                with observations_lock:
                    observations.extend(cycle)
                seen.extend(row['revision'] for row in cycle)
        except BaseException as error:
            record_failure(name, error)

    def writer():
        session = Session(s.url)
        try:
            barrier.wait(timeout=10)
            warm.wait(timeout=10)
            for index in range(rounds):
                edits, occupancy_next = swap_edits(occupancy)
                result = probe(journal, session, 'writer', f'swap-{index}', 'POST', '/edits',
                               dict(edits=edits))
                assert result['status'] == 200, result['body']
                revision = result['body']['revision']
                assert revision == seed_revision + index + 1, (
                    'swap did not publish exactly one revision', revision, index)
                occupancy.update(occupancy_next)
                with observations_lock:
                    views[revision] = dict(occupancy)
        except BaseException as error:
            record_failure('writer', error)

    workers = [threading.Thread(target=reader, args=(f'reader-{i}',), name=f'reader-{i}')
               for i in range(CONCURRENCY_READERS)]
    workers.append(threading.Thread(target=writer, name='writer'))
    for worker in workers:
        worker.start()
    for worker in workers:
        worker.join()
    if failures:
        raise AssertionError(failures)

    revision = seed_revision + rounds
    for index in range(races):
        batch = swap_batch(occupancy)
        start = threading.Barrier(2)
        results = []
        results_lock = threading.Lock()

        def racer(name):
            session = Session(s.url)
            start.wait(timeout=10)
            result = probe(journal, session, name, f'race-{index}', 'POST', '/edits',
                           dict(batch, if_revision=revision))
            with results_lock:
                results.append(result)

        contenders = [threading.Thread(target=racer, args=(f'writer-{i}',), name=f'writer-{i}')
                      for i in range(2)]
        for contender in contenders:
            contender.start()
        for contender in contenders:
            contender.join()
        winners = [row for row in results if row['status'] == 200]
        losers = [row for row in results if row['status'] == 409]
        assert len(winners) == 1 and len(losers) == 1, (
            'same-revision writers did not resolve to one success', revision,
            [(row['status'], row['body']) for row in results])
        assert losers[0]['body']['code'] == 'conflict', losers[0]['body']
        assert winners[0]['body']['revision'] == revision + 1, winners[0]['body']
        _, occupancy = swap_edits(occupancy)
        revision += 1
        views[revision] = dict(occupancy)
        latest = probe(journal, s, 'verifier', f'race-{index}-status', 'GET', '/status')
        assert latest['status'] == 200 and latest['body']['revision'] == revision, latest['body']
        observe(journal, s, 'verifier', observations)

    verify_views(views, observations, seed_revision, final_revision, revision)
    return dict(swaps=rounds, races=races, reads=len(observations),
                revisions=dict(first=seed_revision, last=revision),
                final=views[revision])


WORKFLOWS = {f.__name__: f for f in (register, edit, reframe, window, locate, nearest, join, coverage,
                                     inspect, seams, relation, audit, export, restore, reopen,
                                     concurrency)}


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
