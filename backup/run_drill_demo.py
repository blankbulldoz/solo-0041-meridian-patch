#!/usr/bin/env python3
"""End-to-end backup rotation and recovery-drill demonstration.

Everything runs under one temporary working directory and never touches a real
database:

  1. build meridian and mpbverify
  2. start a live meridian service with a deterministic spatial seed
  3. keep the online database moving with background atomic edit batches
  4. take several backups while writes continue, then prove rotation prunes
     old images while GFS slots retain a working set
  5. run a healthy drill: the newest backup must be selected and pass
  6. corrupt the newest backup, then re-run the drill: it must detect the
     corruption, fall back to an older usable image, and report the lost
     revision range and the key spatial changes against the live commit
  7. leave the drill report, server logs and a re-runnable parameters file in
     the requested artifacts directory

Usage:
    python3 backup/run_drill_demo.py --artifacts ./backup-demo-artifacts
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

FIELD, BED = "drill-field", "drill-bed"
STATE_NAME = "meridian-state.mpb"

# Fixed seed geometry so the verification plan's expected areas and pairs are
# known in advance. All rectangles live inside [-20, 20]^2.
SEED_BOXES = {
    FIELD: [
        ("alpha", common.box(0, 0, 4, 3)),
        ("bravo", common.box(4, 0, 8, 3)),
        ("charlie", common.box(2, 1, 6, 4)),
    ],
    BED: [
        ("sill", common.box(3, 0, 5, 2)),
    ],
}
WINDOW = common.box(0, 0, 8, 4)
JOIN_WINDOW = common.box(0, 0, 8, 4)


def build_binaries(repo_root: Path, work: Path) -> tuple[Path, Path]:
    env = dict(os.environ)
    env.setdefault("GOPATH", str(Path.home() / "gopath"))
    meridian = work / "meridian"
    verifier = work / "mpbverify"
    subprocess.run(["go", "build", "-o", str(meridian), "./cmd/meridian"],
                   cwd=repo_root, check=True, env=env, timeout=120)
    subprocess.run(["go", "build", "-o", str(verifier), "./cmd/mpbverify"],
                   cwd=repo_root, check=True, env=env, timeout=120)
    return meridian, verifier


def start_meridian(binary: Path, data_dir: Path, log_handle):
    child = subprocess.Popen(
        [str(binary), "-listen", "127.0.0.1:0", "-data", str(data_dir)],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    readable, _, _ = select.select([child.stderr], [], [], 15)
    if not readable:
        child.kill()
        raise RuntimeError("service did not start")
    line = child.stderr.readline()
    log_handle.write(line.decode())
    startup = json.loads(line)
    if "address" not in startup:
        child.kill()
        raise RuntimeError(f"service startup failed: {startup}")
    return child, "http://" + startup["address"]


def stop(child):
    if child.poll() is None:
        child.terminate()
        try:
            child.communicate(timeout=8)
        except subprocess.TimeoutExpired:
            child.kill()
            child.communicate(timeout=5)


def seed(url):
    bounds = common.box(-20, -20, 20, 20)
    for layer in (FIELD, BED):
        common.http_json(url, "POST", "/strata",
                         {"name": layer, "kind": "extent", "bounds": bounds, "exclusive": False})
    edits = []
    for layer, boxes in SEED_BOXES.items():
        for feature_id, rectangle in boxes:
            edits.append({"layer": layer, "id": feature_id, "mode": "insert",
                          "value": {"id": feature_id, "class": "fixture",
                                    "shape": {"extent": rectangle}}})
    status, _, body = common.http_json(url, "POST", "/edits", {"edits": edits})
    assert status == 200, body
    return body["revision"]


def build_plan():
    # Window area on the seed field over [0,8]x[0,4]:
    # union covered = 28, repeated (>=2 covers) = 8, uncovered window = 4.
    # Cross-layer pairs in the same window: alpha/bravo/charlie each meet sill.
    return [
        {"name": "seed-window-area", "type": "coverage", "layer": FIELD,
         "bounds": WINDOW, "expect": {"covered": 28, "repeated": 8, "uncovered": 4}},
        {"name": "seed-cross-layer-pairs", "type": "join", "left": FIELD, "right": BED,
         "bounds": JOIN_WINDOW, "limit": 100, "expect_count": 3,
         "expect_pairs": [["alpha", "sill"], ["bravo", "sill"], ["charlie", "sill"]],
         "expect_intersections": {
             "alpha|sill": common.box(3, 0, 4, 2),
             "bravo|sill": common.box(4, 0, 5, 2),
             "charlie|sill": common.box(3, 1, 5, 2),
         }},
        {"name": "seed-index-audit", "type": "audit", "layer": FIELD,
         "expect": {"records": 3, "checked_nodes": 1}},
        {"name": "seed-locate", "type": "locate", "layer": FIELD,
         "point": {"x": 4, "y": 1}, "expect_ids": ["bravo", "charlie"]},
    ]


def run_drill(script, arguments, log_path: Path):
    proc = subprocess.run([sys.executable, str(script)] + arguments,
                          capture_output=True, text=True, timeout=180)
    log_path.write_text(
        "COMMAND: " + " ".join([sys.executable, str(script)] + arguments) + "\n\n"
        "STDOUT:\n" + proc.stdout + "\nSTDERR:\n" + proc.stderr)
    if proc.returncode not in (0, 1):
        raise RuntimeError(f"drill exited {proc.returncode}: {proc.stderr}")
    return proc.returncode, json.loads(proc.stdout.strip().splitlines()[-1])


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--artifacts", required=True,
                        help="directory kept after the run with reports and logs")
    parser.add_argument("--backups", type=int, default=5)
    parser.add_argument("--workload-batches", type=int, default=14)
    args = parser.parse_args()

    repo_root = Path(__file__).resolve().parents[1]
    artifacts = Path(args.artifacts).resolve()
    if artifacts.exists():
        shutil.rmtree(artifacts)
    artifacts.mkdir(parents=True)
    work = Path(tempfile.mkdtemp(prefix="mpb-demo-", dir=str(artifacts)))
    live_data, archive = work / "live-data", work / "archive"
    reports = artifacts / "reports"
    reports.mkdir()
    server_log = artifacts / "live-server.log"
    op_log = work / "workload.jsonl"

    meridian, verifier = build_binaries(repo_root, work)
    with open(server_log, "w", encoding="utf-8") as log_handle:
        server, url = start_meridian(meridian, live_data, log_handle)
        try:
            seed_revision = seed(url)
            workload = subprocess.Popen(
                [sys.executable, str(repo_root / "backup" / "mpb_workload.py"),
                 "--url", url, "--batches", str(args.workload_batches),
                 "--interval-seconds", "0.2", "--op-log", str(op_log)],
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)

            backup_script = repo_root / "backup" / "mpb_backup.py"
            backup_runs = []
            for index in range(args.backups):
                proc = subprocess.run(
                    [sys.executable, str(backup_script), "--url", url,
                     "--archive", str(archive), "--verifier", str(verifier),
                     "--keep", "2", "--hourly", "2", "--daily", "1",
                     "--weekly", "0", "--monthly", "0"],
                    capture_output=True, text=True, timeout=90)
                if proc.returncode != 0:
                    raise RuntimeError(f"backup {index} failed: {proc.stderr}")
                backup_runs.append(json.loads(proc.stdout.strip().splitlines()[-1]))
                time.sleep(0.6)
            workload.wait(timeout=60)
            if workload.returncode != 0:
                raise RuntimeError(workload.stderr.read().decode())

            catalog = json.loads((archive / "index.json").read_text())
            surviving = catalog["backups"]
            rotation_events = (archive / "rotation.jsonl").read_text().count("\n") if (archive / "rotation.jsonl").exists() else 0
            revisions = sorted(b["committed_revision"] for b in surviving)
            newest_archive = max(
                surviving,
                key=lambda b: (b["created_epoch"], b["committed_revision"], b["archive"]))["archive"]

            plan_path = work / "verify-plan.json"
            plan_path.write_text(json.dumps(build_plan(), indent=2))

            drill_script = repo_root / "backup" / "mpb_restore_drill.py"
            common_args = ["--url", url, "--archive", str(archive),
                           "--binary", str(meridian), "--verifier", str(verifier),
                           "--plan", str(plan_path), "--op-log", str(op_log)]

            # 1. healthy drill: newest backup must win and the gate must open.
            healthy_code, healthy = run_drill(
                drill_script, common_args + ["--report-dir", str(reports / "healthy")],
                reports / "healthy-runner.log")
            assert healthy_code == 0 and healthy["release_gate"]["open"], json.dumps(healthy)
            assert healthy["selection"]["fallback_used"] is False
            assert healthy["recovery_point"]["archive"] == newest_archive

            # 2. corrupt the newest backup and prove fallback selection.
            newest_path = archive / newest_archive
            raw = bytearray(newest_path.read_bytes())
            raw[-1] ^= 0xFF
            newest_path.write_bytes(bytes(raw))
            corrupt_code, fallback = run_drill(
                drill_script, common_args + ["--report-dir", str(reports / "fallback")],
                reports / "fallback-runner.log")
            assert corrupt_code == 0 and fallback["release_gate"]["open"], json.dumps(fallback)
            assert fallback["selection"]["fallback_used"] is True
            first_attempt = fallback["selection"]["newest_first_attempts"][0]
            assert first_attempt["archive"] == newest_archive and first_attempt["ok"] is False
            assert "checksum" in first_attempt["error"]
            chosen = fallback["recovery_point"]
            assert chosen["archive"] != newest_archive
            loss = fallback["loss_analysis"]
            assert loss["lost_revision_range"]["revisions_lost"] > 0
            assert loss["restore_point_revision"] == chosen["committed_revision"]
            assert loss["lost_revision_range"]["from_exclusive"] == chosen["committed_revision"]
            moved = [name for name, detail in loss["layers"].items()
                     if detail["added_ids"] or detail["removed_ids"] or detail["changed_ids"]]
            assert moved, "spatial change analysis found no movement"
        finally:
            stop(server)

    summary = {
        "artifacts": str(artifacts),
        "seed_revision": seed_revision,
        "backup_runs": len(backup_runs),
        "surviving_backups": len(surviving),
        "rotation_events": rotation_events,
        "surviving_revisions": revisions,
        "healthy_drill": {"gate_open": healthy["release_gate"]["open"],
                          "recovery_point": healthy["recovery_point"]},
        "fallback_drill": {
            "gate_open": fallback["release_gate"]["open"],
            "corrupted_archive": newest_archive,
            "recovery_point": fallback["recovery_point"],
            "lost_revision_range": fallback["loss_analysis"]["lost_revision_range"],
            "changed_layers": moved,
            "spatial_checks": [c["name"] for c in fallback["spatial_verification"]
                               if c.get("ok")],
        },
        "reports": {"healthy": str(reports / "healthy" / "drill-report.json"),
                    "fallback": str(reports / "fallback" / "drill-report.json")},
        "logs": {"live_server": str(server_log),
                 "archive_manifest": str(archive / "manifest.jsonl"),
                 "rotation_log": str(archive / "rotation.jsonl"),
                 "workload_op_log": str(op_log)},
        "temp_working_dir": str(work),
    }
    common.write_json_atomic(artifacts / "demo-summary.json", summary)
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
