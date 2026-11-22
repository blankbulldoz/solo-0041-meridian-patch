#!/usr/bin/env python3
"""Reference window-paging client and drill.

Walks one layer through every event that can invalidate a page cursor —
another layer's write, a write to the same layer, a changed predicate, a
process restart and an image restore — and records, for each, whether the
client must resume, rewind or stop.

The drill also feeds every page to a downstream publisher that edits nothing
twice, so a retried or replayed request never republishes a row.

Runs against a throwaway data directory and a system-assigned port; it never
touches a running database. Standard library only.
"""
import argparse
import contextlib
import json
import select
import subprocess
import tempfile
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'

FIELD, SIDE, SPARE = 'alpha-flat', 'beta-flat', 'gamma-flat'
PLANE = dict(min_x=-20, min_y=-20, max_x=20, max_y=20)
VIEW = dict(min_x=0, min_y=0, max_x=10, max_y=4)

RESUME, REWIND, STOP = 'resume', 'rewind', 'stop'


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def point(x, y):
    return dict(x=x, y=y)


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, headers=None):
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
            if response.headers.get_content_type() == 'application/json':
                body = json.loads(raw)
            else:
                body = raw
            return response.status, body, response.headers

    def get(self, path):
        return self.request('GET', path)

    def post(self, path, value):
        return self.request('POST', path, value)


def op(layer, feature, shape, mode='insert', category='fixture'):
    item = dict(layer=layer, id=feature, mode=mode)
    if shape is not None:
        kind = 'mark' if 'x' in shape else 'extent'
        item['value'] = dict(id=feature, **{'class': category}, shape={kind: shape})
    return item


def page(s, layer, bounds, limit, bookmark=None, category=None):
    body = dict(bounds=bounds, limit=limit)
    if bookmark is not None:
        body['bookmark'] = bookmark
    if category is not None:
        body['class'] = category
    return s.post(f'/strata/{layer}/window', body)


def revision(s):
    return s.get('/status')[1]['revision']


def generation_of(s, layer):
    for row in s.get('/strata')[1]['layers']:
        if row['name'] == layer:
            return row['revision']
    raise KeyError(layer)


def ids(body):
    return [row['id'] for row in body['features']]


class Journal:
    """One line per observation, so the transcript reads as a decision table."""

    def __init__(self):
        self.rows = []

    def note(self, event, status, decision, detail):
        self.rows.append(dict(event=event, http=status, decision=decision, detail=detail))
        print(f'{event:<38} {status!s:<4} {decision:<7} {detail}')

    def check(self, event, status, decision, condition, detail):
        self.note(event, status, decision, detail)
        assert condition, f'{event}: {detail}'


class Publisher:
    """Downstream hand-off keyed by (layer, layer_revision, id).

    A row is published at most once per layer generation. A generation is
    committed only when a page arrives without ``next``, so a rewind after a
    same-layer change discards the partial buffer and never emits a truncated
    feed.
    """

    def __init__(self):
        self.committed = []
        self.seen = set()
        self.open = {}

    def accept(self, layer, body):
        generation = body['layer_revision']
        buffered = self.open.get(layer)
        if buffered is not None and buffered['revision'] != generation:
            self.open[layer] = None
            buffered = None
        if buffered is None:
            buffered = self.open[layer] = dict(revision=generation, rows=[])
        added = 0
        for row in body['features']:
            key = (layer, generation, row['id'])
            if key in self.seen:
                continue
            self.seen.add(key)
            buffered['rows'].append(row)
            added += 1
        if 'next' not in body:
            self.open[layer] = None
            if buffered['rows']:
                self.committed.append(dict(layer=layer, revision=generation,
                                           rows=buffered['rows']))
        return added

    def generations(self, layer):
        return [g for g in self.committed if g['layer'] == layer]


@contextlib.contextmanager
def running(binary, data):
    child = subprocess.Popen([str(binary), '-listen', '127.0.0.1:0', '-data', str(data)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        readable, _, _ = select.select([child.stderr], [], [], 10)
        if not readable:
            raise RuntimeError('service did not start within 10 seconds')
        startup = json.loads(child.stderr.readline())
        if 'address' not in startup:
            raise RuntimeError(startup)
        yield Session('http://' + startup['address']), startup
    finally:
        if child.poll() is None:
            child.terminate()
        try:
            child.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.communicate(timeout=5)


def seed(s):
    s.post('/strata', dict(name=FIELD, kind='extent', bounds=PLANE, exclusive=False))
    s.post('/strata', dict(name=SIDE, kind='extent', bounds=PLANE, exclusive=False))
    edits = [op(FIELD, fid, box(i * 2, 0, i * 2 + 2, 2)) for i, fid in enumerate('abcde')]
    edits.append(op(SIDE, 'z', box(0, 0, 2, 2)))
    return s.post('/edits', dict(edits=edits))[1]['revision']


def scenarios(s, journal):
    """Thread the five invalidating events, plus the no-op counterexamples."""
    generation = seed(s)
    first = page(s, FIELD, VIEW, 2)[1]
    token = first['next']
    journal.check('seed + first page', 200, RESUME,
                  generation == 3 and ids(first) == ['a', 'b']
                  and first['layer_revision'] == 3 and first['revision'] == 3,
                  f'db={first["revision"]} layer={first["layer_revision"]} ids={ids(first)}')

    # 1. Another layer moves: the database revision advances, the layer does not.
    s.post('/edits', dict(edits=[op(SIDE, 'z', box(1, 1, 3, 3), mode='replace')]))
    status, second, _ = page(s, FIELD, VIEW, 2, bookmark=token)
    journal.check('other-layer write, resume', status, RESUME,
                  status == 200 and ids(second) == ['c', 'd']
                  and second['layer_revision'] == generation and second['revision'] == generation + 1
                  and 'next' in second,
                  f'db={second["revision"]} layer={second["layer_revision"]} ids={ids(second)}')
    carry = second['next']
    # Re-issuing the *first* page request is also safe: same rows, same cursor.
    status, replay, _ = page(s, FIELD, VIEW, 2)
    journal.check('replay page one, resume', status, RESUME,
                  status == 200 and ids(replay) == ids(first) and replay['next'] == token,
                  'same rows and same bookmark; the token is a pure function of the predicate')

    # 2. The same layer moves: the generation is gone, the rows are not.
    s.post('/edits', dict(edits=[op(FIELD, 'a', box(0, 0, 2, 2), mode='replace')]))
    generation = generation_of(s, FIELD)
    status, expired, _ = page(s, FIELD, VIEW, 2, bookmark=carry)
    journal.check('same-layer write, rewind', status, REWIND,
                  status == 409 and expired['code'] == 'conflict', expired['message'])

    # 3. Not every request that touches the layer kills the cursor. Take a
    # cursor from the *current* generation before testing each no-op.
    carry = page(s, FIELD, VIEW, 2)[1]['next']
    status, rejected, _ = s.post('/edits', dict(edits=[op(FIELD, 'q', box(50, 50, 60, 60))]))
    status, resumed, _ = page(s, FIELD, VIEW, 2, bookmark=carry)
    journal.check('rejected batch, resume', status, RESUME,
                  rejected['code'] == 'invalid' and status == 200
                  and ids(resumed) == ['c', 'd'] and revision(s) == generation,
                  f'db={revision(s)} ids={ids(resumed)}')

    # A layout request that changes nothing is also a no-op.
    status, same, _ = s.request('PUT', f'/strata/{FIELD}/layout', dict(bounds=PLANE))
    status, fourth, _ = page(s, FIELD, VIEW, 2, bookmark=carry)
    journal.check('no-op reframe, resume', status, RESUME,
                  same['revision'] == generation and status == 200 and revision(s) == generation,
                  f'db={same["revision"]} ids={ids(fourth)}')

    # But storing a byte-identical value is still a write, and still expires.
    s.post('/edits', dict(edits=[op(FIELD, 'b', box(2, 0, 4, 2), mode='replace')]))
    generation = generation_of(s, FIELD)
    status, expired_again, _ = page(s, FIELD, VIEW, 2, bookmark=carry)
    journal.check('idempotent replace, rewind', status, REWIND,
                  status == 409 and expired_again['code'] == 'conflict',
                  expired_again['message'] + ' (a same-valued replace still commits)')

    # 4. A changed predicate is a different scan, not a continuation.
    fresh = page(s, FIELD, VIEW, 2)[1]
    token = fresh['next']
    status, mismatch, _ = page(s, FIELD, VIEW, 2, bookmark=token, category='fixture')
    journal.check('class added, rewind', status, REWIND,
                  status == 400 and mismatch['code'] == 'invalid', mismatch['message'])
    status, widened, _ = page(s, FIELD, box(0, 0, 9, 4), 2, bookmark=token)
    journal.check('bounds changed, rewind', status, REWIND,
                  status == 400 and widened['code'] == 'invalid', widened['message'])
    status, after_mismatch, _ = page(s, FIELD, VIEW, 2, bookmark=token)
    journal.check('original predicate again', status, RESUME,
                  status == 200 and ids(after_mismatch) == ['c', 'd'],
                  f'ids={ids(after_mismatch)}; a 400 does not burn the token')
    status, resized, _ = page(s, FIELD, VIEW, 3, bookmark=token)
    journal.check('limit changed, resume', status, RESUME,
                  status == 200 and ids(resized) == ['c', 'd', 'e']
                  and 'next' not in resized
                  and resized['layer_revision'] == fresh['layer_revision'],
                  f'page size is not part of the predicate; ids={ids(resized)}')
    # A smaller page still continues from the same cursor without repeating rows.
    status, small, _ = page(s, FIELD, VIEW, 1, bookmark=token)
    status, small2, _ = page(s, FIELD, VIEW, 1, bookmark=small['next'])
    status, small3, _ = page(s, FIELD, VIEW, 1, bookmark=small2['next'])
    journal.check('smaller pages, resume', status, RESUME,
                  ids(small) == ['c'] and ids(small2) == ['d'] and ids(small3) == ['e']
                  and 'next' in small and 'next' in small2 and 'next' not in small3,
                  f'{ids(small)} then {ids(small2)} then {ids(small3)}; no row repeats')

    status, moved, _ = page(s, SIDE, VIEW, 2, bookmark=token)
    journal.check('layer switched, rewind', status, REWIND,
                  status == 400 and moved['code'] == 'invalid', moved['message'])

    backup = s.get('/image')[1]
    return token, backup


def restart(binary, data, token, journal):
    """A restart rotates the bookmark key; the revision does not move."""
    with running(binary, data) as (s, startup):
        before = startup['revision']
        status, stale, _ = page(s, FIELD, VIEW, 2, bookmark=token)
        journal.check('restart, rewind', status, REWIND,
                      status == 400 and stale['code'] == 'invalid' and before == 6,
                      f'db unchanged at {before}; {stale["message"]}')
        status, fresh, _ = page(s, FIELD, VIEW, 2)
        journal.check('restart, fresh page', status, RESUME,
                      status == 200 and ids(fresh) == ['a', 'b']
                      and fresh['layer_revision'] == 6,
                      f'db={fresh["revision"]} layer={fresh["layer_revision"]}')
        status, gone, _ = page(s, 'missing-layer', VIEW, 2)
        journal.check('unknown layer, stop', status, STOP,
                      status == 404 and gone['code'] == 'missing', gone['message'])
        return fresh['next']


def restore(s, restart_token, backup, journal):
    """Restore rebases every imported layer and drops layers the image lacks."""
    # A cursor minted in this process proves the restore, not the key, expires it.
    status, stale_after_restart, _ = page(s, FIELD, VIEW, 2, bookmark=restart_token)
    journal.check('cursor across restart, rewind', status, REWIND,
                  status == 400 and stale_after_restart['code'] == 'invalid',
                  stale_after_restart['message'] + ' (same revision, new process key)')
    token = page(s, FIELD, VIEW, 2)[1]['next']

    revision_before = revision(s)
    status, commit, _ = s.request('PUT', '/image', backup, {
        'Content-Type': IMAGE,
        'If-Meridian-Revision': str(revision_before),
    })
    layers = {row['name']: row['revision'] for row in commit['layers']}
    journal.check('image restore, rewind', status, REWIND,
                  status == 200 and commit['revision'] == revision_before + 1
                  and set(layers.values()) == {revision_before + 1}
                  and set(layers) == {FIELD, SIDE},
                  f'db={commit["revision"]} layers={layers}')
    status, stale, _ = page(s, FIELD, VIEW, 2, bookmark=token)
    journal.check('restored layer, rewind', status, REWIND,
                  status == 409 and stale['code'] == 'conflict',
                  stale['message'] + ' (imported layers were rebased)')

    # A layer absent from the image is gone; its cursor can only stop.
    s.post('/strata', dict(name=SPARE, kind='extent', bounds=box(0, 0, 5, 5)))
    s.post('/edits', dict(edits=[op(SPARE, 'g1', box(1, 1, 2, 2)),
                                 op(SPARE, 'g2', box(2, 2, 3, 3))]))
    spare = page(s, SPARE, box(0, 0, 5, 5), 1)[1]
    spare_token = spare['next']
    status, commit, _ = s.request('PUT', '/image', backup, {
        'Content-Type': IMAGE,
        'If-Meridian-Revision': str(revision(s)),
    })
    status, gone, _ = page(s, SPARE, box(0, 0, 5, 5), 1, bookmark=spare_token)
    journal.check('restore dropped layer, stop', status, STOP,
                  status == 404 and gone['code'] == 'missing', gone['message'])
    fresh = page(s, FIELD, VIEW, 2)[1]
    journal.check('post-restore page', 200, RESUME,
                  ids(fresh) == ['a', 'b']
                  and fresh['layer_revision'] == commit['revision'],
                  f'db={fresh["revision"]} layer={fresh["layer_revision"]}')
    return fresh['next']


def downstream(s, journal):
    """Hand every page over without republishing a row for a re-read."""
    publisher = Publisher()
    bounds, limit = VIEW, 2

    def drain(category=None):
        token, pages = None, 0
        while True:
            status, body, _ = page(s, FIELD, bounds, limit, bookmark=token, category=category)
            assert status == 200, (status, body)
            publisher.accept(FIELD, body)
            pages += 1
            token = body.get('next')
            if token is None:
                return pages

    pages = drain()
    first_generation = publisher.generations(FIELD)[0]
    journal.check('downstream full scan', 200, STOP,
                  pages == 3 and [row['id'] for row in first_generation['rows']] == list('abcde'),
                  f'{pages} pages, generation {first_generation["revision"]}')

    # Replay every request: a retry must not republish.
    before = len(publisher.seen)
    token, replayed = None, 0
    while True:
        status, body, _ = page(s, FIELD, bounds, limit, bookmark=token)
        added = publisher.accept(FIELD, body)
        replayed += added
        token = body.get('next')
        if token is None:
            break
    journal.check('replayed requests', 200, RESUME,
                  replayed == 0 and len(publisher.seen) == before
                  and len(publisher.generations(FIELD)) == 1,
                  f'{len(publisher.seen)} rows seen, 0 added, 1 committed generation')

    # A same-layer change mid-scan: the cursor dies, the buffer is dropped, and
    # the publisher restarts only after seeing the 409.
    token, status = None, None
    while True:
        status, body, _ = page(s, FIELD, bounds, limit, bookmark=token)
        assert status == 200, (status, body)
        publisher.accept(FIELD, body)
        token = body.get('next')
        if token is None:
            raise AssertionError('expected more than one page before the write')
        break
    s.post('/edits', dict(edits=[op(FIELD, 'a', box(0, 0, 3, 2), mode='replace')]))
    status, expired, _ = page(s, FIELD, bounds, limit, bookmark=token)
    journal.check('mid-scan 409, rewind', status, REWIND,
                  status == 409 and expired['code'] == 'conflict',
                  expired['message'] + '; the partial page was not published')
    publisher.open[FIELD] = None
    pages = drain()
    generations = publisher.generations(FIELD)
    journal.check('rewind publishes generation', 200, STOP,
                  len(generations) == 2 and pages == 3,
                  f'generations {[g["revision"] for g in generations]}')
    latest = generations[-1]
    shape = latest['rows'][0]['shape']['extent']
    journal.check('new generation supersedes', 200, RESUME,
                  latest['revision'] != generations[0]['revision']
                  and shape['max_x'] == 3
                  and [row['id'] for row in latest['rows']] == list('abcde'),
                  f'generation {latest["revision"]} carries a.max_x={shape["max_x"]}')
    keys = [(g['revision'], row['id']) for g in generations for row in g['rows']]
    journal.check('no duplicate publication', 200, RESUME,
                  len(keys) == len(set(keys))
                  and all(len({row['id'] for row in g['rows']}) == len(g['rows'])
                          for g in generations),
                  f'{len(keys)} publications, {len(set(keys))} distinct; '
                  'each generation publishes each id once and replaces the last')
    return publisher


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', help='reuse a built meridian binary')
    args = parser.parse_args()
    journal = Journal()
    with tempfile.TemporaryDirectory(prefix='meridian-paging-') as temporary:
        root = Path(temporary)
        binary, data = root / 'meridian', root / 'space'
        if args.binary:
            binary = Path(args.binary)
        else:
            subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'],
                           check=True, cwd=ROOT, timeout=90)
        with running(binary, data) as (s, _):
            token, backup = scenarios(s, journal)
        token = restart(binary, data, token, journal)
        with running(binary, data) as (s, _):
            token = restore(s, token, backup, journal)
            publisher = downstream(s, journal)
            final = revision(s)
        summary = dict(
            scopes=[
                dict(name='database', field='revision', binds='if_revision, If-Meridian-Revision'),
                dict(name='layer', field='layer_revision', binds='bookmark'),
            ],
            decisions=journal.rows,
            generations=[dict(layer=g['layer'], revision=g['revision'],
                              ids=[row['id'] for row in g['rows']])
                         for g in publisher.generations(FIELD)],
            final_revision=final,
        )
        print(json.dumps(summary, indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
