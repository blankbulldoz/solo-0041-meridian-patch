#!/usr/bin/env python3
"""Run an MPB backup recovery drill entirely inside a temporary directory.

Selection order: the newest backup in the archive is always tried first. Each
candidate is validated offline (SHA-256 against archive metadata plus full
MPB v1 structure/geometry/index validation). A corrupt newest backup is
recorded and the drill falls back to the next older usable image.

The chosen image is booted offline as its own meridian process (no data is
written back to the live service), then key spatial state is verified through
the real HTTP API: window areas (coverage) and cross-layer pairs (join). A
release gate is opened only when every check passes. The drill finally diffs
the chosen image against the current live committed image and states the lost
revision range and key spatial changes, using backup metadata and the write
operation log when one is available.

All logs and a fully re-runnable parameters file are kept under --report-dir.
"""
from __future__ import annotations

import argparse
import json
import os
import select
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mpb_common as common  # noqa: E402

STATE_NAME = "meridian-state.mpb"


# --------------------------------------------------------------------------
# Archive helpers
# --------------------------------------------------------------------------

def load_manifest(archive: Path):
    records = []
    path = archive / "manifest.jsonl"
    if not path.exists():
        raise SystemExit(f"no manifest found in {archive}")
    for line in path.read_text().splitlines():
        if line.strip():
            records.append(json.loads(line))
    return records


def candidates(records):
    """Newest-first list of backup records that are not quarantined."""
    usable = [r for r in records if r.get("state") == "valid"]
    usable.sort(key=lambda r: (r["created_epoch"], r["committed_revision"], r["archive"]),
                reverse=True)
    return usable


def offline_check(record, archive: Path, verifier: str | None):
    """Re-validate one backup without any running server."""
    image_path = archive / record["archive"]
    result = {"archive": record["archive"], "committed_revision": record["committed_revision"]}
    if not image_path.exists():
        result.update(ok=False, stage="file", error="backup file is missing")
        return result
    raw = image_path.read_bytes()
    actual = common.sha256_hex(raw)
    result["bytes"] = len(raw)
    result["sha256"] = actual
    if actual != record["sha256"]:
        result.update(ok=False, stage="sha256",
                      error=f"checksum mismatch: metadata {record['sha256']} != file {actual}")
        return result
    try:
        image, _ = common.parse_mpb(raw)
        if image["revision"] != record["committed_revision"]:
            raise common.MPBError("payload revision does not match manifest")
    except common.MPBError as error:
        result.update(ok=False, stage="mpb-framing", error=str(error))
        return result
    if verifier:
        proc = subprocess.run([verifier, str(image_path)], capture_output=True, timeout=60)
        try:
            probe = json.loads(proc.stdout.decode().strip().splitlines()[-1])
        except (json.JSONDecodeError, IndexError):
            probe = {"ok": False, "error": "mpbverify produced no result"}
        result["mpbverify"] = {"exit_code": proc.returncode, "probe": probe.get("probe"),
                               "error": probe.get("error")}
        if proc.returncode != 0 or not probe.get("ok"):
            result.update(ok=False, stage="mpbverify",
                          error=probe.get("error", "offline geometry/index validation failed"))
            return result
    else:
        result["mpbverify"] = {"skipped": "verifier binary not available"}
    result["ok"] = True
    result["image"] = image
    result["raw"] = raw
    return result


# --------------------------------------------------------------------------
# Offline boot of the recovered data directory
# --------------------------------------------------------------------------

def start_server(binary, data_dir, log_handle):
    child = subprocess.Popen(
        [str(binary), "-listen", "127.0.0.1:0", "-data", str(data_dir)],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    readable, _, _ = select.select([child.stderr], [], [], 15)
    if not readable:
        child.kill()
        raise RuntimeError("recovered service did not start within 15 seconds")
    line = child.stderr.readline()
    log_handle.write(line.decode())
    log_handle.flush()
    startup = json.loads(line)
    if "address" not in startup:
        child.kill()
        raise RuntimeError(f"recovered service refused to start: {startup}")
    return child, "http://" + startup["address"], startup["revision"]


def stop_server(child):
    if child.poll() is None:
        child.terminate()
        try:
            child.communicate(timeout=8)
        except subprocess.TimeoutExpired:
            child.kill()
            child.communicate(timeout=5)


# --------------------------------------------------------------------------
# Spatial verification on the recovered state
# --------------------------------------------------------------------------

def run_checks(base_url, plan, log_handle):
    outcomes = []
    all_ok = True
    for check in plan:
        kind = check["type"]
        outcome = {"name": check.get("name", kind), "type": kind}
        if kind == "coverage":
            status, _, body = common.http_json(
                base_url, "POST", f"/strata/{check['layer']}/coverage",
                {"bounds": check["bounds"]})
            outcome["status"] = status
            if status != 200:
                outcome["ok"] = False
                outcome["error"] = body
            else:
                expected = check["expect"]
                observed = {key: body[key] for key in expected}
                outcome["observed"] = observed
                outcome["expected"] = expected
                outcome["ok"] = observed == expected
        elif kind == "join":
            status, _, body = common.http_json(base_url, "POST", "/join", {
                "left": check["left"], "right": check["right"],
                "bounds": check["bounds"], "limit": check.get("limit", 2000)})
            outcome["status"] = status
            if status != 200:
                outcome["ok"] = False
                outcome["error"] = body
            else:
                pairs = body["pairs"]
                if "expect_count" in check:
                    outcome["observed"] = {"pair_count": len(pairs)}
                    outcome["expected"] = {"pair_count": check["expect_count"]}
                    outcome["ok"] = len(pairs) == check["expect_count"]
                if "expect_pairs" in check:
                    got = sorted((p["left"], p["right"]) for p in pairs)
                    wanted = sorted(tuple(p) for p in check["expect_pairs"])
                    outcome["observed_pairs"] = got
                    outcome["expected_pairs"] = wanted
                    outcome["ok"] = outcome.get("ok", True) and got == wanted
                if "expect_intersections" in check:
                    got = {f"{p['left']}|{p['right']}": p["intersection"] for p in pairs}
                    outcome["intersections_ok"] = all(
                        got.get(pair) == box for pair, box in check["expect_intersections"].items())
                    outcome["observed_intersections"] = got
                    outcome["ok"] = outcome.get("ok", True) and outcome["intersections_ok"]
        elif kind == "locate":
            status, _, body = common.http_json(
                base_url, "POST", f"/strata/{check['layer']}/locate", check["point"])
            outcome["status"] = status
            if status != 200:
                outcome["ok"] = False
                outcome["error"] = body
            else:
                got = sorted(f["id"] for f in body["features"])
                outcome["observed"] = {"ids": got}
                outcome["expected"] = {"ids": sorted(check["expect_ids"])}
                outcome["ok"] = got == sorted(check["expect_ids"])
        elif kind == "audit":
            status, _, body = common.http_json(
                base_url, "GET", f"/strata/{check['layer']}/index-audit")
            outcome["status"] = status
            if status != 200:
                outcome["ok"] = False
                outcome["error"] = body
            else:
                observed = {"records": body["records"], "checked_nodes": body["checked_nodes"]}
                outcome["observed"] = observed
                outcome["expected"] = check["expect"]
                outcome["ok"] = observed == check["expect"]
        else:
            outcome.update(ok=False, error=f"unknown check type {kind}")
        outcomes.append(outcome)
        log_handle.write(json.dumps({"event": "check", **outcome}, sort_keys=True) + "\n")
        all_ok = all_ok and outcome.get("ok", False)
    return all_ok, outcomes


# --------------------------------------------------------------------------
# Loss analysis: chosen restore point vs current live commit
# --------------------------------------------------------------------------

def load_op_log(path: Path | None):
    if not path or not path.exists():
        return []
    events = []
    for line in path.read_text().splitlines():
        if line.strip():
            events.append(json.loads(line))
    return events


def diff_images(candidate_image, live_image, op_events, candidate_revision, live_revision):
    cand_map, live_map = common.feature_map(candidate_image), common.feature_map(live_image)
    layers = sorted(set(cand_map) | set(live_map))
    per_layer = {}
    for name in layers:
        before, after = cand_map.get(name, {}), live_map.get(name, {})
        added = sorted(set(after) - set(before))
        removed = sorted(set(before) - set(after))
        changed = sorted(fid for fid in set(before) & set(after)
                         if before[fid]["shape"] != after[fid]["shape"]
                         or before[fid].get("class") != after[fid].get("class"))
        per_layer[name] = {
            "features_at_restore_point": len(before),
            "features_now": len(after),
            "added_ids": added,
            "removed_ids": removed,
            "changed_ids": changed,
            "net_feature_delta": len(after) - len(before),
        }
    bounds_before, bounds_after = common.layer_bounds(candidate_image), common.layer_bounds(live_image)
    layout_changes = {
        name: {"restore_point": bounds_before.get(name), "now": bounds_after.get(name)}
        for name in layers if bounds_before.get(name) != bounds_after.get(name)
    }
    lost_ops = [event for event in op_events
                if candidate_revision < event["revision"] <= live_revision]
    return {
        "restore_point_revision": candidate_revision,
        "live_revision": live_revision,
        "lost_revision_range": {
            "from_exclusive": candidate_revision,
            "to_inclusive": live_revision,
            "revisions_lost": live_revision - candidate_revision,
        },
        "layers": per_layer,
        "layout_changes": layout_changes,
        "committed_operations_in_lost_range": lost_ops,
        "op_log_complete": bool(op_events) and lost_ops[-1]["revision"] == live_revision
        if lost_ops else (candidate_revision == live_revision),
    }


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default="http://127.0.0.1:8741",
                        help="live service URL, used read-only for the current image")
    parser.add_argument("--archive", required=True)
    parser.add_argument("--binary", required=True, help="path to the meridian server binary")
    parser.add_argument("--verifier", default=None, help="path to mpbverify")
    parser.add_argument("--plan", required=True, help="JSON file with spatial verification checks")
    parser.add_argument("--op-log", default=None, help="JSONL log produced by mpb_workload.py")
    parser.add_argument("--report-dir", required=True,
                        help="directory receiving the drill report and targeted logs")
    parser.add_argument("--no-live", action="store_true",
                        help="skip the live image comparison (offline-only drill)")
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    archive = Path(args.archive).resolve()
    report_dir = Path(args.report_dir).resolve()
    report_dir.mkdir(parents=True, exist_ok=True)
    plan = json.loads(Path(args.plan).read_text())
    verifier = args.verifier
    if verifier is None:
        candidate_binary = archive / "bin" / "mpbverify"
        if candidate_binary.exists():
            verifier = str(candidate_binary)

    parameters = {
        "invoked_at": common.utc_now(),
        "url": args.url, "archive": str(archive), "binary": str(args.binary),
        "verifier": verifier, "plan_file": str(Path(args.plan).resolve()),
        "op_log": str(Path(args.op_log).resolve()) if args.op_log else None,
        "report_dir": str(report_dir), "no_live": args.no_live,
        "argv": sys.argv,
    }
    common.write_json_atomic(report_dir / "parameters.json", parameters)

    records = load_manifest(archive)
    attempts, chosen, chosen_image, chosen_raw = [], None, None, None
    for record in candidates(records):
        check = offline_check(record, archive, verifier)
        attempts.append({k: v for k, v in check.items() if k not in ("image", "raw")})
        if check["ok"]:
            chosen, chosen_image, chosen_raw = record, check["image"], check["raw"]
            break
    if chosen is None:
        report = {"status": "failed", "reason": "no usable backup in archive",
                  "attempts": attempts, "parameters": parameters}
        common.write_json_atomic(report_dir / "drill-report.json", report)
        print(json.dumps(report, sort_keys=True))
        return 1

    log_path = report_dir / "recovered-server.log"
    with tempfile.TemporaryDirectory(prefix="mpb-restore-") as temporary, \
            open(log_path, "w", encoding="utf-8") as log_handle:
        data_dir = Path(temporary) / "space"
        data_dir.mkdir()
        # The server's restart path is the real recovery path: validate and
        # rebuild indexes from meridian-state.mpb.
        common.atomic_write(data_dir / STATE_NAME, chosen_raw)
        child, base_url, booted_revision = start_server(args.binary, data_dir, log_handle)
        try:
            status_ok, _, status_body = common.http_json(base_url, "GET", "/status")
            checks_ok, outcomes = run_checks(base_url, plan, log_handle)
            boot_ok = booted_revision == chosen["committed_revision"] and status_ok == 200
            live = None
            if not args.no_live:
                live_raw, live_revision = common.fetch_image(args.url)
                live_image, _ = common.parse_mpb(live_raw)
                live = diff_images(
                    chosen_image, live_image, load_op_log(Path(args.op_log) if args.op_log else None),
                    chosen["committed_revision"], live_revision)
        finally:
            stop_server(child)

    gate_checks = {
        "offline_image_valid": True,
        "offline_boot_revision_matches_manifest": boot_ok,
        "spatial_checks_passed": checks_ok,
        "live_comparison_completed": args.no_live or live is not None,
    }
    gate_open = all(gate_checks.values())
    report = {
        "status": "passed" if gate_open else "failed",
        "ran_at": common.utc_now(),
        "selection": {"newest_first_attempts": attempts,
                      "fallback_used": attempts[0]["archive"] != chosen["archive"]},
        "recovery_point": {
            "archive": chosen["archive"],
            "committed_revision": chosen["committed_revision"],
            "sha256": chosen["sha256"],
            "created_at": chosen["created_at"],
            "layer_count": chosen["layer_count"],
            "feature_count": chosen["feature_count"],
        },
        "release_gate": {"open": gate_open, "checks": gate_checks},
        "spatial_verification": outcomes,
        "loss_analysis": live,
        "logs": {"server_log": str(log_path),
                 "parameters": str(report_dir / "parameters.json")},
        "parameters": parameters,
    }
    common.write_json_atomic(report_dir / "drill-report.json", report)
    print(json.dumps(report, sort_keys=True))
    return 0 if gate_open else 1


if __name__ == "__main__":
    raise SystemExit(main())
