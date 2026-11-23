"""Shared helpers for the Meridian Patch backup toolchain.

These helpers only depend on the Python standard library. They speak the
production HTTP/JSON API and parse the documented MPB v1 on-disk format so that
backup metadata can be proven against exact bytes without a running verifier.
"""
from __future__ import annotations

import hashlib
import json
import os
import struct
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

PREFIX = "/meridian/v1"
IMAGE_TYPE = "application/vnd.meridian.image"
SIGNATURE = b"MRDNMPB\x01"
HEADER_SIZE = 8 + 4 + 32
MAX_IMAGE = 16 << 20

# Go encoding/json emits object fields in struct declaration order, not sorted
# order. These key lists mirror the tagged Go structs that make up an image.
_GO_KEY_ORDER = {
    "definition": ("name", "kind", "bounds", "exclusive"),
    "box": ("min_x", "min_y", "max_x", "max_y"),
    "point": ("x", "y"),
}


def _go_json_string(value: str) -> str:
    # Match Go's string escaping (HTML escapes included).
    out = json.dumps(value, ensure_ascii=False)
    for char, escape in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"),
                         (" ", "\\u2028"), (" ", "\\u2029")):
        out = out.replace(char, escape)
    return out


def _canonical(value, key_order: tuple | None = None) -> str:
    if isinstance(value, dict):
        keys = key_order if key_order is not None else tuple(value.keys())
        parts = []
        for key in keys:
            if key not in value:
                continue
            child_order = {
                "bounds": _GO_KEY_ORDER["box"],
                "shape": None,
                "mark": _GO_KEY_ORDER["point"],
                "extent": _GO_KEY_ORDER["box"],
            }.get(key)
            parts.append(json.dumps(key) + ":" + _canonical(value[key], child_order))
        return "{" + ",".join(parts) + "}"
    if isinstance(value, list):
        return "[" + ",".join(_canonical(item) for item in value) + "]"
    if isinstance(value, str):
        return _go_json_string(value)
    if isinstance(value, bool):
        return "true" if value else "false"
    if value is None:
        return "null"
    return str(value)


def canonical_image_bytes(image: dict) -> bytes:
    """Serialize a decoded image exactly as Go's json.Marshal would."""
    layers = []
    for layer in image["layers"]:
        features = []
        for feature in layer["features"]:
            shape = feature["shape"]
            if "mark" in shape:
                shape_text = '{"mark":' + _canonical(shape["mark"], _GO_KEY_ORDER["point"]) + "}"
            else:
                shape_text = '{"extent":' + _canonical(shape["extent"], _GO_KEY_ORDER["box"]) + "}"
            feature_text = (
                "{" + f'"id":{_go_json_string(feature["id"])},"class":'
                f'{_go_json_string(feature["class"])},"shape":{shape_text}' + "}")
            features.append(feature_text)
        definition_text = _canonical(layer["definition"], _GO_KEY_ORDER["definition"])
        layers.append(
            "{" + f'"definition":{definition_text},"revision":{layer["revision"]},'
            f'"features":[' + ",".join(features) + "]}")
    return ("{" + f'"revision":{image["revision"]},"layers":['
            + ",".join(layers) + "]}").encode()


class MPBError(ValueError):
    """Raised when bytes do not satisfy the MPB v1 format rules."""


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def iso_from_epoch(seconds: float) -> str:
    return datetime.fromtimestamp(seconds, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def http_call(base_url, method, path, body=None, headers=None, timeout=15):
    """Perform one HTTP call and return (status, headers, raw_bytes)."""
    payload = None
    merged = dict(headers or {})
    if isinstance(body, (dict, list)):
        payload = json.dumps(body).encode()
        merged.setdefault("Content-Type", "application/json")
    elif isinstance(body, bytes):
        payload = body
    request = urllib.request.Request(
        base_url.rstrip("/") + PREFIX + path,
        data=payload,
        method=method,
        headers=merged,
    )
    try:
        response = urllib.request.urlopen(request, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, dict(response.headers), response.read()


def http_json(base_url, method, path, body=None, headers=None, timeout=15):
    status, response_headers, raw = http_call(base_url, method, path, body, headers, timeout)
    return status, response_headers, json.loads(raw.decode())


def fetch_image(base_url, timeout=30):
    """Fetch one immutable committed snapshot.

    The server renders the image from a single published revision under the
    write lock, so the returned bytes are always one commit even while writes
    keep moving. Returns (raw_bytes, committed_revision).
    """
    status, headers, raw = http_call(base_url, "GET", "/image", timeout=timeout)
    if status != 200:
        raise RuntimeError(f"image export failed with HTTP {status}: {raw[:200]!r}")
    if headers.get("Content-Type") != IMAGE_TYPE:
        raise RuntimeError(f"unexpected image content type {headers.get('Content-Type')!r}")
    revision = int(headers["Meridian-Revision"])
    return raw, revision


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def parse_mpb(raw: bytes):
    """Validate MPB v1 framing and return (image_dict, payload_sha256_hex).

    Checks the fixed signature, declared payload length, SHA-256 and canonical
    JSON ordering. This mirrors the Go decoder's cheap structural checks and is
    used as a defense-in-depth fallback; full geometry/index validation is done
    offline by cmd/mpbverify.
    """
    if len(raw) < HEADER_SIZE or len(raw) > MAX_IMAGE:
        raise MPBError("invalid image size")
    if raw[:8] != SIGNATURE:
        raise MPBError("bad MPB v1 signature")
    declared = struct.unpack(">I", raw[8:12])[0]
    payload = raw[HEADER_SIZE:]
    if declared != len(payload):
        raise MPBError("image payload length mismatch")
    checksum = hashlib.sha256(payload).hexdigest()
    embedded = raw[12:HEADER_SIZE].hex()
    if checksum != embedded:
        raise MPBError("image checksum mismatch")
    image = json.loads(payload.decode())
    if not isinstance(image, dict) or not isinstance(image.get("layers"), list):
        raise MPBError("image payload is not a catalog image")
    # Canonical JSON: Go encoding/json emits struct fields in declaration
    # order with HTML-escaped strings. canonical_image_bytes reproduces the
    # exact wire bytes so any tampering of the payload fails here.
    if canonical_image_bytes(image) != payload:
        raise MPBError("noncanonical image payload")
    return image, checksum


def feature_map(image):
    """Return {layer_name: {feature_id: feature}} from a decoded image."""
    out = {}
    for layer in image["layers"]:
        name = layer["definition"]["name"]
        out[name] = {feature["id"]: feature for feature in layer["features"]}
    return out


def layer_bounds(image):
    return {
        layer["definition"]["name"]: layer["definition"]["bounds"]
        for layer in image["layers"]
    }


def atomic_write(path: Path, data: bytes):
    """Write to a temp file, fsync, then atomically rename."""
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    with open(temporary, "wb") as handle:
        handle.write(data)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, path)


def write_json_atomic(path: Path, value):
    atomic_write(path, json.dumps(value, indent=2, sort_keys=True).encode() + b"\n")


def box(min_x, min_y, max_x, max_y):
    return dict(min_x=min_x, min_y=min_y, max_x=max_x, max_y=max_y)


def now_epoch() -> int:
    return int(time.time())
