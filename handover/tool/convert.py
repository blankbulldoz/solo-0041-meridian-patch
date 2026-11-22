#!/usr/bin/env python3
"""Convert rectangles from an old MPB image into a handover image that
satisfies the target team's narrower, exclusive layer boundary.

The converter never renames features (original IDs are kept) and never
ships conflicting data. Every source rectangle is classified:

  fully-legal   candidate == original, no conflicts          -> handed over
  clippable     candidate = clip(original) != original,
                no conflicts after clipping                  -> handed over (clipped)
  conflicting   still conflicting after clipping:
                - unplaceable: clip is empty (outside the boundary)
                - mutually exclusive group members           -> resolved below

Mutually exclusive groups (positive-area overlap between candidates) are
resolved deterministically: within a group, consider members by descending
candidate area, ties broken by ascending id; keep a member when it does not
overlap any already-kept member, otherwise discard it. The basis of every
decision and every discarded rectangle is recorded in the report.

Outputs: the handover image (MPB v1) and a machine-readable report.
"""
import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mpb

BUCKET_FULLY_LEGAL = "fully-legal"
BUCKET_CLIPPABLE = "clippable"
BUCKET_CONFLICTING = "conflicting"

SELECTION_RULE = (
    "within each mutually exclusive group, consider members by descending "
    "candidate area with ties broken by ascending id; keep a member when it "
    "has no positive-area overlap with any already-kept member, otherwise "
    "discard it"
)


def parse_bounds(text):
    parts = [int(p) for p in text.split(",")]
    if len(parts) != 4:
        raise argparse.ArgumentTypeError("bounds must be min_x,min_y,max_x,max_y")
    bounds = mpb.box(*parts)
    if bounds["max_x"] <= bounds["min_x"] or bounds["max_y"] <= bounds["min_y"]:
        raise argparse.ArgumentTypeError("bounds must have positive width and height")
    return bounds


def extent_of(feature, where):
    shape = feature.get("shape") or {}
    extent = shape.get("extent")
    if extent is None or shape.get("mark") is not None:
        raise SystemExit(f"{where}: feature {feature.get('id')!r} is not an extent")
    return extent


def find_layer(image, name):
    for layer in image["layers"]:
        if layer["definition"]["name"] == name:
            return layer
    raise SystemExit(f"layer {name!r} not found in image")


def overlap_groups(ids, candidates):
    """Connected components of the positive-area overlap graph."""
    neighbors = {fid: set() for fid in ids}
    ordered = sorted(ids)
    for i, left in enumerate(ordered):
        for right in ordered[i + 1:]:
            if mpb.overlaps(candidates[left], candidates[right]):
                neighbors[left].add(right)
                neighbors[right].add(left)
    groups = []
    seen = set()
    for fid in ordered:
        if fid in seen or not neighbors[fid]:
            continue
        stack, group = [fid], []
        seen.add(fid)
        while stack:
            current = stack.pop()
            group.append(current)
            for other in sorted(neighbors[current]):
                if other not in seen:
                    seen.add(other)
                    stack.append(other)
        groups.append(sorted(group))
    return groups, neighbors


def resolve_group(group, candidates):
    """Greedy keep/discard with a recorded basis for every decision."""
    order = sorted(group, key=lambda fid: (-mpb.area(candidates[fid]), fid))
    kept, decisions = [], []
    for fid in order:
        area = mpb.area(candidates[fid])
        blockers = [other for other in kept
                    if mpb.overlaps(candidates[fid], candidates[other])]
        if not blockers:
            if kept:
                basis = (f"candidate area {area}; no positive-area overlap with "
                         f"already-kept {kept}")
            else:
                tied = [o for o in order
                        if o != fid and mpb.area(candidates[o]) == area]
                basis = f"largest candidate area {area} in group"
                if tied:
                    basis += f"; tied with {sorted(tied)}, tie broken by ascending id"
            kept.append(fid)
            decisions.append({"id": fid, "outcome": "kept", "basis": basis})
        else:
            notes = []
            for other in blockers:
                other_area = mpb.area(candidates[other])
                overlap = mpb.clip(candidates[fid], candidates[other])
                relation = "equal area, lost the ascending-id tie-break" \
                    if other_area == area else f"smaller area {area} < {other_area}"
                notes.append(f"overlaps kept '{other}' (overlap area "
                             f"{mpb.area(overlap)}); {relation}")
            decisions.append({"id": fid, "outcome": "discarded",
                              "basis": "; ".join(notes)})
    discarded = [fid for fid in group if fid not in kept]
    return {"members": group, "kept": kept, "discarded": discarded,
            "decisions": decisions}


def convert(image, layer_name, target_bounds, exclusive):
    layer = find_layer(image, layer_name)
    definition = layer["definition"]
    if definition["kind"] != "extent":
        raise SystemExit(f"layer {layer_name!r} is kind {definition['kind']!r}, "
                         "this converter handles extent layers")

    records = {}
    candidates = {}
    for feature in layer["features"]:
        fid = feature["id"]
        original = extent_of(feature, layer_name)
        candidate = mpb.clip(original, target_bounds)
        clipped = candidate is not None and candidate != original
        records[fid] = {
            "id": fid,
            "class": feature["class"],
            "original_shape": {"extent": original},
            "candidate_shape": {"extent": candidate} if candidate else None,
            "clipped_by_boundary": clipped,
            "conflict_companions": [],
        }
        if candidate is not None:
            candidates[fid] = candidate

    groups, neighbors = overlap_groups(list(candidates), candidates)
    resolutions = [resolve_group(group, candidates) for group in groups]
    outcome = {}
    for resolution in resolutions:
        for decision in resolution["decisions"]:
            outcome[decision["id"]] = (resolution, decision)

    for fid, record in records.items():
        record["conflict_companions"] = sorted(neighbors.get(fid, ()))

    fully_legal, clippable, conflicting = [], [], []
    for fid in sorted(records):
        record = records[fid]
        if candidates.get(fid) is None:
            record["substatus"] = "unplaceable"
            record["reason"] = ("no positive-area intersection with the target "
                                "bounds; clipping leaves nothing to hand over")
            conflicting.append(record)
        elif not neighbors.get(fid):
            if record["clipped_by_boundary"]:
                clippable.append(record)
            else:
                fully_legal.append(record)
        else:
            resolution, decision = outcome[fid]
            record["substatus"] = f"conflict-{decision['outcome']}"
            record["selection"] = {
                "group": resolution["members"],
                "outcome": decision["outcome"],
                "basis": decision["basis"],
            }
            if decision["outcome"] == "discarded":
                record["reason"] = ("discarded during mutual-exclusion "
                                    "resolution; see selection.basis")
            conflicting.append(record)

    kept_ids = ([r["id"] for r in fully_legal]
                + [r["id"] for r in clippable]
                + sorted(fid for fid in candidates
                         if outcome.get(fid) and outcome[fid][1]["outcome"] == "kept"))
    kept_ids.sort()
    dropped_ids = sorted(set(records) - set(kept_ids))

    handover_features = []
    for fid in kept_ids:
        record = records[fid]
        handover_features.append({
            "id": fid,
            "class": record["class"],
            "shape": record["candidate_shape"],
        })

    handover_layers = []
    for source_layer in image["layers"]:
        if source_layer["definition"]["name"] == layer_name:
            handover_layers.append({
                "definition": {
                    "name": layer_name,
                    "kind": "extent",
                    "bounds": target_bounds,
                    "exclusive": exclusive,
                },
                "revision": source_layer["revision"],
                "features": handover_features,
            })
        else:
            handover_layers.append(source_layer)  # pass through unchanged
    handover_layers.sort(key=lambda entry: entry["definition"]["name"])
    handover_image = {"revision": image["revision"], "layers": handover_layers}

    report = {
        "source": {
            "image_revision": image["revision"],
            "layer": layer_name,
            "layer_bounds": definition["bounds"],
            "layer_exclusive": definition["exclusive"],
            "feature_count": len(records),
        },
        "target": {
            "layer": layer_name,
            "bounds": target_bounds,
            "exclusive": exclusive,
        },
        "selection_rule": SELECTION_RULE,
        "buckets": {
            BUCKET_FULLY_LEGAL: fully_legal,
            BUCKET_CLIPPABLE: clippable,
            BUCKET_CONFLICTING: conflicting,
        },
        "conflict_resolution": resolutions,
        "handover": {
            "kept_ids": kept_ids,
            "dropped_ids": dropped_ids,
            "kept_count": len(kept_ids),
            "dropped_count": len(dropped_ids),
            "features": handover_features,
        },
        "pass_through_layers": sorted(
            entry["definition"]["name"] for entry in image["layers"]
            if entry["definition"]["name"] != layer_name),
    }
    return handover_image, report


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--input", required=True, help="old MPB image")
    parser.add_argument("--layer", default="legacy-parcels", help="extent layer to convert")
    parser.add_argument("--bounds", type=parse_bounds, default=mpb.box(-8, -8, 40, 24),
                        help="target bounds as min_x,min_y,max_x,max_y "
                             "(default: -8,-8,40,24)")
    parser.add_argument("--exclusive", action=argparse.BooleanOptionalAction, default=True,
                        help="target layer exclusivity (default: --exclusive)")
    parser.add_argument("--image-out", required=True, help="output handover MPB image")
    parser.add_argument("--report-out", required=True, help="output JSON report")
    args = parser.parse_args()

    raw = Path(args.input).read_bytes()
    image = mpb.decode(raw)
    handover_image, report = convert(image, args.layer, args.bounds, args.exclusive)
    encoded = mpb.encode(handover_image)

    report["source"]["image_sha256"] = mpb.sha256_hex(raw)
    report["handover"]["image_sha256"] = mpb.sha256_hex(encoded)
    report["handover"]["image_bytes"] = len(encoded)

    image_out = Path(args.image_out)
    image_out.parent.mkdir(parents=True, exist_ok=True)
    image_out.write_bytes(encoded)
    report_out = Path(args.report_out)
    report_out.parent.mkdir(parents=True, exist_ok=True)
    report_out.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")

    counts = {name: len(bucket) for name, bucket in report["buckets"].items()}
    print(json.dumps({
        "buckets": counts,
        "kept": report["handover"]["kept_count"],
        "dropped": report["handover"]["dropped_count"],
        "image": str(image_out),
        "image_sha256": report["handover"]["image_sha256"],
        "report": str(report_out),
    }, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
