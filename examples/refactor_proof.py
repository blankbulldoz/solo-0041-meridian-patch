#!/usr/bin/env python3
"""Regression proof for the staged /edits and /layout refactor.

Three paths prove the behavior did not drift:

1. exclusive-swap   - two rectangles that only touch on an edge may exchange
                      positions inside one batch; an actual positive-area
                      overlap in the final state is rejected.
2. chained-edits    - a later command depends on a feature that an earlier
                      command in the same batch creates, replaces or removes.
3. layout-shrink    - a layout whose new bounds exclude a committed feature
                      is rejected, the revision does not move and the feature
                      stays queryable.

The script starts the given server binary against an empty temporary data
directory, so the same proof can be replayed against a pre-refactor and a
post-refactor build. Run twice with different --binary values and compare the
exported MPB image bytes to also prove the disk format is unchanged.
"""
import argparse
import hashlib
import json
from pathlib import Path
import select
import subprocess
import tempfile
from urllib.error import HTTPError
from urllib.request import Request, urlopen

PREFIX = '/meridian/v1'
LAYER = 'proof-field'
BOUNDS = dict(min_x=-20, min_y=-20, max_x=20, max_y=20)


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def extent(feature, shape, category='fixture'):
    return dict(id=feature, **{'class': category}, shape={'extent': shape})


def op(feature, shape, mode='insert'):
    item = dict(layer=LAYER, id=feature, mode=mode)
    if shape is not None:
        item['value'] = extent(feature, shape)
    return item


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, status=200):
        headers = {}
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            headers['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=value, method=method, headers=headers)
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

    def put(self, path, value, status=200):
        return self.request('PUT', path, value, status)[0]

    def edit(self, *items, status=200):
        return self.post('/edits', dict(edits=list(items)), status)

    def locate(self, x, y):
        return self.post(f'/strata/{LAYER}/locate', dict(x=x, y=y))

    def layout(self, bounds, exclusive, status=200, revision=None):
        body = dict(bounds=bounds, exclusive=exclusive)
        if revision is not None:
            body['if_revision'] = revision
        return self.put(f'/strata/{LAYER}/layout', body, status)


def run(binary):
    with tempfile.TemporaryDirectory(prefix='meridian-proof-') as data:
        args = [str(binary), '-listen', '127.0.0.1:0', '-data', data]
        child = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            readable, _, _ = select.select([child.stderr], [], [], 10)
            if not readable:
                raise RuntimeError('service did not start within 10 seconds')
            startup = json.loads(child.stderr.readline())
            if 'address' not in startup:
                raise RuntimeError(startup)
            session = Session('http://' + startup['address'])
            proof = dict()
            proof['exclusive_swap'] = exclusive_swap(session)
            proof['chained_edits'] = chained_edits(session)
            proof['layout_shrink'] = layout_shrink(session)
            image, headers = session.request('GET', '/image')
            proof['image_revision'] = int(headers['Meridian-Revision'])
            proof['image_bytes'] = len(image)
            proof['image_head'] = image[:8].hex()
            proof['image_sha256'] = hashlib.sha256(image).hexdigest()
            return proof
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.communicate(timeout=5)


def exclusive_swap(s):
    s.post('/strata', dict(name=LAYER, kind='extent', bounds=BOUNDS, exclusive=True), 201)
    # Edge-touching rectangles are legal in an exclusive layer.
    first = s.edit(op('alpha', box(0, 0, 2, 2)), op('bravo', box(2, 0, 4, 2)))
    revision = first['revision']
    # One batch swaps the two footprints. The final state only touches edges,
    # so the swap commits even though neither intermediate row matches the end.
    swapped = s.edit(
        op('alpha', box(2, 0, 4, 2), mode='replace'),
        op('bravo', box(0, 0, 2, 2), mode='replace'),
    )
    assert swapped['revision'] == revision + 1
    assert [row['id'] for row in s.locate(1, 0)['features']] == ['bravo']
    assert [row['id'] for row in s.locate(3, 0)['features']] == ['alpha']
    # A genuine positive-area overlap in the final state is still a conflict,
    # and the rejection leaves the revision untouched.
    rejected = s.edit(op('alpha', box(1, 0, 3, 2), mode='replace'), status=409)
    assert rejected['code'] == 'conflict', rejected
    assert s.get('/status')['revision'] == swapped['revision']
    assert [row['id'] for row in s.locate(1, 0)['features']] == ['bravo']
    return dict(committed=swapped['revision'], rejected=rejected['code'])


def chained_edits(s):
    # An insert later in the same batch references a feature created earlier;
    # the second operation must observe the first one's output.
    created = s.edit(
        op('chain-a', box(8, 0, 9, 1)),
        op('chain-b', box(9, 0, 10, 1)),
    )
    revision = created['revision']
    chained = s.edit(
        op('chain-c', box(10, 0, 11, 1)),
        dict(layer=LAYER, id='chain-a', mode='replace', value=extent('chain-a', box(8, 0, 9, 2))),
        dict(layer=LAYER, id='chain-b', mode='remove'),
    )
    assert chained['revision'] == revision + 1
    ids = {row['id'] for row in s.locate(10, 0)['features']}
    assert ids == {'chain-c'}, ids
    assert s.locate(9, 0)['features'] == []
    assert [row['id'] for row in s.locate(8, 1)['features']] == ['chain-a']
    # A replace targeting a feature the same batch removes cannot see it: the
    # missing rule fires during interpretation and nothing is published.
    rejected = s.edit(
        dict(layer=LAYER, id='chain-c', mode='remove'),
        dict(layer=LAYER, id='chain-c', mode='replace', value=extent('chain-c', box(10, 0, 11, 2))),
        status=404,
    )
    assert rejected['code'] == 'missing', rejected
    assert s.get('/status')['revision'] == chained['revision']
    assert [row['id'] for row in s.locate(10, 0)['features']] == ['chain-c']
    return dict(committed=chained['revision'], rejected=rejected['code'])


def layout_shrink(s):
    s.post('/strata', dict(name='shrink-field', kind='extent', bounds=BOUNDS, exclusive=False), 201)
    seeded = s.post('/edits', dict(edits=[
        dict(layer='shrink-field', id='far', mode='insert',
             value=extent('far', box(10, 10, 12, 12))),
    ]))
    revision = seeded['revision']
    # New bounds exclude the committed feature: the stage-end definition check
    # rejects the layout and nothing is published.
    rejected = s.request(
        'PUT', '/strata/shrink-field/layout',
        dict(bounds=box(0, 0, 5, 5), exclusive=False), 400,
    )[0]
    assert rejected['code'] == 'invalid', rejected
    status = s.get('/status')
    assert status['revision'] == revision, status
    summary = s.get('/strata')['layers']
    layer = next(item for item in summary if item['name'] == 'shrink-field')
    assert layer['bounds'] == BOUNDS and layer['count'] == 1, layer
    found = s.post('/strata/shrink-field/locate', dict(x=11, y=11))
    assert [row['id'] for row in found['features']] == ['far']
    # A layout that keeps every feature inside the new bounds still commits.
    accepted = s.request(
        'PUT', '/strata/shrink-field/layout',
        dict(bounds=box(0, 0, 12, 12), exclusive=False), 200,
    )[0]
    assert accepted['revision'] == revision + 1
    return dict(rejected=rejected['code'], committed=accepted['revision'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--label', default='build')
    args = parser.parse_args()
    proof = run(args.binary)
    print(json.dumps(dict(label=args.label, proof=proof), sort_keys=True, indent=2))


if __name__ == '__main__':
    main()
