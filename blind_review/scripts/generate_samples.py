#!/usr/bin/env python3
"""Build paired polygon blind-review samples through the public Meridian HTTP API.

Pipeline
--------
1. Start (or reuse) a Meridian server, create a non-exclusive extent layer.
2. Stage A: generate random rectangle pairs in a fixed integer plane and label
   every pair by the SERVER's public ``POST /relation`` response (not by us).
3. Stage B: when a class is under quota after blind random sampling, construct
   deterministic supplementary pairs and label them through the same query,
   writing an auditable supplement ledger (补样依据).
4. Persist the raw server response for every ordered query as shape evidence.
5. Split the UNORDERED pair (both directions together) into training / audit
   groups so the two directions of one shape pair can never cross groups.
6. Emit three separated artifacts: blind input, answer key, raw evidence.

All geometry are axis-aligned integer rectangles (extent), matching the
database's geometry contract. Relation semantics follow PROJECT_SPEC.md:
half-open rectangles, positive-area intersection, closed-envelope distance,
directional containment.

Only the Python standard library is used.
"""
from __future__ import annotations

import argparse
import json
import random
import select
import subprocess
from collections import Counter
from contextlib import contextmanager
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

PREFIX = "/meridian/v1"
LAYER = "blind-pairs"
FEATURE_CLASS = "sample-pair"
BOUNDS = dict(min_x=-1000, min_y=-1000, max_x=1000, max_y=1000)
# Blind random draws use a smaller sampling window inside the layer so that
# contacts/overlaps/containments occur naturally but remain imbalanced; the
# deterministic stage-B builders can still use the full inset layer plane.
SAMPLE_WINDOW = dict(min_x=-180, min_y=-180, max_x=180, max_y=180)
SEED = 20261122
RANDOM_DRAWS = 300          # stage A blind random pairs
TARGET_PER_CLASS = 30       # final unordered pairs per target class
AUDIT_PER_CLASS = 6         # of which audit (抽查); remainder training
MAX_SIDE = 140
MIN_SIDE = 6

# Class codes (unordered-pair level).
C_TOUCH = "C1_TOUCH_DISJOINT"          # 零距离不相交
C_OVERLAP = "C2_PARTIAL_OVERLAP"       # 正面积相交但互不包含
C_REVERSE = "C3_REVERSE_CONTAINMENT"   # 反转左右才变成包含
C_OTHER = "C0_OTHER"                   # 分离(正距离)/全等/完全同向包含等, 不入选


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


# --------------------------------------------------------------------------- #
# HTTP client
# --------------------------------------------------------------------------- #
class Client:
    def __init__(self, base_url):
        self.base = base_url.rstrip("/")

    def call(self, method, path, payload=None, expect=200):
        data = None
        headers = {}
        if payload is not None:
            data = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        req = Request(self.base + PREFIX + path, data=data, method=method,
                      headers=headers)
        try:
            resp = urlopen(req, timeout=10)
        except HTTPError as err:
            resp = err
        with resp:
            raw = resp.read()
            if resp.status != expect:
                raise RuntimeError(
                    f"{method} {path} -> {resp.status}, want {expect}: {raw[:300]!r}")
            return json.loads(raw)


@contextmanager
def server(binary, data_dir):
    proc = subprocess.Popen(
        [str(binary), "-listen", "127.0.0.1:0", "-data", str(data_dir)],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        readable, _, _ = select.select([proc.stderr], [], [], 10)
        if not readable:
            raise RuntimeError("server failed to start within 10s")
        startup = json.loads(proc.stderr.readline())
        if "address" not in startup:
            raise RuntimeError(f"bad startup line: {startup}")
        client = Client("http://" + startup["address"])
        assert client.call("GET", "/status")["revision"] == startup["revision"]
        yield client
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.communicate(timeout=5)


# --------------------------------------------------------------------------- #
# Local geometry helpers (used ONLY to build candidates; labels come from API)
# --------------------------------------------------------------------------- #
def random_box(rng, window=SAMPLE_WINDOW):
    w = rng.randint(MIN_SIDE, MAX_SIDE)
    h = rng.randint(MIN_SIDE, MAX_SIDE)
    x0 = rng.randint(window["min_x"], window["max_x"] - w)
    y0 = rng.randint(window["min_y"], window["max_y"] - h)
    return box(x0, y0, x0 + w, y0 + h)


# Keep constructed neighbors inside the layer bounds: base box sits in an
# inset region and every neighbor extends at most MAX_SIDE beyond it.
INSET = MAX_SIDE + 20
BUILDER_MIN_SIDE = 24   # base boxes for builders need room for interior draws


def random_inner_box(rng):
    w = rng.randint(BUILDER_MIN_SIDE, MAX_SIDE)
    h = rng.randint(BUILDER_MIN_SIDE, MAX_SIDE)
    lo_x, hi_x = BOUNDS["min_x"] + INSET, BOUNDS["max_x"] - INSET - w
    lo_y, hi_y = BOUNDS["min_y"] + INSET, BOUNDS["max_y"] - INSET - h
    x0 = rng.randint(lo_x, hi_x)
    y0 = rng.randint(lo_y, hi_y)
    return box(x0, y0, x0 + w, y0 + h)


def overlap_area(a, b):
    w = max(0, min(a["max_x"], b["max_x"]) - max(a["min_x"], b["min_x"]))
    h = max(0, min(a["max_y"], b["max_y"]) - max(a["min_y"], b["min_y"]))
    return w * h


def contains_box(outer, inner):
    return (outer["min_x"] <= inner["min_x"] and outer["max_x"] >= inner["max_x"]
            and outer["min_y"] <= inner["min_y"] and outer["max_y"] >= inner["max_y"])


def closed_distance_sq(a, b):
    dx = max(a["min_x"] - b["max_x"], b["min_x"] - a["max_x"], 0)
    dy = max(a["min_y"] - b["max_y"], b["min_y"] - a["max_y"], 0)
    return dx * dx + dy * dy


def classify_locally(a, b):
    """Mirror of the server's plane.Relate rules, for candidate construction."""
    area = overlap_area(a, b)
    lc = contains_box(a, b)
    rc = contains_box(b, a)
    d2 = closed_distance_sq(a, b)
    intersects = area > 0
    if not intersects and d2 == 0:
        return C_TOUCH
    if intersects and not lc and not rc:
        return C_OVERLAP
    if (not lc) and rc:  # forward (a,b) not contained; reversed direction is
        return C_REVERSE
    return C_OTHER


# Deterministic builders for the three target classes ------------------------ #
def build_touch(rng):
    """Edge/corner contact only: half-open interiors disjoint, closure gap 0."""
    a = random_inner_box(rng)
    side = rng.randrange(4)
    w = rng.randint(MIN_SIDE, MAX_SIDE)
    h = rng.randint(MIN_SIDE, MAX_SIDE)
    if side == 0:      # b to the right, sharing the vertical edge a.max_x
        by0 = rng.randint(a["min_y"] - h + 3, a["max_y"] - 3)
        b = box(a["max_x"], by0, a["max_x"] + w, by0 + h)
    elif side == 1:    # b above, sharing the horizontal edge a.max_y
        bx0 = rng.randint(a["min_x"] - w + 3, a["max_x"] - 3)
        b = box(bx0, a["max_y"], bx0 + w, a["max_y"] + h)
    elif side == 2:    # b to the left
        by0 = rng.randint(a["min_y"] - h + 3, a["max_y"] - 3)
        b = box(a["min_x"] - w, by0, a["min_x"], by0 + h)
    else:              # corner contact
        corner = rng.randrange(4)
        if corner == 0:
            b = box(a["max_x"], a["max_y"], a["max_x"] + w, a["max_y"] + h)
        elif corner == 1:
            b = box(a["max_x"], a["min_y"] - h, a["max_x"] + w, a["min_y"])
        elif corner == 2:
            b = box(a["min_x"] - w, a["max_y"], a["min_x"], a["max_y"] + h)
        else:
            b = box(a["min_x"] - w, a["min_y"] - h, a["min_x"], a["min_y"])
    return a, b


def build_overlap(rng):
    """Strict partial overlap: positive-area intersection, neither contains."""
    a = random_inner_box(rng)
    # b starts strictly inside a but ends beyond a's right/top edges, so the
    # positive-area clip is real yet neither box can contain the other.
    bx0 = rng.randint(a["min_x"] + 1, a["max_x"] - 2)
    by0 = rng.randint(a["min_y"] + 1, a["max_y"] - 2)
    bx1 = a["max_x"] + rng.randint(1, MAX_SIDE // 2)
    by1 = a["max_y"] + rng.randint(1, MAX_SIDE // 2)
    return a, box(bx0, by0, bx1, by1)


def build_reverse(rng):
    """b strictly contains a: forward not-contained, reversed is containment."""
    b = random_inner_box(rng)
    # leave a positive margin on every side
    a = box(b["min_x"] + 1, b["min_y"] + 1, b["max_x"] - 1, b["max_y"] - 1)
    return a, b


BUILDERS = {C_TOUCH: build_touch, C_OVERLAP: build_overlap, C_REVERSE: build_reverse}


# --------------------------------------------------------------------------- #
# Server labeling
# --------------------------------------------------------------------------- #
def feature_id(pair_idx, side):
    return f"p{pair_idx:03d}-{side}"


def insert_pair(client, idx, a, b):
    items = [
        dict(layer=LAYER, id=feature_id(idx, "a"), mode="insert",
             value=dict(id=feature_id(idx, "a"), **{"class": FEATURE_CLASS},
                        shape=dict(extent=a))),
        dict(layer=LAYER, id=feature_id(idx, "b"), mode="insert",
             value=dict(id=feature_id(idx, "b"), **{"class": FEATURE_CLASS},
                        shape=dict(extent=b))),
    ]
    client.call("POST", "/edits", dict(edits=items), expect=200)


def query_relation(client, idx, left_side, right_side):
    payload = dict(left=dict(layer=LAYER, id=feature_id(idx, left_side)),
                   right=dict(layer=LAYER, id=feature_id(idx, right_side)))
    return client.call("POST", "/relation", payload, expect=200)


def class_from_server(resp_ab, resp_ba):
    """Derive the unordered-pair class strictly from two directional responses."""
    i1, i2 = resp_ab["intersects"], resp_ba["intersects"]
    assert i1 == i2, "intersection must be direction-independent"
    d2 = resp_ab["distance_squared"]
    assert d2 == resp_ba["distance_squared"], "distance must be direction-independent"
    lc_ab, rc_ab = resp_ab["left_contains"], resp_ab["right_contains"]
    if not i1 and d2 == 0:
        return C_TOUCH
    if i1 and not lc_ab and not rc_ab:
        return C_OVERLAP
    if i1 and (not lc_ab) and rc_ab:
        return C_REVERSE
    return C_OTHER


# --------------------------------------------------------------------------- #
# Main pipeline
# --------------------------------------------------------------------------- #
def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--binary", default="/tmp/meridian")
    ap.add_argument("--out", default=str(Path(__file__).resolve().parents[1]))
    ap.add_argument("--keep-server", action="store_true",
                    help="reuse an already running server (debug only)")
    args = ap.parse_args()

    out_root = Path(args.out)
    for sub in ("data", "evidence", "figures"):
        (out_root / sub).mkdir(parents=True, exist_ok=True)

    rng = random.Random(SEED)
    pairs = []                 # accepted unordered pairs
    stage_a_log = []           # every random draw with local + server class
    ledger = []                # supplement evidence
    raw_evidence = {}          # pair_idx -> {direction: raw server response}

    def run(client):
        # 1) layer ------------------------------------------------------------
        client.call("POST", "/strata",
                    dict(name=LAYER, kind="extent", bounds=BOUNDS,
                         exclusive=False), expect=201)

        idx = 0

        # 2) Stage A: blind random sampling, labeled by the public query ------
        for draw in range(RANDOM_DRAWS):
            a, b = random_box(rng), random_box(rng)
            local = classify_locally(a, b)
            insert_pair(client, idx, a, b)
            r_ab = query_relation(client, idx, "a", "b")
            r_ba = query_relation(client, idx, "b", "a")
            server_class = class_from_server(r_ab, r_ba)
            stage_a_log.append(dict(draw=draw, pair_index=idx,
                                    local_class=local, server_class=server_class,
                                    left=a, right=b,
                                    response_ab=r_ab, response_ba=r_ba))
            pairs.append(dict(pair_index=idx, stage="A_random",
                              target_class=server_class, left=a, right=b,
                              response_ab=r_ab, response_ba=r_ba))
            raw_evidence[idx] = dict(ab=r_ab, ba=r_ba)
            idx += 1

        accepted = Counter(p["target_class"] for p in pairs)

        # 3) Stage B: targeted supplementation for under-quota target classes -
        for cls in (C_TOUCH, C_OVERLAP, C_REVERSE):
            deficit = TARGET_PER_CLASS - accepted.get(cls, 0)
            if deficit <= 0:
                continue
            made = 0
            attempts = 0
            while made < deficit:
                attempts += 1
                a, b = BUILDERS[cls](rng)
                # bounds check
                for shp in (a, b):
                    assert all(BOUNDS[k0] <= shp[k1] and shp[k2] <= BOUNDS[k3]
                               for k0, k1, k2, k3 in (
                                   ("min_x", "min_x", "max_x", "max_x"),
                                   ("min_y", "min_y", "max_y", "max_y")))
                local = classify_locally(a, b)
                insert_pair(client, idx, a, b)
                r_ab = query_relation(client, idx, "a", "b")
                r_ba = query_relation(client, idx, "b", "a")
                server_class = class_from_server(r_ab, r_ba)
                if server_class != cls:
                    # builder missed (should not happen); record and skip
                    ledger.append(dict(pair_index=idx, wanted=cls,
                                       local_class=local,
                                       server_class=server_class,
                                       accepted=False, left=a, right=b))
                    idx += 1
                    continue
                pairs.append(dict(pair_index=idx, stage="B_supplement",
                                  target_class=cls, left=a, right=b,
                                  response_ab=r_ab, response_ba=r_ba))
                raw_evidence[idx] = dict(ab=r_ab, ba=r_ba)
                ledger.append(dict(pair_index=idx, wanted=cls,
                                   local_class=local, server_class=server_class,
                                   accepted=True, deficit_before=deficit - made,
                                   left=a, right=b,
                                   response_ab=r_ab, response_ba=r_ba))
                made += 1
                idx += 1

        # 4) keep exactly TARGET_PER_CLASS per target class; drop extras ------
        kept, seen = [], Counter()
        for p in pairs:
            c = p["target_class"]
            if c in (C_TOUCH, C_OVERLAP, C_REVERSE) and seen[c] < TARGET_PER_CLASS:
                kept.append(p)
                seen[c] += 1
        assert seen == Counter({C_TOUCH: TARGET_PER_CLASS,
                                C_OVERLAP: TARGET_PER_CLASS,
                                C_REVERSE: TARGET_PER_CLASS}), seen
        return kept, stage_a_log, ledger, raw_evidence

    import tempfile
    with tempfile.TemporaryDirectory(prefix="meridian-blind-") as tmp:
        with server(args.binary, Path(tmp)) as cli:
            kept, stage_a_log, ledger, raw_evidence = run(cli)

    # 5) deterministic group split at the UNORDERED-pair level ----------------
    rng_split = random.Random(SEED + 1)
    by_class = {}
    for p in kept:
        by_class.setdefault(p["target_class"], []).append(p)
    groups = {}  # pair_index -> group
    for cls, plist in by_class.items():
        plist = sorted(plist, key=lambda p: p["pair_index"])
        rng_split.shuffle(plist)
        audit = set(p["pair_index"] for p in plist[:AUDIT_PER_CLASS])
        for p in plist:
            groups[p["pair_index"]] = "audit" if p["pair_index"] in audit else "train"

    # 6) write artifacts ------------------------------------------------------
    write_artifacts(out_root, kept, groups, stage_a_log, ledger, raw_evidence)


# --------------------------------------------------------------------------- #
# Artifacts
# --------------------------------------------------------------------------- #
CLASS_CN = {
    C_TOUCH: "零距离不相交",
    C_OVERLAP: "正面积相交但互不包含",
    C_REVERSE: "反转左右才变成包含",
    C_OTHER: "其他（不入选）",
}


def ordered_label(pair, direction):
    """Human label for an ORDERED row, derived from server relation flags."""
    if direction == "ab":
        r = pair["response_ab"]
    else:
        r = pair["response_ba"]
    if not r["intersects"]:
        return "不相交（闭包零距离）" if r["distance_squared"] == 0 else "不相交（正距离）"
    if r["left_contains"] and r["right_contains"]:
        return "互相包含（全等）"
    if r["left_contains"]:
        return "左包含右"
    if r["right_contains"]:
        return "右包含左（反转左右后左包含右）"
    return "正面积相交，互不包含"


def write_artifacts(out_root, kept, groups, stage_a_log, ledger, raw_evidence):
    data_dir = out_root / "data"
    ev_dir = out_root / "evidence"

    blind_rows, key_rows, evidence_rows = [], [], []
    pair_meta = []
    for p in sorted(kept, key=lambda x: x["pair_index"]):
        pid = f"pair_{p['pair_index']:03d}"
        group = groups[p["pair_index"]]
        pair_meta.append(dict(
            pair_id=pid, pair_index=p["pair_index"], group=group,
            stage=p["stage"], target_class=p["target_class"],
            target_class_cn=CLASS_CN[p["target_class"]],
            left_id=feature_id(p["pair_index"], "a"),
            right_id=feature_id(p["pair_index"], "b"),
            left=p["left"], right=p["right"]))
        for direction, lside, rside, resp in (
                ("forward", "a", "b", p["response_ab"]),
                ("reversed", "b", "a", p["response_ba"])):
            row_id = f"{pid}_{direction}"
            blind_rows.append(dict(
                row_id=row_id, pair_id=pid, group=group, direction=direction,
                left_feature=dict(layer=LAYER,
                                  id=feature_id(p["pair_index"], lside)),
                right_feature=dict(layer=LAYER,
                                   id=feature_id(p["pair_index"], rside)),
                left_shape=p["left"] if lside == "a" else p["right"],
                right_shape=p["right"] if rside == "b" else p["left"]))
            key_rows.append(dict(
                row_id=row_id, pair_id=pid, group=group, direction=direction,
                target_class=p["target_class"],
                target_class_cn=CLASS_CN[p["target_class"]],
                ordered_relation=ordered_label(p, "ab" if direction == "forward" else "ba"),
                intersects=resp["intersects"],
                left_contains=resp["left_contains"],
                right_contains=resp["right_contains"],
                distance_squared=resp["distance_squared"],
                intersection=resp.get("intersection")))
            evidence_rows.append(dict(
                row_id=row_id, pair_id=pid, direction=direction,
                query=dict(left=dict(layer=LAYER,
                                     id=feature_id(p["pair_index"], lside)),
                           right=dict(layer=LAYER,
                                      id=feature_id(p["pair_index"], rside))),
                raw_response=resp,
                source="POST /meridian/v1/relation"))

    blind = dict(
        artifact="blind_input",
        note="盲审输入：仅含坐标、行号与分组，不含任何关系判定字段",
        seed=SEED, plane_bounds=BOUNDS, layer=LAYER,
        rows=blind_rows)
    key = dict(
        artifact="answer_key",
        note="答键：由 Meridian 公开关系查询 POST /relation 实际返回值生成",
        seed=SEED,
        class_legend={c: CLASS_CN[c] for c in
                      (C_TOUCH, C_OVERLAP, C_REVERSE, C_OTHER)},
        rows=key_rows)
    evidence = dict(
        artifact="raw_shape_evidence",
        note="原始形状证据：每条有序查询的原始 HTTP 响应与查询入参",
        layer_bounds=BOUNDS, pairs=pair_meta, rows=evidence_rows)

    (data_dir / "blind_input.json").write_text(
        json.dumps(blind, ensure_ascii=False, indent=2) + "\n")
    (data_dir / "answer_key.json").write_text(
        json.dumps(key, ensure_ascii=False, indent=2) + "\n")
    (ev_dir / "raw_relation_responses.json").write_text(
        json.dumps(evidence, ensure_ascii=False, indent=2) + "\n")
    (ev_dir / "stage_a_random_draws.json").write_text(
        json.dumps(dict(
            artifact="stage_a_random_sampling_log",
            note="阶段A盲抽全量记录：固定采样窗口内 300 个随机对的本地预判类与服务器实际类，用于核对类别不均衡",
            sample_window=SAMPLE_WINDOW, layer_bounds=BOUNDS,
            draws=stage_a_log), ensure_ascii=False, indent=2) + "\n")

    # supplement ledger -------------------------------------------------------
    stage_a_counts = Counter(d["server_class"] for d in stage_a_log)
    accepted_supp = Counter(e["server_class"] for e in ledger if e.get("accepted"))
    final_counts = Counter(p["target_class"] for p in kept)
    ledger_doc = dict(
        artifact="supplement_ledger",
        note="补样依据：阶段A盲抽后各类缺口、定向构造数量与服务器复核结果",
        sample_window=SAMPLE_WINDOW, layer_bounds=BOUNDS,
        random_draws=RANDOM_DRAWS,
        target_per_class=TARGET_PER_CLASS,
        stage_a_server_class_counts=dict(stage_a_counts),
        deficits={c: max(0, TARGET_PER_CLASS - stage_a_counts.get(c, 0))
                  for c in (C_TOUCH, C_OVERLAP, C_REVERSE)},
        supplements_accepted=dict(accepted_supp),
        final_class_counts=dict(final_counts),
        balance_check={c: final_counts.get(c, 0) == TARGET_PER_CLASS
                       for c in (C_TOUCH, C_OVERLAP, C_REVERSE)},
        entries=ledger)
    (ev_dir / "supplement_ledger.json").write_text(
        json.dumps(ledger_doc, ensure_ascii=False, indent=2) + "\n")

    # summary -----------------------------------------------------------------
    summary = dict(
        seed=SEED,
        random_draws=RANDOM_DRAWS,
        stage_a_class_counts=dict(stage_a_counts),
        supplement_counts=dict(accepted_supp),
        final_pair_counts=dict(final_counts),
        ordered_rows=len(key_rows),
        group_pair_counts=Counter(groups.values()),
        group_row_counts=Counter(r["group"] for r in key_rows),
        groups_by_class={
            c: dict(Counter(groups[p["pair_index"]] for p in
                            kept if p["target_class"] == c))
            for c in (C_TOUCH, C_OVERLAP, C_REVERSE)})
    (data_dir / "sampling_summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
