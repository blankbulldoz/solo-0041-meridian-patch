#!/usr/bin/env python3
"""Independent re-check of the blind-review answer key from raw coordinates.

This script deliberately does NOT import or call Meridian. It re-derives every
relation purely from the integer rectangle coordinates stored in the blind
input, using the documented contract in PROJECT_SPEC.md:

* rectangles are half-open [min_x,max_x) x [min_y,max_y);
* intersection requires a POSITIVE area;
* containment is directional and uses inclusive box bounds;
* distance is the closed-envelope Euclidean distance (squared);
* an intersection box exists only when both clipped width and height are > 0.

It then cross-checks, for every ordered row:
  1. recomputed flags vs the answer key;
  2. answer key vs the saved raw HTTP response (key must be a faithful copy);
  3. the derived unordered-pair class vs the pair-level target class;
  4. group-leakage: both directions of a pair share one group, and no pair_id
     appears in both training and audit groups;
  5. supplement ledger arithmetic against the stage-A sampling log.

Run with -I so the interpreter cannot pick up planted modules:
    python3 -I verify_independently.py
Exit code is non-zero if any check fails.
"""
from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from pathlib import Path

C_TOUCH = "C1_TOUCH_DISJOINT"
C_OVERLAP = "C2_PARTIAL_OVERLAP"
C_REVERSE = "C3_REVERSE_CONTAINMENT"
C_OTHER = "C0_OTHER"


# --------------------------------------------------------------------------- #
# Coordinate-only reimplementation (no Shapely, no server, no third party)
# --------------------------------------------------------------------------- #
def clipped_box(a, b):
    """Half-open positive-area clip; returns box dict or None."""
    min_x = max(a["min_x"], b["min_x"])
    min_y = max(a["min_y"], b["min_y"])
    max_x = min(a["max_x"], b["max_x"])
    max_y = min(a["max_y"], b["max_y"])
    if max_x > min_x and max_y > min_y:
        return dict(min_x=min_x, min_y=min_y, max_x=max_x, max_y=max_y)
    return None


def intersects_positive_area(a, b):
    return clipped_box(a, b) is not None


def contains(outer, inner):
    """Directional inclusive box containment (mirrors Box.ContainsBox)."""
    return (outer["min_x"] <= inner["min_x"] and
            outer["max_x"] >= inner["max_x"] and
            outer["min_y"] <= inner["min_y"] and
            outer["max_y"] >= inner["max_y"])


def closed_distance_squared(a, b):
    dx = max(a["min_x"] - b["max_x"], b["min_x"] - a["max_x"], 0)
    dy = max(a["min_y"] - b["max_y"], b["min_y"] - a["max_y"], 0)
    return dx * dx + dy * dy


def area(b):
    return (b["max_x"] - b["min_x"]) * (b["max_y"] - b["min_y"])


def relate(left, right):
    clip = clipped_box(left, right)
    return dict(
        intersects=clip is not None,
        left_contains=contains(left, right),
        right_contains=right and contains(right, left),
        distance_squared=closed_distance_squared(left, right),
        intersection=clip,
    )


def unordered_class(rel_ab, rel_ba):
    assert rel_ab["intersects"] == rel_ba["intersects"]
    assert rel_ab["distance_squared"] == rel_ba["distance_squared"]
    if not rel_ab["intersects"] and rel_ab["distance_squared"] == 0:
        return C_TOUCH
    if (rel_ab["intersects"] and not rel_ab["left_contains"]
            and not rel_ab["right_contains"]):
        return C_OVERLAP
    if (rel_ab["intersects"] and not rel_ab["left_contains"]
            and rel_ab["right_contains"]):
        return C_REVERSE
    return C_OTHER


# --------------------------------------------------------------------------- #
# Checks
# --------------------------------------------------------------------------- #
def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    args = ap.parse_args()
    root = Path(args.root)

    blind = json.loads((root / "data" / "blind_input.json").read_text())
    key = json.loads((root / "data" / "answer_key.json").read_text())
    evidence = json.loads(
        (root / "evidence" / "raw_relation_responses.json").read_text())
    ledger = json.loads(
        (root / "evidence" / "supplement_ledger.json").read_text())
    stage_a = json.loads(
        (root / "evidence" / "stage_a_random_draws.json").read_text())
    summary = json.loads(
        (root / "data" / "sampling_summary.json").read_text())

    failures = []
    checks = 0

    def check(cond, message):
        nonlocal checks
        checks += 1
        if not cond:
            failures.append(message)

    # index artifacts by row_id / pair_id ------------------------------------
    key_by_row = {r["row_id"]: r for r in key["rows"]}
    ev_by_row = {r["row_id"]: r for r in evidence["rows"]}
    check(len(key_by_row) == len(key["rows"]), "answer key has duplicate row_id")
    check(len(ev_by_row) == len(evidence["rows"]), "evidence has duplicate row_id")

    pair_groups = {}
    pair_class = {}
    class_counter = Counter()
    group_counter = Counter()

    for brow in blind["rows"]:
        rid = brow["row_id"]
        check(rid in key_by_row, f"blind row {rid} missing from answer key")
        check(rid in ev_by_row, f"blind row {rid} missing from raw evidence")
        if rid not in key_by_row or rid not in ev_by_row:
            continue

        # 1) recompute strictly from coordinates -----------------------------
        recalc = relate(brow["left_shape"], brow["right_shape"])
        krow = key_by_row[rid]
        for field in ("intersects", "left_contains", "right_contains",
                      "distance_squared"):
            check(recalc[field] == krow[field],
                  f"{rid}: recomputed {field}={recalc[field]} != key {krow[field]}")
        check(recalc["intersection"] == krow["intersection"],
              f"{rid}: recomputed intersection {recalc['intersection']} "
              f"!= key {krow['intersection']}")

        # 2) answer key must exactly mirror the saved raw HTTP response ------
        raw = ev_by_row[rid]["raw_response"]
        for field in ("intersects", "left_contains", "right_contains",
                      "distance_squared", "intersection"):
            check(raw.get(field) == krow[field],
                  f"{rid}: raw response {field}={raw.get(field)} != key {krow[field]}")
        check(ev_by_row[rid]["query"]["left"]["id"] ==
              brow["left_feature"]["id"],
              f"{rid}: evidence query left id disagrees with blind input")
        check(ev_by_row[rid]["query"]["right"]["id"] ==
              brow["right_feature"]["id"],
              f"{rid}: evidence query right id disagrees with blind input")

        # shape positivity and integer bounds --------------------------------
        for tag, shp in (("left", brow["left_shape"]),
                         ("right", brow["right_shape"])):
            check(shp["max_x"] > shp["min_x"] and shp["max_y"] > shp["min_y"],
                  f"{rid}: {tag} shape is not a positive-area rectangle")
            check(all(isinstance(v, int) for v in shp.values()),
                  f"{rid}: {tag} shape contains non-integer coordinates")

        # group consistency per ordered row ----------------------------------
        check(brow["group"] == krow["group"],
              f"{rid}: group differs between blind input and answer key")
        pair_groups.setdefault(brow["pair_id"], set()).add(brow["group"])
        pair_class.setdefault(brow["pair_id"], set()).add(krow["target_class"])
        group_counter[brow["group"]] += 1

    # 3) pair-level class must agree with both directional recomputations ----
    blind_pairs = {}
    for brow in blind["rows"]:
        blind_pairs.setdefault(brow["pair_id"], {})[brow["direction"]] = brow
    for pid, dirs in blind_pairs.items():
        check(set(dirs) == {"forward", "reversed"},
              f"{pid}: does not have exactly two directions")
        f, r = dirs["forward"], dirs["reversed"]
        # forward stores (a,b); reversed must store the same two shapes swapped
        check(f["left_shape"] == r["right_shape"]
              and f["right_shape"] == r["left_shape"],
              f"{pid}: reversed row is not the exact L/R swap of forward")
        rel_ab = relate(f["left_shape"], f["right_shape"])
        rel_ba = relate(r["left_shape"], r["right_shape"])
        # the reversed recomputation must equal the swapped forward relation
        check(rel_ba["intersects"] == rel_ab["intersects"]
              and rel_ba["distance_squared"] == rel_ab["distance_squared"]
              and rel_ba["left_contains"] == rel_ab["right_contains"]
              and rel_ba["right_contains"] == rel_ab["left_contains"],
              f"{pid}: swapped-direction relations are not symmetric")
        derived = unordered_class(rel_ab, rel_ba)
        declared = next(iter(pair_class[pid]))
        check(derived == declared,
              f"{pid}: coordinate-derived class {derived} != declared {declared}")
        class_counter[declared] += 1

    # 4) group leakage: both directions together, no pair crossing groups -----
    for pid, gset in pair_groups.items():
        check(gset == {"train"} or gset == {"audit"},
              f"{pid}: directions span groups {sorted(gset)}")
    train_pairs = {p for p, g in pair_groups.items() if gset_contains(g, "train")}
    audit_pairs = {p for p, g in pair_groups.items() if gset_contains(g, "audit")}
    check(not (train_pairs & audit_pairs),
          "a pair_id appears in both training and audit groups")

    # 5) supplement ledger arithmetic ----------------------------------------
    stage_counts = Counter(d["server_class"] for d in stage_a["draws"])
    check(stage_counts == Counter(ledger["stage_a_server_class_counts"]),
          "ledger stage-A counts do not match the raw stage-A log")
    target_n = ledger["target_per_class"]
    for cls in (C_TOUCH, C_OVERLAP, C_REVERSE):
        deficit = max(0, target_n - stage_counts.get(cls, 0))
        check(ledger["deficits"][cls] == deficit,
              f"ledger deficit for {cls} inconsistent with stage-A log")
        accepted = sum(1 for e in ledger["entries"]
                       if e.get("accepted") and e["server_class"] == cls)
        check(accepted == deficit,
              f"{cls}: accepted supplements {accepted} != deficit {deficit}")
        check(ledger["final_class_counts"][cls]
              == min(stage_counts.get(cls, 0), target_n) + accepted,
              f"{cls}: final count not reconciled from random + supplement")
        check(ledger["balance_check"][cls] is True,
              f"{cls}: balance check flag not true")
    # every ledger entry references a real pair when accepted
    known_indexes = {p["pair_index"] for p in evidence["pairs"]}
    for entry in ledger["entries"]:
        if entry.get("accepted"):
            check(entry["pair_index"] in known_indexes,
                  f"ledger pair_index {entry['pair_index']} not in evidence pairs")

    # 6) summary consistency --------------------------------------------------
    check(summary["final_pair_counts"] == dict(class_counter),
          "summary final_pair_counts disagree with independent count")
    check(summary["ordered_rows"] == len(blind["rows"]) == len(key["rows"]),
          "summary ordered row count inconsistent")
    check(dict(summary["group_row_counts"]) == dict(group_counter),
          "summary group row counts inconsistent")

    # 7) blind input must contain no label leakage ---------------------------
    leaked = [k for k in json.dumps(blind, ensure_ascii=False).lower().split('"')
              if k in ("intersects", "left_contains", "right_contains",
                       "answer", "target_class", "答键")]
    check(not leaked, f"blind input appears to leak label fields: {set(leaked)}")

    report = dict(
        checks_run=checks,
        failures=failures,
        independent_pair_class_counts=dict(class_counter),
        independent_group_row_counts=dict(group_counter),
        distinct_pairs=len(pair_groups),
        train_pairs=len(train_pairs), audit_pairs=len(audit_pairs),
        status="PASS" if not failures else "FAIL",
    )
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if not failures else 1


def gset_contains(gset, name):
    return name in gset


if __name__ == "__main__":
    sys.exit(main())
