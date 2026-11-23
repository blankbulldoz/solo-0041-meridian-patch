#!/usr/bin/env python3
"""Generate continuous, moving writes against a Meridian Patch service.

Every batch is one atomic edit package. Each successful batch advances the
database by exactly one revision and is appended to an operation log, so the
revision numbers captured in backup metadata can later be mapped to the exact
operations that are lost when an older restore point is chosen.

Used by the backup/restore drill to prove that backups capture single
commits while the online database keeps moving.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mpb_common as common  # noqa: E402

FIELD, BED = "ochre-field", "mica-bed"
BOUNDS = common.box(-20, -20, 20, 20)


def box_edit(layer, feature_id, x0, y0, category="fixture", mode="insert"):
    return {
        "layer": layer, "id": feature_id, "mode": mode,
        "value": {"id": feature_id, "class": category,
                  "shape": {"extent": common.box(x0, y0, x0 + 2, y0 + 2)}},
    }


def ensure_layers(url):
    status, _, body = common.http_json(url, "GET", "/strata")
    existing = {layer["name"] for layer in body["layers"]}
    for name in (FIELD, BED):
        if name not in existing:
            common.http_json(url, "POST", "/strata",
                             {"name": name, "kind": "extent", "bounds": BOUNDS, "exclusive": False})


def write_batch(url, seq, log_handle):
    # Features move along a small grid: every batch inserts one new rectangle,
    # slides an earlier one (replace) and eventually removes the oldest. Bounds
    # stay within [-20, 20] so every package is accepted.
    x = -16 + (seq % 14) * 2
    y = (seq % 4) * 2
    new_id = f"move-{seq:04d}"
    edits = [box_edit(FIELD, new_id, x, y, category="moving")]
    if seq >= 2:
        prior = f"move-{seq - 2:04d}"
        edits.append(box_edit(FIELD, prior, (x + 4) % 32 - 16, (y + 2) % 8 - 4,
                              category="moving", mode="replace"))
    if seq % 5 == 4:
        bed_id = f"bed-cycle-{seq // 5:04d}"
        edits.append(box_edit(BED, bed_id, (x + 2) % 32 - 16, y))
    if seq >= 7 and seq % 3 == 0:
        edits.append({"layer": FIELD, "id": f"move-{seq - 7:04d}", "mode": "remove"})

    status, _, body = common.http_json(url, "POST", "/edits", {"edits": edits})
    if status != 200:
        raise RuntimeError(f"edit batch {seq} rejected: {status} {body}")
    event = {"seq": seq, "revision": body["revision"], "at": common.utc_now(),
             "operations": [{"layer": e["layer"], "id": e["id"], "mode": e["mode"]} for e in edits]}
    log_handle.write(json.dumps(event, sort_keys=True) + "\n")
    log_handle.flush()
    os.fsync(log_handle.fileno())
    return event


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:8741")
    parser.add_argument("--batches", type=int, default=20, help="number of edit batches (0 = until stopped)")
    parser.add_argument("--interval-seconds", type=float, default=0.25)
    parser.add_argument("--op-log", required=True, help="append-only JSONL log of every committed batch")
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    ensure_layers(args.url)
    log_path = Path(args.op_log)
    log_path.parent.mkdir(parents=True, exist_ok=True)
    seq = 0
    if log_path.exists():
        with open(log_path, encoding="utf-8") as handle:
            for line in handle:
                if line.strip():
                    seq = max(seq, json.loads(line)["seq"] + 1)
    with open(log_path, "a", encoding="utf-8") as log_handle:
        count = 0
        while args.batches == 0 or count < args.batches:
            write_batch(args.url, seq, log_handle)
            seq += 1
            count += 1
            if args.interval_seconds:
                time.sleep(args.interval_seconds)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
