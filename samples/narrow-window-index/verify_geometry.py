#!/usr/bin/env python3
"""Independent, request-free verification of a sample payload.

Reads only the payload JSON files plus the shared windows.json and
re-derives, with plain Python interval arithmetic, the candidate and hit
counts the index run should report.  It never talks to the server, so it
cannot inherit a bug from the Go index.
"""
import argparse
import json
from collections import Counter
from pathlib import Path

HERE = Path(__file__).resolve().parent


def overlap(a, b):
    """Positive-area intersection of half-open rectangles."""
    x0 = max(a['min_x'], b['min_x'])
    x1 = min(a['max_x'], b['max_x'])
    y0 = max(a['min_y'], b['min_y'])
    y1 = min(a['max_y'], b['max_y'])
    return x1 > x0 and y1 > y0


def may_meet(a, b):
    """Inclusive envelope test, exactly the index's pruning predicate."""
    return (a['min_x'] <= b['max_x'] and b['min_x'] <= a['max_x']
            and a['min_y'] <= b['max_y'] and b['min_y'] <= a['max_y'])


def load(sample):
    body = json.loads((HERE / 'payloads' / f'sample-{sample}.json').read_text())
    assert set(body) == {'layer', 'edits'}, sorted(body)
    features = []
    for edit in body['edits']:
        assert edit['mode'] == 'insert'
        value = edit['value']
        assert value['id'] == edit['id']
        assert set(value['shape']) == {'extent'}, value
        features.append((value['id'], value['class'], value['shape']['extent']))
    windows = json.loads((HERE / 'windows.json').read_text())
    return body, features, windows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sample', choices=('a', 'b'), required=True)
    args = parser.parse_args()
    body, features, windows = load(args.sample)

    classes = Counter(cls for _, cls, _ in features)
    assert len(features) == len({fid for fid, _, _ in features}) == 400
    assert classes == Counter(core=200, halo=120, edge=80), classes
    for _, _, r in features:
        assert r['min_x'] < r['max_x'] and r['min_y'] < r['max_y'], r
        for k, lo in (('min_x', -80), ('min_y', -80)):
            assert r[k] >= lo, (k, r)
        for k, hi in (('max_x', 80), ('max_y', 80)):
            assert r[k] <= hi, (k, r)

    cands, hits = {}, {}
    for window in windows:
        bounds = window['request']['bounds']
        cands[window['name']] = sorted(fid for fid, _, r in features if may_meet(r, bounds))
        hits[window['name']] = sorted(fid for fid, _, r in features if overlap(r, bounds))
        assert len(hits[window['name']]) == 3, (window['name'], hits[window['name']])
        assert set(hits[window['name']]) <= set(cands[window['name']])

    # True hits and all non-fringe rows must be byte-identical between the
    # two samples; only fringe rectangles are allowed to move.
    other_body, other_features, _ = load('b' if args.sample == 'a' else 'a')
    other_hits = {w['name']: sorted(fid for fid, _, r in other_features
                                    if overlap(r, w['request']['bounds'])) for w in windows}
    assert other_hits == hits, 'true hits differ between samples'
    fixed = {fid: (cls, r) for fid, cls, r in features if not fid.startswith('fringe-')}
    other_fixed = {fid: (cls, r) for fid, cls, r in other_features if not fid.startswith('fringe-')}
    assert fixed == other_fixed, 'non-fringe features differ between samples'
    other_by_id = {fid: (cls, r) for fid, cls, r in other_features}
    moving = [fid for fid, cls, r in features
              if fid.startswith('fringe-') and other_by_id[fid] != (cls, r)]
    assert len(moving) == 180, len(moving)

    result = dict(
        sample=args.sample,
        features=len(features),
        classes=dict(sorted(classes.items())),
        moved_fringe=len(moving),
        candidates_total=sum(len(v) for v in cands.values()),
        hits_total=sum(len(v) for v in hits.values()),
        per_window=[dict(
            window=w['name'],
            candidates=len(cands[w['name']]),
            hits=len(hits[w['name']]),
            candidate_ids=cands[w['name']],
            hit_ids=hits[w['name']],
        ) for w in windows],
    )
    print(json.dumps(result, indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
