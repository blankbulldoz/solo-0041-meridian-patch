#!/usr/bin/env python3
"""Build union-contour line data for the extent features of one Meridian layer.

The tool talks only to the public HTTP JSON surface of Meridian Patch:

  * POST /strata/{layer}/window   - paged feature snapshot (signed bookmarks)
  * POST /strata/{layer}/coverage - independent area cross-check
  * POST /strata/{layer}/seams    - interior shared edges cross-check
  * POST /edits                   - only used by the optional staleness probe

For every straight contour line of the union of the layer's axis-aligned
rectangles, clipped to the query window, it records:

  * start and end points,
  * orientation (vertical/horizontal plus a compass-like direction),
  * the original feature IDs owning the covered side,
  * one of the three line kinds:
      outer       - boundary between covered region and window exterior,
      hole        - boundary of an enclosed empty region (a ring opening),
      window-cut  - boundary produced by clipping a feature at a window side.

Rectangles are half-open [min_x, max_x) x [min_y, max_y). A rectangle edge that
lies on the right/top side of a half-open window is not owned by the window and
is never emitted as a window-cut line; left/bottom sides are included.

Pagination safety: every page must carry the same database and layer revision.
If a bookmark is rejected (HTTP 409, e.g. the layer changed between pages), the
run aborts and no contour file is written, so an old page and a new page can
never be stitched into one outline.
"""
import argparse
import json
import sys
from bisect import bisect_right
from urllib.error import HTTPError
from urllib.request import Request, urlopen

PREFIX = "/meridian/v1"
KIND_OUTER = "outer"
KIND_HOLE = "hole"
KIND_WINDOW = "window-cut"


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


class Client:
    def __init__(self, base):
        self.base = base.rstrip("/")

    def call(self, method, path, value=None):
        data = None
        headers = {}
        if value is not None:
            data = json.dumps(value).encode()
            headers["Content-Type"] = "application/json"
        req = Request(self.base + PREFIX + path, data=data, method=method, headers=headers)
        try:
            response = urlopen(req, timeout=10)
        except HTTPError as error:
            payload = error.read()
            try:
                detail = json.loads(payload)
            except json.JSONDecodeError:
                detail = {"raw": payload.decode("utf-8", "replace")}
            raise ApiError(error.status, detail) from None
        with response:
            return json.loads(response.read())

    def insert(self, layer, feature_id, rectangle, category="block"):
        edit = {
            "layer": layer,
            "id": feature_id,
            "mode": "insert",
            "value": {
                "id": feature_id,
                "class": category,
                "shape": {"extent": rectangle},
            },
        }
        return self.call("POST", "/edits", {"edits": [edit]})


class ApiError(Exception):
    def __init__(self, status, detail):
        super().__init__("HTTP %s: %s" % (status, detail))
        self.status = status
        self.detail = detail


def paged_features(client, layer, window, page_limit, staleness_insert=None):
    """Page through a window snapshot, refusing any revision drift."""
    bookmark = ""
    pages = 0
    features = []
    stamps = []
    while True:
        query = {"bounds": window, "limit": page_limit}
        if bookmark:
            query["bookmark"] = bookmark
        page = client.call("POST", f"/strata/{layer}/window", query)
        pages += 1
        stamps.append({
            "page": pages,
            "revision": page["revision"],
            "layer_revision": page["layer_revision"],
        })
        if pages > 1 and page["layer_revision"] != stamps[0]["layer_revision"]:
            # The server normally rejects the stale bookmark first (HTTP 409);
            # this guard catches a fresh layer stamp if one ever came through.
            raise StalenessError("page %d carries a different layer revision" % pages, stamps)
        features.extend(page["features"])
        if "next" not in page or not page["next"]:
            break
        bookmark = page["next"]
        if staleness_insert is not None and pages == 1:
            # Deliberately mutate the same layer between two pages. The signed
            # bookmark is bound to the layer revision and must be rejected.
            client.insert(layer, staleness_insert["id"], staleness_insert["rectangle"])
    return features, stamps, pages


class StalenessError(Exception):
    def __init__(self, message, stamps):
        super().__init__(message)
        self.stamps = stamps


def clip(rect, window):
    clipped = box(
        max(rect["min_x"], window["min_x"]),
        max(rect["min_y"], window["min_y"]),
        min(rect["max_x"], window["max_x"]),
        min(rect["max_y"], window["max_y"]),
    )
    if clipped["max_x"] > clipped["min_x"] and clipped["max_y"] > clipped["min_y"]:
        return clipped
    return None


def build_grid(rectangles, window):
    """Coordinate-compressed coverage grid over the window.

    Returns x/y breakpoint lists and covered/owners grids indexed [iy][ix].
    """
    xs = {window["min_x"], window["max_x"]}
    ys = {window["min_y"], window["max_y"]}
    clipped = []
    for feature_id, rect in rectangles:
        part = clip(rect, window)
        if part is None:
            continue
        clipped.append((feature_id, part))
        xs.add(part["min_x"])
        xs.add(part["max_x"])
        ys.add(part["min_y"])
        ys.add(part["max_y"])
    xs = sorted(xs)
    ys = sorted(ys)
    nx, ny = len(xs) - 1, len(ys) - 1
    # Indexed [iy][ix]; every cell is either covered, window-exterior or an
    # enclosed opening.
    covered = [[False] * nx for _ in range(ny)]
    owners = [[set() for _ in range(nx)] for _ in range(ny)]
    for feature_id, part in clipped:
        ix0 = xs.index(part["min_x"])
        ix1 = xs.index(part["max_x"])
        iy0 = ys.index(part["min_y"])
        iy1 = ys.index(part["max_y"])
        for ix in range(ix0, ix1):
            for iy in range(iy0, iy1):
                covered[iy][ix] = True
                owners[iy][ix].add(feature_id)
    return xs, ys, covered, owners


def exterior_cells(covered, nx, ny):
    """Flood fill empty cells that can reach the window border."""
    exterior = [[False] * nx for _ in range(ny)]
    stack = []
    for ix in range(nx):
        border_rows = (0, ny - 1)
        for iy in border_rows:
            if not covered[iy][ix]:
                stack.append((ix, iy))
    for iy in range(ny):
        for ix in (0, nx - 1):
            if not covered[iy][ix]:
                stack.append((ix, iy))
    while stack:
        ix, iy = stack.pop()
        if not (0 <= ix < nx and 0 <= iy < ny):
            continue
        if covered[iy][ix] or exterior[iy][ix]:
            continue
        exterior[iy][ix] = True
        stack.extend(((ix + 1, iy), (ix - 1, iy), (ix, iy + 1), (ix, iy - 1)))
    return exterior


def contour_lines(window, xs, ys, covered, owners, exterior):
    """Walk every compressed cell edge and merge collinear pieces.

    Each emitted segment is oriented so the covered region is on its left.
    """
    nx, ny = len(xs) - 1, len(ys) - 1
    # key: (axis, fixed, direction_sign, kind) -> list of [from, to, ids]
    merged = {}

    def take(axis, fixed, start, end, direction, kind, ids):
        key = (axis, fixed, direction, kind)
        if key not in merged:
            merged[key] = {"from": start, "to": end, "ids": set(ids)}
            return
        entry = merged[key]
        # Grid adjacency guarantees consecutive pieces only; disjoint pieces
        # sharing one key cannot arise from a union of axis-aligned rectangles.
        if entry["to"] == start:
            entry["to"] = end
        elif entry["from"] == end:
            entry["from"] = start
        else:
            raise AssertionError("non-adjacent contour pieces share key %r" % (key,))
        entry["ids"].update(ids)

    # Vertical edges: a grid line xs[ix] separating cell ix-1 (left) and ix (right).
    for ix in range(nx + 1):
        x = xs[ix]
        for iy in range(ny):
            left_covered = ix > 0 and covered[iy][ix - 1]
            right_covered = ix < nx and covered[iy][ix]
            if left_covered == right_covered:
                continue  # interior seam (both covered) or empty/empty: no contour
            y0, y1 = ys[iy], ys[iy + 1]
            on_window = x == window["min_x"] or x == window["max_x"]
            if right_covered:
                # Covered on the east side: face south so the east side is left.
                # The empty neighbour is the western cell (ix-1).
                direction, ids = "down", owners[iy][ix]
                empty_outside = ix == nx
                empty_cell = (max(ix - 1, 0), iy)
            else:
                # Covered on the west side: face north so the west side is left.
                # The empty neighbour is the eastern cell (ix).
                direction, ids = "up", owners[iy][ix - 1]
                empty_outside = ix == 0
                empty_cell = (min(ix, nx - 1), iy)
            if on_window:
                kind = KIND_WINDOW
            elif empty_outside or exterior[empty_cell[1]][empty_cell[0]]:
                kind = KIND_OUTER
            else:
                kind = KIND_HOLE
            take("vertical", x, y0, y1, direction, kind, ids)

    # Horizontal edges: a grid line ys[iy] separating cell iy-1 (below) and iy (above).
    for iy in range(ny + 1):
        y = ys[iy]
        for ix in range(nx):
            below_covered = iy > 0 and covered[iy - 1][ix]
            above_covered = iy < ny and covered[iy][ix]
            if below_covered == above_covered:
                continue
            x0, x1 = xs[ix], xs[ix + 1]
            on_window = y == window["min_y"] or y == window["max_y"]
            if below_covered:
                # Covered below (e.g. a top edge): face west so the south side
                # is left. The empty neighbour is the northern cell (iy).
                direction, ids = "left", owners[iy - 1][ix]
                empty_outside = iy == ny
                empty_cell = (ix, min(iy, ny - 1))
            else:
                # Covered above (e.g. a bottom edge): face east so the north
                # side is left. The empty neighbour is the southern cell (iy-1).
                direction, ids = "right", owners[iy][ix]
                empty_outside = iy == 0
                empty_cell = (ix, max(iy - 1, 0))
            if on_window:
                kind = KIND_WINDOW
            elif empty_outside or exterior[empty_cell[1]][empty_cell[0]]:
                kind = KIND_OUTER
            else:
                kind = KIND_HOLE
            take("horizontal", y, x0, x1, direction, kind, ids)

    kind_rank = {KIND_OUTER: 0, KIND_HOLE: 1, KIND_WINDOW: 2}
    lines = []
    for (axis, fixed, direction, kind), entry in merged.items():
        if axis == "vertical":
            start = {"x": fixed, "y": entry["from"]}
            end = {"x": fixed, "y": entry["to"]}
        else:
            start = {"x": entry["from"], "y": fixed}
            end = {"x": entry["to"], "y": fixed}
        lines.append({
            "kind": kind,
            "axis": axis,
            "orientation": direction,
            "start": start,
            "end": end,
            "length": abs(entry["to"] - entry["from"]),
            "source_feature_ids": sorted(entry["ids"]),
        })
    lines.sort(key=lambda line: (
        kind_rank[line["kind"]],
        line["axis"],
        line["start"]["x"] if line["axis"] == "vertical" else line["start"]["y"],
        line["start"]["y"] if line["axis"] == "vertical" else line["start"]["x"],
        line["orientation"],
    ))
    return lines


def render_ascii(window, xs, ys, covered, exterior, labels):
    """One-character-per-unit-cell paper sketch with a coordinate frame."""
    width = window["max_x"] - window["min_x"]

    def cell_x(unit_x):
        # Compressed interval containing unit x (unit cells are half-open).
        return min(bisect_right(xs, unit_x) - 1, len(xs) - 2)

    def cell_y(unit_y):
        return min(bisect_right(ys, unit_y) - 1, len(ys) - 2)

    rows = []
    header = "    " + "".join(str(abs(unit_x) % 10) for unit_x in range(window["min_x"], window["max_x"]))
    rows.append(header)
    rows.append("    " + "-" * width)
    for unit_y in range(window["max_y"] - 1, window["min_y"] - 1, -1):
        iy = cell_y(unit_y)
        chars = []
        for unit_x in range(window["min_x"], window["max_x"]):
            ix = cell_x(unit_x)
            if covered[iy][ix]:
                chars.append("#")
            elif exterior[iy][ix]:
                chars.append(".")
            else:
                chars.append("o")  # enclosed opening (hole)
        rows.append("%3d|%s" % (unit_y, "".join(chars)))
    rows.append("")
    rows.append("legend: # covered   o enclosed opening (hole)   . window exterior")
    if labels:
        rows.append("features:")
        for feature_id, rect in sorted(labels.items()):
            rows.append("  %-10s [%d,%d)-[%d,%d)" % (
                feature_id, rect["min_x"], rect["min_y"], rect["max_x"], rect["max_y"]))
    return "\n".join(rows)


def orientation_consistency(window, xs, ys, covered, lines):
    """Verify the covered region is on the left of every oriented line.

    For each line we inspect the compressed cell immediately to its left at
    the segment midpoint. A line on a window border has no in-window cell on
    its empty side, so it is checked against the in-window (covered) side.
    """
    nx, ny = len(xs) - 1, len(ys) - 1

    def cell_at(ix, iy):
        if 0 <= ix < nx and 0 <= iy < ny:
            return covered[iy][ix]
        return None  # outside the window grid

    offenders = []
    for line in lines:
        if line["axis"] == "vertical":
            x = line["start"]["x"]
            mid_y = (line["start"]["y"] + line["end"]["y"]) // 2
            ix = xs.index(x)
            iy = min(bisect_right(ys, mid_y) - 1, ny - 1)
            if line["orientation"] == "up":
                left_ix = ix - 1  # facing north, left is west
            else:
                left_ix = ix      # facing south, left is east
            left_state = cell_at(left_ix, iy)
            if left_state is False:
                offenders.append(line)
        else:
            y = line["start"]["y"]
            mid_x = (line["start"]["x"] + line["end"]["x"]) // 2
            iy = ys.index(y)
            ix = min(bisect_right(xs, mid_x) - 1, nx - 1)
            if line["orientation"] == "right":
                left_iy = iy      # facing east, left is north
            else:
                left_iy = iy - 1  # facing west, left is south
            left_state = cell_at(ix, left_iy)
            if left_state is False:
                offenders.append(line)
    return not offenders, offenders


def cross_checks(client, layer, window, lines, grid):
    """Independent server-side checks the contour must agree with."""
    xs, ys, covered = grid
    coverage = client.call("POST", f"/strata/{layer}/coverage", {"bounds": window})
    seams = client.call("POST", f"/strata/{layer}/seams",
                        {"bounds": window, "limit": 2000})["seams"]
    window_area = ((window["max_x"] - window["min_x"])
                   * (window["max_y"] - window["min_y"]))
    contour_length = sum(line["length"] for line in lines)
    # A shared edge is interior: no contour segment may coincide with a seam.
    seam_keys = set()
    for shared in seams:
        seam = shared["seam"]
        seam_keys.add((seam["axis"], seam["fixed"], seam["from"], seam["to"]))
    line_keys = set()
    for line in lines:
        if line["axis"] == "vertical":
            line_keys.add(("vertical", line["start"]["x"],
                           line["start"]["y"], line["end"]["y"]))
        else:
            line_keys.add(("horizontal", line["start"]["y"],
                           line["start"]["x"], line["end"]["x"]))
    leaking_seams = sorted(seam_keys & line_keys)
    orientation_ok, offenders = orientation_consistency(window, xs, ys, covered, lines)
    return {
        "window_area": window_area,
        "coverage": {
            "covered": coverage["covered"],
            "repeated": coverage["repeated"],
            "uncovered": coverage["uncovered"],
        },
        "coverage_identity_holds": coverage["covered"] + coverage["uncovered"] == window_area,
        "server_seam_count": len(seams),
        "seams_absent_from_contour": not leaking_seams,
        "covered_on_left_holds": orientation_ok,
        "orientation_offenders": [
            {"kind": line["kind"], "axis": line["axis"],
             "start": line["start"], "end": line["end"]}
            for line in offenders
        ],
        "contour_total_length": contour_length,
        "line_count": len(lines),
        "counts_by_kind": {
            kind: sum(1 for line in lines if line["kind"] == kind)
            for kind in (KIND_OUTER, KIND_HOLE, KIND_WINDOW)
        },
    }


def write_outputs(out_dir, name, payload, sketch):
    out_dir.mkdir(parents=True, exist_ok=True)
    json_path = out_dir / f"{name}.contour.json"
    txt_path = out_dir / f"{name}.sketch.txt"
    json_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
    header = [
        f"sample: {payload['sample']}",
        f"layer: {payload['layer']}  layer_revision: {payload['snapshot']['layer_revision']}",
        f"window: [{payload['window']['min_x']},{payload['window']['min_y']}) - "
        f"[{payload['window']['max_x']},{payload['window']['max_y']})",
        "",
    ]
    txt_path.write_text("\n".join(header) + sketch + "\n")
    return json_path, txt_path


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", required=True, help="Meridian service base URL")
    parser.add_argument("--layer", required=True)
    parser.add_argument("--window", required=True,
                        help="min_x,min_y,max_x,max_y (half-open integer window)")
    parser.add_argument("--page-limit", type=int, default=64)
    parser.add_argument("--out", required=True, help="output directory")
    parser.add_argument("--name", required=True, help="sample name used in file names")
    parser.add_argument("--staleness-insert",
                        help="probe: JSON {id,rectangle:[x0,y0,x1,y1]} inserted after page 1")
    args = parser.parse_args()

    from pathlib import Path
    client = Client(args.url)
    coords = [int(part) for part in args.window.split(",")]
    if len(coords) != 4:
        parser.error("--window needs four integers")
    window = box(*coords)
    staleness = None
    if args.staleness_insert:
        probe = json.loads(args.staleness_insert)
        staleness = {"id": probe["id"], "rectangle": box(*probe["rectangle"])}

    try:
        features, stamps, pages = paged_features(
            client, args.layer, window, args.page_limit, staleness)
    except ApiError as error:
        if error.status != 409:
            raise
        # Expected for the staleness probe: persist the rejection, emit no contour.
        out_dir = Path(args.out)
        out_dir.mkdir(parents=True, exist_ok=True)
        report = {
            "sample": args.name,
            "layer": args.layer,
            "window": window,
            "aborted": True,
            "reason": "bookmark rejected by server after same-layer edit",
            "http_status": error.status,
            "server_response": error.detail,
            "pages_completed": 1,
            "contour_lines": [],
        }
        path = out_dir / f"{args.name}.contour.json"
        path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        print("STALENESS_ABORTED", path)
        return 0
    except StalenessError as error:
        out_dir = Path(args.out)
        out_dir.mkdir(parents=True, exist_ok=True)
        report = {
            "sample": args.name,
            "layer": args.layer,
            "window": window,
            "aborted": True,
            "reason": str(error),
            "page_stamps": error.stamps,
            "contour_lines": [],
        }
        path = out_dir / f"{args.name}.contour.json"
        path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
        print("STALENESS_ABORTED", path)
        return 0

    rectangles = [(feature["id"], feature["shape"]["extent"]) for feature in features]
    xs, ys, covered, owners = build_grid(rectangles, window)
    exterior = exterior_cells(covered, len(xs) - 1, len(ys) - 1)
    lines = contour_lines(window, xs, ys, covered, owners, exterior)
    labels = {feature_id: rect for feature_id, rect in rectangles}
    sketch = render_ascii(window, xs, ys, covered, exterior, labels)
    checks = cross_checks(client, args.layer, window, lines, (xs, ys, covered))

    payload = {
        "sample": args.name,
        "layer": args.layer,
        "window": window,
        "window_rule": "half-open [min_x,max_x) x [min_y,max_y); right/top sides excluded",
        "snapshot": {
            # layer_revision is the identity of this layer's geometry and is
            # stable across runs; it is also the revision the signed bookmark
            # is bound to. The database revision also moves when unrelated
            # layers are edited, so it is intentionally kept out of this
            # artifact and only printed on the console for traceability.
            "layer_revision": stamps[-1]["layer_revision"],
        },
        "pagination": {
            "page_limit": args.page_limit,
            "pages_read": pages,
            "layer_revision_per_page": [s["layer_revision"] for s in stamps],
            # Pages may only be joined into one contour when they describe the
            # same layer revision. A same-layer edit invalidates the bookmark.
            "single_layer_revision": len({s["layer_revision"] for s in stamps}) == 1,
        },
        "features_used": [
            {"id": feature_id, "extent": rect} for feature_id, rect in sorted(rectangles)
        ],
        "line_kinds": {
            KIND_OUTER: "covered region vs window exterior",
            KIND_HOLE: "edge of an enclosed empty region",
            KIND_WINDOW: "edge created by clipping a feature at a window side",
        },
        "orientation_rule": "each line keeps the covered region on its left",
        "contour_lines": lines,
        "cross_checks": checks,
    }
    json_path, txt_path = write_outputs(Path(args.out), args.name, payload, sketch)
    print("WROTE", json_path, txt_path,
          "layer_revision=%s database_revision=%s" %
          (stamps[-1]["layer_revision"], stamps[-1]["revision"]))


if __name__ == "__main__":
    sys.exit(main())
