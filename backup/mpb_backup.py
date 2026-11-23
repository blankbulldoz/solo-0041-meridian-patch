#!/usr/bin/env python3
"""Back up a live Meridian Patch (MPB) database with tiered rotation.

Each run exports one immutable committed MPB v1 image over HTTP while writes
keep moving, validates the bytes, records an append-only archive manifest that
proves exactly which commit was saved, and then applies grandfather-father-son
rotation (hourly / daily / weekly / monthly slots plus a fixed recent count).

Examples
--------
    # one-shot backup against a local service
    python3 backup/mpb_backup.py --url http://127.0.0.1:8741 \
        --archive ./backups

    # periodic mode: take a backup every 60 seconds until signalled
    python3 backup/mpb_backup.py --url http://127.0.0.1:8741 \
        --archive ./backups --daemon --interval-seconds 60

    # simple count-based rotation instead of time slots
    python3 backup/mpb_backup.py --url http://127.0.0.1:8741 \
        --archive ./backups --keep 8
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mpb_common as common  # noqa: E402

MANIFEST_NAME = "manifest.jsonl"
ROTATION_LOG_NAME = "rotation.jsonl"
CATALOG_NAME = "index.json"
SLOT_ORDER = ("hourly", "daily", "weekly", "monthly")


def load_catalog(archive: Path) -> dict:
    path = archive / CATALOG_NAME
    if path.exists():
        return json.loads(path.read_text())
    return {"backups": []}


def save_catalog(archive: Path, catalog: dict):
    common.write_json_atomic(archive / CATALOG_NAME, catalog)


def append_jsonl(path: Path, record: dict):
    path.parent.mkdir(parents=True, exist_ok=True)
    line = json.dumps(record, sort_keys=True) + "\n"
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(line)
        handle.flush()
        os.fsync(handle.fileno())


def find_verifier(archive: Path, explicit: str | None) -> str | None:
    """Locate cmd/mpbverify, building it once into the archive if Go exists."""
    if explicit:
        return explicit if Path(explicit).exists() else None
    cached = archive / "bin" / "mpbverify"
    if cached.exists():
        return str(cached)
    repo_root = Path(__file__).resolve().parents[1]
    source = repo_root / "cmd" / "mpbverify"
    go_binary = shutil.which("go")
    if go_binary is None:
        return None
    cached.parent.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    env.setdefault("GOPATH", str(Path.home() / "gopath"))
    try:
        subprocess.run(
            [go_binary, "build", "-o", str(cached), "./cmd/mpbverify"],
            cwd=repo_root, check=True, capture_output=True, timeout=120, env=env,
        )
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        cached.unlink(missing_ok=True)
        return None
    return str(cached)


def run_verifier(verifier: str | None, image_path: Path) -> dict:
    """Run offline geometry/index validation when mpbverify is available."""
    if not verifier:
        return {"mode": "python-framing", "ok": True,
                "note": "mpbverify unavailable; signature, length, SHA-256 and canonical JSON validated in Python"}
    proc = subprocess.run([verifier, str(image_path)], capture_output=True, timeout=60)
    try:
        parsed = json.loads(proc.stdout.decode().strip().splitlines()[-1])
    except (json.JSONDecodeError, IndexError):
        parsed = {"ok": False, "error": "mpbverify produced no result"}
    parsed["mode"] = "mpbverify"
    parsed["exit_code"] = proc.returncode
    if proc.returncode != 0:
        parsed.setdefault("error", proc.stderr.decode()[:500])
    return parsed


def take_backup(args, archive: Path, verifier: str | None) -> dict:
    started = common.utc_now()
    started_epoch = common.now_epoch()
    raw, committed_revision = common.fetch_image(args.url)
    fetched_epoch = common.now_epoch()

    # Prove the bytes are a well-formed single MPB v1 commit before persisting.
    image, payload_sha256 = common.parse_mpb(raw)
    if image["revision"] != committed_revision:
        raise common.MPBError(
            f"header revision {committed_revision} != payload revision {image['revision']}"
        )
    file_sha256 = common.sha256_hex(raw)

    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime(started_epoch))
    base = f"mpb-{stamp}-r{committed_revision}"
    target = archive / f"{base}.mpb"
    # Several backups can land in the same second (or even capture the same
    # revision); keep every one with a deterministic sequence suffix.
    sequence = 1
    while target.exists():
        target = archive / f"{base}-{sequence:02d}.mpb"
        sequence += 1
    common.atomic_write(target, raw)

    layer_summaries = [
        {
            "name": layer["definition"]["name"],
            "kind": layer["definition"]["kind"],
            "revision": layer["revision"],
            "count": len(layer["features"]),
            "bounds": layer["definition"]["bounds"],
            "exclusive": layer["definition"]["exclusive"],
        }
        for layer in image["layers"]
    ]
    record = {
        "archive": target.name,
        "created_at": started,
        "created_epoch": started_epoch,
        "fetched_epoch": fetched_epoch,
        "source_url": args.url,
        "committed_revision": committed_revision,
        "bytes": len(raw),
        "sha256": file_sha256,
        "payload_sha256": payload_sha256,
        "signature": common.SIGNATURE.hex(),
        "layer_count": len(image["layers"]),
        "feature_count": sum(len(layer["features"]) for layer in image["layers"]),
        "layers": layer_summaries,
        "tier": None,
        "offline_verification": None,
    }

    verification = run_verifier(verifier, target)
    record["offline_verification"] = verification
    if not verification.get("ok", False):
        # Never register an image that fails full validation as a restore point.
        record["state"] = "quarantined"
        quarantine = archive / "quarantine"
        quarantine.mkdir(exist_ok=True)
        bad = target.rename(quarantine / target.name)
        record["archive"] = f"quarantine/{bad.name}"
        append_jsonl(archive / MANIFEST_NAME, record)
        return record

    record["state"] = "valid"
    append_jsonl(archive / MANIFEST_NAME, record)

    catalog = load_catalog(archive)
    catalog["backups"] = [item for item in catalog["backups"] if item["archive"] != record["archive"]]
    catalog["backups"].append({key: record[key] for key in (
        "archive", "created_epoch", "committed_revision", "sha256", "bytes",
        "layer_count", "feature_count", "state", "tier")})
    save_catalog(archive, catalog)
    return record


def slot_for(epoch_seconds: int, tier: str) -> str:
    if tier == "hourly":
        return time.strftime("%Y%m%d%H", time.gmtime(epoch_seconds))
    if tier == "daily":
        return time.strftime("%Y%m%d", time.gmtime(epoch_seconds))
    if tier == "weekly":
        return time.strftime("%G%V", time.gmtime(epoch_seconds))
    if tier == "monthly":
        return time.strftime("%Y%m", time.gmtime(epoch_seconds))
    raise ValueError(tier)


def rotate(archive: Path, keep: int, slots: dict) -> list:
    """Apply count + GFS rotation. Returns deletion records."""
    catalog = load_catalog(archive)
    valid = [b for b in catalog["backups"] if b.get("state") == "valid"]
    valid.sort(
        key=lambda item: (item["created_epoch"], item["committed_revision"], item["archive"]),
        reverse=True)

    keepers = set()
    # 1. always retain the newest N valid images
    for item in valid[:keep]:
        keepers.add(item["archive"])

    # 2. one backup per GFS time slot; slots are evaluated newest-first
    used_slots = {tier: set() for tier in SLOT_ORDER}
    for item in valid:
        for tier in SLOT_ORDER:
            if slots[tier] <= 0:
                continue
            slot = slot_for(item["created_epoch"], tier)
            if slot not in used_slots[tier] and len(used_slots[tier]) < slots[tier]:
                used_slots[tier].add(slot)
                keepers.add(item["archive"])

    deletions = []
    for item in valid:
        if item["archive"] in keepers:
            continue
        image_path = archive / item["archive"]
        size = image_path.stat().st_size if image_path.exists() else 0
        image_path.unlink(missing_ok=True)
        deletions.append({
            "at": common.utc_now(),
            "archive": item["archive"],
            "committed_revision": item["committed_revision"],
            "sha256": item["sha256"],
            "bytes": size,
            "reason": "rotation",
        })

    # Annotate surviving backups with the tiers they occupy.
    tier_by_archive = {}
    for item in valid:
        if item["archive"] not in keepers:
            continue
        occupied = [tier for tier in SLOT_ORDER
                    if slots[tier] > 0 and slot_for(item["created_epoch"], tier) in used_slots[tier]]
        if item["archive"] in {x["archive"] for x in valid[:keep]}:
            occupied.insert(0, "recent")
        tier_by_archive[item["archive"]] = occupied
    for item in catalog["backups"]:
        item["tier"] = tier_by_archive.get(item["archive"])
        if item["archive"] not in keepers and item.get("state") == "valid":
            item["state"] = "rotated-out"
    catalog["backups"] = [b for b in catalog["backups"] if b.get("state") != "rotated-out"]
    save_catalog(archive, catalog)
    for deletion in deletions:
        append_jsonl(archive / ROTATION_LOG_NAME, deletion)
    return deletions


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=os.environ.get("MERIDIAN_URL", "http://127.0.0.1:8741"),
                        help="base URL of the Meridian HTTP service")
    parser.add_argument("--archive", required=True, help="backup archive directory")
    parser.add_argument("--keep", type=int, default=3, help="always keep the N newest valid images")
    parser.add_argument("--hourly", type=int, default=6)
    parser.add_argument("--daily", type=int, default=7)
    parser.add_argument("--weekly", type=int, default=4)
    parser.add_argument("--monthly", type=int, default=3)
    parser.add_argument("--daemon", action="store_true", help="run forever on an interval")
    parser.add_argument("--interval-seconds", type=int, default=3600)
    parser.add_argument("--max-runs", type=int, default=0,
                        help="daemon: stop after N backups (0 = unlimited); useful in drills")
    parser.add_argument("--verifier", default=None, help="path to a prebuilt mpbverify binary")
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    archive = Path(args.archive).resolve()
    archive.mkdir(parents=True, exist_ok=True)
    verifier = find_verifier(archive, args.verifier)
    slots = {"hourly": args.hourly, "daily": args.daily,
             "weekly": args.weekly, "monthly": args.monthly}

    runs = 0
    while True:
        try:
            record = take_backup(args, archive, verifier)
            deletions = []
            if record["state"] == "valid":
                deletions = rotate(archive, args.keep, slots)
            print(json.dumps({"event": "backup", "record": record,
                              "rotated": len(deletions)}, sort_keys=True))
        except Exception as error:  # one failed run must not kill a daemon
            print(json.dumps({"event": "backup_failed", "error": str(error)}, sort_keys=True),
                  file=sys.stderr)
            if not args.daemon:
                return 1
        runs += 1
        if not args.daemon or (args.max_runs and runs >= args.max_runs):
            return 0
        time.sleep(args.interval_seconds)


if __name__ == "__main__":
    raise SystemExit(main())
