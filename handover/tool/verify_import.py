#!/usr/bin/env python3
"""Verify the handover image by really importing it into a temporary target
repository (a fresh meridian data directory) and reading the data back.

Steps:
  1. start a temporary target database (empty, revision 0)
  2. negative control A: target layout with UNCLIPPED original shapes
     must be rejected (the boundary rule bites)
  3. negative control B: clipped shapes with conflicts UNRESOLVED
     must be rejected (the exclusivity rule bites)
  4. import the real handover image exactly once (PUT /image)
  5. read everything back and compare against the conversion report:
     layer definitions, every kept feature (id, class, shape),
     pass-through layer, index audit, revision assignment
  6. probe the live layer with an overlapping insert (must be refused)
     and confirm the failed write did not move the revision

Every step is logged to verification/import-log.json; the script exits
non-zero when any check fails.
"""
import argparse
import json
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import client
import mpb


def build_control_images(old_image, report):
    """Control A: target layout, original shapes (unclipped, conflicts kept).
    Control B: target layout, clipped shapes, conflicts unresolved."""
    layer_name = report["target"]["layer"]
    target_def = {
        "name": layer_name,
        "kind": "extent",
        "bounds": report["target"]["bounds"],
        "exclusive": report["target"]["exclusive"],
    }
    unclipped, clipped_unresolved = [], []
    for layer in old_image["layers"]:
        if layer["definition"]["name"] != layer_name:
            continue
        for feature in layer["features"]:
            original = feature["shape"]["extent"]
            unclipped.append(feature)
            candidate = mpb.clip(original, report["target"]["bounds"])
            if candidate is not None:
                clipped_unresolved.append({
                    "id": feature["id"],
                    "class": feature["class"],
                    "shape": {"extent": candidate},
                })

    def with_features(features):
        layers = []
        for layer in old_image["layers"]:
            if layer["definition"]["name"] == layer_name:
                layers.append({"definition": target_def,
                               "revision": layer["revision"],
                               "features": features})
            else:
                layers.append(layer)
        layers.sort(key=lambda entry: entry["definition"]["name"])
        return mpb.encode({"revision": old_image["revision"], "layers": layers})

    return with_features(unclipped), with_features(clipped_unresolved)


def normalized_layers(image):
    """Layer entries with revisions zeroed, for cross-revision comparison."""
    return [{**layer, "revision": 0} for layer in image["layers"]]


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", required=True, help="path to the meridian server binary")
    parser.add_argument("--old-image", required=True, help="source MPB image")
    parser.add_argument("--image", required=True, help="handover MPB image to import")
    parser.add_argument("--report", required=True, help="conversion report JSON")
    parser.add_argument("--log-out", required=True, help="verification log output path")
    args = parser.parse_args()

    old_image = mpb.decode(Path(args.old_image).read_bytes())
    handover_raw = Path(args.image).read_bytes()
    handover_image = mpb.decode(handover_raw)
    report = json.loads(Path(args.report).read_text())
    layer_name = report["target"]["layer"]
    control_a, control_b = build_control_images(old_image, report)

    log = {"target": report["target"], "steps": [], "ok": True}

    def step(name, ok, detail):
        log["steps"].append({"step": name, "ok": bool(ok), "detail": detail})
        log["ok"] = log["ok"] and bool(ok)
        print(("PASS" if ok else "FAIL"), name, "-",
              json.dumps(detail, ensure_ascii=False)[:160])

    with tempfile.TemporaryDirectory(prefix="meridian-target-") as data_dir:
        with client.running_server(args.binary, data_dir) as (session, startup):
            status = session.get("/status")
            step("target repository starts empty",
                 status["revision"] == 0 and status["writable"], status)

            # Restore flattens geometry rejections to 400 invalid; the message
            # still identifies which rule bit (bounds vs exclusivity).
            for label, raw, expect, marker in (
                    ("control A (unclipped shapes) rejected", control_a, 400, "outside"),
                    ("control B (conflicts unresolved) rejected", control_b, 400, "intersects")):
                try:
                    session.restore_image(raw, 0)
                    step(label, False, "import unexpectedly succeeded")
                except client.ApiError as error:
                    message = str(error.body)
                    step(label, error.status == expect and marker in message,
                         {"status": error.status, "body": error.body})
            status = session.get("/status")
            step("rejected imports left the target untouched",
                 status["revision"] == 0, status)

            commit = session.restore_image(handover_raw, 0)
            step("handover image imported once",
                 commit["revision"] == 1 and len(commit["layers"]) == len(handover_image["layers"]),
                 commit)

            status = session.get("/status")
            step("revision assigned by target", status["revision"] == 1, status)

            strata = session.get("/strata")
            summary = {entry["name"]: entry for entry in strata["layers"]}
            target_summary = summary.get(layer_name, {})
            step("target layer definition",
                 target_summary.get("bounds") == report["target"]["bounds"]
                 and target_summary.get("exclusive") == report["target"]["exclusive"]
                 and target_summary.get("count") == report["handover"]["kept_count"],
                 target_summary)

            page = session.window(layer_name, report["target"]["bounds"])
            imported = sorted(page["features"], key=lambda f: f["id"])
            expected = sorted(report["handover"]["features"], key=lambda f: f["id"])
            step("all kept features read back identically (id, class, shape)",
                 imported == expected,
                 {"imported": len(imported), "expected": len(expected),
                  "ids": [f["id"] for f in imported]})

            audit = session.index_audit(layer_name)
            step("index audit matches catalog",
                 audit["records"] == report["handover"]["kept_count"], audit)

            for other in report["pass_through_layers"]:
                old_features = next(layer["features"] for layer in old_image["layers"]
                                    if layer["definition"]["name"] == other)
                page = session.window(other, {"min_x": -1000000000, "min_y": -1000000000,
                                              "max_x": 1000000000, "max_y": 1000000000})
                got = sorted(page["features"], key=lambda f: f["id"])
                want = sorted(old_features, key=lambda f: f["id"])
                step(f"pass-through layer '{other}' unchanged", got == want,
                     {"features": len(got)})

            exported_raw, _ = session.export_image()
            exported = mpb.decode(exported_raw)
            step("re-exported catalog matches handover catalog",
                 normalized_layers(exported) == normalized_layers(handover_image),
                 {"layers": [l["definition"]["name"] for l in exported["layers"]]})

            try:
                session.edit(client.insert_extent(layer_name, "probe", "fixture",
                                                  (4, 4, 10, 10)))
                step("live exclusivity probe refused", False,
                     "overlapping insert unexpectedly succeeded")
            except client.ApiError as error:
                step("live exclusivity probe refused", error.status == 409,
                     {"status": error.status, "body": error.body})
            status = session.get("/status")
            step("failed probe did not move the revision",
                 status["revision"] == 1, status)

    log_out = Path(args.log_out)
    log_out.parent.mkdir(parents=True, exist_ok=True)
    log_out.write_text(json.dumps(log, indent=2, ensure_ascii=False) + "\n")
    print("verification log:", log_out)
    sys.exit(0 if log["ok"] else 1)


if __name__ == "__main__":
    main()
