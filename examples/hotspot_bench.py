#!/usr/bin/env python3
"""Measure global vs hotspot-local window query cost on two data shapes.

Scenario "points" concentrates a small share of a mark layer inside one
hotspot cell while the rest of the marks are scattered across the layer.
Scenario "rects" fills an extent layer with thin tall rectangles that cross
every horizontal cut line, so all of them land in the grid's spill tree.

Both scenarios query the same window through the global tree and through
the hotspot grid, assert identical result sets, and report the visit cost
(nodes and candidates) of each path.
"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
from walkthrough import ROOT, box, op, running

LIMIT = 512  # catalog.MaxEdits: one edit batch holds at most 512 operations


def seed(s, layer, kind, cuts, shapes):
    s.post('/strata', dict(name=layer, kind=kind, bounds=box(-1000, -1000, 1000, 1000),
                           exclusive=False, cuts=cuts), 201)
    for start in range(0, len(shapes), LIMIT):
        batch = [op(layer, f'f-{start + k:05d}', shape)
                 for k, shape in enumerate(shapes[start:start + LIMIT])]
        s.edit(*batch)


def collect(s, layer, window, mode=None):
    """Paginate a window query to completion; return ids and first-page cost."""
    query = dict(bounds=window, limit=256)
    if mode:
        query['index'] = mode
    ids, visit, cells = [], None, 0
    while True:
        page = s.query(layer, 'window', query)
        ids.extend(row['id'] for row in page['features'])
        if visit is None:
            visit = page['visit']
        cells = max(cells, page.get('cells', 0))
        if 'next' not in page:
            return ids, visit, cells
        query['bookmark'] = page['next']


def compare(s, layer, window):
    glob_ids, glob_visit, _ = collect(s, layer, window)
    loc_ids, loc_visit, cells = collect(s, layer, window, mode='local')
    assert glob_ids == loc_ids, (len(glob_ids), len(loc_ids))
    assert len(loc_ids) == len(set(loc_ids))
    return dict(matches=len(loc_ids), cells=cells,
                global_visit=glob_visit, local_visit=loc_visit)


def concentrated_points(s):
    # 500 marks inside the hotspot cell [0,100)x[0,100); 3596 marks scattered
    # over the rest of the layer with x ranks interleaving the hotspot, so
    # the packed tree cannot isolate the hotspot by a single axis split.
    inside = [dict(x=1 + (i * 7) % 99, y=1 + (i * 13) % 99) for i in range(500)]
    outside = []
    for i in range(3596):
        x, y = -1000 + (i * 37) % 1999, -1000 + (i * 91) % 1999
        if 0 <= y < 100:
            y -= 200
        outside.append(dict(x=x, y=y))
    seed(s, 'pins', 'mark', dict(x=[0, 100], y=[0, 100]), inside + outside)
    return compare(s, 'pins', box(10, 10, 90, 90))


def thin_crossing_rects(s):
    # 2000 thin tall rectangles, each crossing all three y cut lines.
    rects = [box(-990 + (i * 13) % 1960, -990, -986 + (i * 13) % 1960, 990)
             for i in range(2000)]
    seed(s, 'rails', 'extent', dict(y=[-500, 0, 500]), rects)
    return compare(s, 'rails', box(-100, -100, 100, 100))


def main():
    with tempfile.TemporaryDirectory(prefix='meridian-bench-') as temporary:
        root = Path(temporary)
        binary, data = root / 'meridian', root / 'space'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'],
                       check=True, cwd=ROOT, timeout=90)
        with running(binary, data) as s:
            report = dict(concentrated_points=concentrated_points(s),
                          thin_crossing_rects=thin_crossing_rects(s))
        print(json.dumps(report, sort_keys=True, indent=2))


if __name__ == '__main__':
    main()
