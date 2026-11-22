#!/usr/bin/env python3
"""Build the legacy source database in a temporary directory and export the
old image (input/old-image.mpb) that the handover converts.

The source layer `legacy-parcels` uses the old wide, non-exclusive layout.
Its rectangles are chosen to exercise every conversion case:

  fully inside target bounds, no conflicts      alpha, bravo
  crossing the target boundary, clean clip      charlie, delta, mike
  fully outside the target boundary             echo, foxtrot
  edge-touching the boundary (zero-area clip)   golf
  mutually exclusive pairs/triple inside bounds hotel/india, juliett/kilo,
                                                november/oscar/papa

A second layer `control-marks` rides along unchanged to prove the
conversion is selective.
"""
import argparse
import json
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import client
import mpb

SOURCE_LAYER = "legacy-parcels"
PASS_THROUGH_LAYER = "control-marks"
SOURCE_BOUNDS = mpb.box(-64, -64, 96, 96)

RECTANGLES = [
    # (id, class, (min_x, min_y, max_x, max_y))
    ("alpha", "survey", (0, 0, 8, 8)),
    ("bravo", "fixture", (12, 0, 20, 6)),
    ("charlie", "fixture", (-12, 0, -4, 6)),
    ("delta", "survey", (30, 18, 48, 28)),
    ("mike", "fixture", (34, 4, 48, 10)),
    ("echo", "survey", (44, 4, 56, 10)),
    ("foxtrot", "fixture", (0, 30, 10, 40)),
    ("golf", "survey", (-12, 2, -8, 9)),
    ("hotel", "survey", (24, 8, 32, 16)),
    ("india", "fixture", (26, 2, 33, 10)),
    ("juliett", "fixture", (22, -6, 28, 0)),
    ("kilo", "survey", (24, -4, 30, 2)),
    ("november", "survey", (4, 12, 12, 20)),
    ("oscar", "fixture", (8, 14, 16, 22)),
    ("papa", "survey", (14, 12, 22, 20)),
]

MARKS = [
    ("brad", "control", (2, 2)),
    ("pin", "control", (-3, 1)),
    ("stud", "control", (5, 7)),
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, help="path to the meridian server binary")
    parser.add_argument("--out", required=True, help="output path for the old MPB image")
    args = parser.parse_args()

    with tempfile.TemporaryDirectory(prefix="meridian-source-") as data_dir:
        with client.running_server(args.binary, data_dir) as (session, startup):
            session.create_layer(SOURCE_LAYER, "extent", SOURCE_BOUNDS, False)
            session.create_layer(PASS_THROUGH_LAYER, "mark", SOURCE_BOUNDS, True)
            edits = [client.insert_extent(SOURCE_LAYER, fid, cls, rect)
                     for fid, cls, rect in RECTANGLES]
            edits += [client.insert_mark(PASS_THROUGH_LAYER, fid, cls, xy)
                      for fid, cls, xy in MARKS]
            commit = session.edit(*edits)
            raw, header_revision = session.export_image()
            status = session.get("/status")

    image = mpb.decode(raw)  # fail loudly if the export is not a valid image
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_bytes(raw)
    summary = {
        "image": str(out),
        "bytes": len(raw),
        "sha256": mpb.sha256_hex(raw),
        "meridian_revision_header": header_revision,
        "image_revision": image["revision"],
        "status_revision": status["revision"],
        "commit_revision": commit["revision"],
        "layers": {layer["definition"]["name"]: len(layer["features"])
                   for layer in image["layers"]},
    }
    print(json.dumps(summary, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
