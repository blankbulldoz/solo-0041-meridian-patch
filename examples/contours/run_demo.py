#!/usr/bin/env python3
"""Reproducible contour demo: starts an isolated Meridian service, seeds three
exclusive extent layers and generates the paper-check contour samples.

Samples:
  ring-open     four rectangles forming a ring with an enclosed opening, plus
                window clipping on three sides
  corner-touch  two rectangles that meet only at one corner point
  paging-stale  a two-page window read interrupted by a same-layer edit; the
                stale signed bookmark is rejected and no contour is assembled

Run via examples/contours/run_demo.sh so that python3 -I and a local build are
prepared. Re-running produces byte-identical JSON (sorted keys, no timestamps).
"""
import hashlib
import json
import select
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TOOL = Path(__file__).resolve().parent / "contour_tool.py"
PREFIX = "/meridian/v1"
LAYER_BOUNDS = {"min_x": -20, "min_y": -20, "max_x": 20, "max_y": 20}

# Window clips the ring on its left (x=2), right (x=10) and bottom (y=2)
# sides; the top side stays inside the window at y=10.
RING_WINDOW = {"min_x": 0, "min_y": 0, "max_x": 12, "max_y": 12}
# Window [0,0)-[12,12) clips the ring on its left side (west arm starts at
# x=0) and bottom side (all arms start at y=0); the east side and top stay
# inside the window. Four edge-touching strips (no positive-area overlap, so
# the exclusive layer accepts them): west/east run full height, north/south
# fill the middle. Their union is a ring with enclosed opening [4,8) x [4,8).
RING_RECTS = {
    "ring-west":  (0, 0, 4, 10),
    "ring-east":  (8, 0, 10, 10),
    "ring-south": (4, 0, 8, 4),
    "ring-north": (4, 8, 8, 10),
}
CORNER_WINDOW = {"min_x": 0, "min_y": 0, "max_x": 12, "max_y": 12}
CORNER_RECTS = {
    "corner-alpha": (1, 1, 5, 5),
    "corner-beta":  (5, 5, 9, 9),
}
STALE_WINDOW = {"min_x": 0, "min_y": 0, "max_x": 10, "max_y": 10}
STALE_RECTS = {
    "stale-one": (1, 1, 4, 4),
    "stale-two": (6, 6, 9, 9),
}


def box(x0, y0, x1, y1):
    return {"min_x": x0, "min_y": y0, "max_x": x1, "max_y": y1}


def request(base, method, path, value=None, status=200):
    from urllib.request import Request, urlopen
    from urllib.error import HTTPError
    data = None
    headers = {}
    if value is not None:
        data = json.dumps(value).encode()
        headers["Content-Type"] = "application/json"
    req = Request(base + PREFIX + path, data=data, method=method, headers=headers)
    try:
        response = urlopen(req, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read()
        body = json.loads(raw) if raw else {}
        assert response.status == status, (method, path, response.status, body)
        return body


def start_service(binary, data_dir):
    child = subprocess.Popen(
        [str(binary), "-listen", "127.0.0.1:0", "-data", str(data_dir)],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    readable, _, _ = select.select([child.stderr], [], [], 15)
    if not readable:
        child.kill()
        raise RuntimeError("service did not start")
    startup = json.loads(child.stderr.readline())
    if "address" not in startup:
        raise RuntimeError(startup)
    return child, "http://" + startup["address"]


def seed_layer(base, name, rects):
    request(base, "POST", "/strata",
            {"name": name, "kind": "extent", "bounds": LAYER_BOUNDS, "exclusive": True},
            status=201)
    edits = [{
        "layer": name,
        "id": feature_id,
        "mode": "insert",
        "value": {"id": feature_id, "class": "block",
                  "shape": {"extent": box(*rectangle)}},
    } for feature_id, rectangle in sorted(rects.items())]
    commit = request(base, "POST", "/edits", {"edits": edits})
    layer_revision = next(
        layer["revision"] for layer in commit["layers"] if layer["name"] == name)
    return layer_revision


def run_tool(args):
    result = subprocess.run([sys.executable, str(TOOL), *args],
                            capture_output=True, text=True, check=True)
    return result.stdout.strip()


def main():
    out_dir = Path(__file__).resolve().parent / "samples"
    with tempfile.TemporaryDirectory(prefix="meridian-contour-") as temporary:
        root = Path(temporary)
        binary, data_dir = root / "meridian", root / "space"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/meridian"],
                       cwd=ROOT, check=True, timeout=120)
        child, base = start_service(binary, data_dir)
        try:
            ring_rev = seed_layer(base, "ring-open", RING_RECTS)
            corner_rev = seed_layer(base, "corner-touch", CORNER_RECTS)
            stale_rev = seed_layer(base, "paging-stale", STALE_RECTS)

            run_tool([
                "--url", base, "--layer", "ring-open",
                "--window", "0,0,12,12", "--page-limit", "2",
                "--out", str(out_dir), "--name", "ring-open",
            ])
            run_tool([
                "--url", base, "--layer", "corner-touch",
                "--window", "0,0,12,12", "--page-limit", "5",
                "--out", str(out_dir), "--name", "corner-touch",
            ])
            run_tool([
                "--url", base, "--layer", "paging-stale",
                "--window", "0,0,10,10", "--page-limit", "1",
                "--out", str(out_dir), "--name", "paging-stale",
                "--staleness-insert", json.dumps(
                    {"id": "stale-three", "rectangle": [2, 6, 3, 7]}),
            ])
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.communicate(timeout=10)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.communicate(timeout=10)

    failures = []

    def load(name):
        return json.loads((out_dir / f"{name}.contour.json").read_text())

    ring = load("ring-open")
    kinds = ring["cross_checks"]["counts_by_kind"]
    if kinds != {"outer": 2, "hole": 4, "window-cut": 2}:
        failures.append(("ring kinds", kinds))
    # Layer revision is the identity of this layer's geometry; the global
    # revision keeps advancing while later layers are seeded in the same database.
    if ring["snapshot"]["layer_revision"] != ring_rev:
        failures.append(("ring layer revision", ring["snapshot"]))
    if not ring["cross_checks"]["seams_absent_from_contour"]:
        failures.append(("ring seam leak", ring["cross_checks"]))
    if ring["cross_checks"]["server_seam_count"] != 4:
        failures.append(("ring seam count", ring["cross_checks"]))
    if not ring["pagination"]["single_layer_revision"] or ring["pagination"]["pages_read"] != 2:
        failures.append(("ring pagination", ring["pagination"]))
    if ring["cross_checks"]["coverage"]["covered"] != 84:
        failures.append(("ring coverage", ring["cross_checks"]["coverage"]))
    if not ring["cross_checks"]["covered_on_left_holds"]:
        failures.append(("ring orientation", ring["cross_checks"]["orientation_offenders"]))

    corner = load("corner-touch")
    c_kinds = corner["cross_checks"]["counts_by_kind"]
    if c_kinds != {"outer": 8, "hole": 0, "window-cut": 0}:
        failures.append(("corner kinds", c_kinds))
    if corner["cross_checks"]["server_seam_count"] != 0:
        failures.append(("corner seam count", corner["cross_checks"]))
    # The two blocks stay separate outlines: no line may cite both feature IDs.
    for line in corner["contour_lines"]:
        ids = line["source_feature_ids"]
        if len(ids) != 1:
            failures.append(("corner merged line", line))
    if not corner["cross_checks"]["covered_on_left_holds"]:
        failures.append(("corner orientation", corner["cross_checks"]["orientation_offenders"]))

    stale = load("paging-stale")
    if not stale.get("aborted") or stale.get("http_status") != 409:
        failures.append(("staleness abort", stale))
    if stale.get("contour_lines"):
        failures.append(("staleness leaked lines", stale["contour_lines"]))
    if stale.get("layer") != "paging-stale" or stale_rev == 0:
        failures.append(("staleness metadata", stale))

    # Determinism: rerun the ring sample against a fresh service and compare.
    with tempfile.TemporaryDirectory(prefix="meridian-contour-re-") as temporary:
        root = Path(temporary)
        binary2, data2 = root / "meridian", root / "space"
        subprocess.run(["go", "build", "-o", str(binary2), "./cmd/meridian"],
                       cwd=ROOT, check=True, timeout=120)
        child2, base2 = start_service(binary2, data2)
        try:
            seed_layer(base2, "ring-open", RING_RECTS)
            rerun_dir = root / "rerun"
            run_tool([
                "--url", base2, "--layer", "ring-open",
                "--window", "0,0,12,12", "--page-limit", "2",
                "--out", str(rerun_dir), "--name", "ring-open",
            ])
        finally:
            if child2.poll() is None:
                child2.terminate()
                child2.communicate(timeout=10)
        first = (out_dir / "ring-open.contour.json").read_bytes()
        second = (rerun_dir / "ring-open.contour.json").read_bytes()
        if hashlib.sha256(first).digest() != hashlib.sha256(second).digest():
            failures.append(("rerun hash differs", None))

    if failures:
        print("DEMO FAILED:")
        for name, detail in failures:
            print(" -", name, detail)
        sys.exit(1)

    summary = {
        "ring_revision": ring_rev,
        "corner_revision": corner_rev,
        "stale_layer_revision_before_abort": stale_rev,
        "ring_lines": ring["cross_checks"]["line_count"],
        "corner_lines": corner["cross_checks"]["line_count"],
        "stale_aborted": stale["aborted"],
        "artifacts": sorted(p.name for p in out_dir.iterdir()),
    }
    print(json.dumps(summary, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
