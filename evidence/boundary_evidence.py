#!/usr/bin/env python3
"""Runnable evidence for window-boundary semantics of seams and joins.

The script starts a real meridian HTTP service against a temporary data
directory and exercises two edge cases called out in the refactor brief:

1. Shared edges on a window side where only one side's rectangle is visible
   inside the window. Left/bottom window sides are included, right/top sides
   are excluded (half-open window convention).
2. A cross-layer pair whose real intersection exists globally but falls
   entirely outside the query window. The join must not return such a pair.

It prints one JSON document containing both raw responses and a compact
verdict section, and exits non-zero if the semantics change.
"""
import json
import select
import subprocess
import tempfile
from pathlib import Path
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = "/meridian/v1"
SEAM_LAYER = "edge-ware"
LEFT_LAYER = "left-ware"
RIGHT_LAYER = "right-ware"
LAYER_BOUNDS = dict(min_x=-50, min_y=-50, max_x=50, max_y=50)


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def insert(layer, feature, rect):
    return dict(layer=layer, id=feature, mode="insert",
                value=dict(id=feature, **{"class": "fixture"},
                           shape=dict(extent=rect)))


class Client:
    def __init__(self, url):
        self.url = url

    def post(self, path, value):
        request = Request(self.url + PREFIX + path,
                          data=json.dumps(value).encode(),
                          headers={"Content-Type": "application/json"},
                          method="POST")
        with urlopen(request, timeout=5) as response:
            return json.loads(response.read())

    def layer(self, name):
        self.post("/strata", dict(name=name, kind="extent",
                                  bounds=LAYER_BOUNDS, exclusive=False))

    def edit(self, *items):
        return self.post("/edits", dict(edits=list(items)))


def start(binary, data):
    child = subprocess.Popen(
        [str(binary), "-listen", "127.0.0.1:0", "-data", str(data)],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    readable, _, _ = select.select([child.stderr], [], [], 10)
    if not readable:
        raise RuntimeError("service did not start within 10 seconds")
    startup = json.loads(child.stderr.readline())
    if "address" not in startup:
        raise RuntimeError(startup)
    return child, "http://" + startup["address"]


def seed(client):
    client.layer(SEAM_LAYER)
    client.layer(LEFT_LAYER)
    client.layer(RIGHT_LAYER)
    # Seam fixtures: rectangles straddle the four sides of window [0,10)^2.
    # Only one rectangle of each pair is visible inside the window.
    client.edit(
        insert(SEAM_LAYER, "outer-left", box(-6, 2, 0, 8)),
        insert(SEAM_LAYER, "inner-left", box(0, 0, 4, 10)),
        insert(SEAM_LAYER, "outer-top", box(2, 10, 8, 16)),
        insert(SEAM_LAYER, "inner-bottom", box(0, 6, 10, 10)),
        insert(SEAM_LAYER, "outer-right", box(10, 2, 16, 8)),
        insert(SEAM_LAYER, "inner-right", box(6, 0, 10, 10)),
    )
    # Join fixtures: alpha overlaps charlie inside the window and bravo
    # above it; the alpha/bravo intersection lies wholly outside the window.
    client.edit(
        insert(LEFT_LAYER, "alpha", box(4, 4, 6, 12)),
        insert(RIGHT_LAYER, "bravo", box(2, 10, 8, 14)),
        insert(RIGHT_LAYER, "charlie", box(2, 6, 8, 8)),
    )


def observe(client):
    window = box(0, 0, 10, 10)
    seams = client.post(f"/strata/{SEAM_LAYER}/seams",
                        dict(bounds=window, limit=2000))
    join = client.post("/join", dict(left=LEFT_LAYER, right=RIGHT_LAYER,
                                     bounds=window, limit=2000))
    return {"window": window, "seams": seams, "join": join}


def evaluate(observed):
    seam_pairs = sorted((item["first"], item["second"])
                        for item in observed["seams"]["seams"])
    join_pairs = sorted((item["left"], item["right"])
                        for item in observed["join"]["pairs"])
    return {
        "seam_pairs": seam_pairs,
        "join_pairs": join_pairs,
        "expectations": {
            "left_side_seam_included":
                ("inner-left", "outer-left") in seam_pairs,
            "right_side_seam_excluded":
                not any(pair in seam_pairs for pair in (
                    ("inner-right", "outer-right"),
                    ("outer-right", "inner-right"))),
            "top_side_seam_excluded":
                not any(pair in seam_pairs for pair in (
                    ("inner-bottom", "outer-top"),
                    ("outer-top", "inner-bottom"))),
            "outside_window_intersection_dropped":
                ("alpha", "bravo") not in join_pairs,
            "inside_window_intersection_kept":
                ("alpha", "charlie") in join_pairs,
        },
    }


def main():
    with tempfile.TemporaryDirectory(prefix="meridian-evidence-") as temporary:
        root = Path(temporary)
        binary, data = root / "meridian", root / "space"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/meridian"],
                       check=True, cwd=ROOT, timeout=90)
        child, url = start(binary, data)
        try:
            client = Client(url)
            seed(client)
            observed = observe(client)
            report = dict(observed=observed, verdict=evaluate(observed))
        finally:
            if child.poll() is None:
                child.terminate()
                child.communicate(timeout=5)
    print(json.dumps(report, sort_keys=True, indent=2))
    if not all(report["verdict"]["expectations"].values()):
        raise SystemExit("boundary semantics regressed")


if __name__ == "__main__":
    main()
