"""MPB v1 image codec, byte-compatible with internal/folio (Go).

Layout: 8-byte signature 'MRDNMPB\\x01' | uint32 big-endian payload length |
32-byte SHA-256 of payload | canonical JSON payload.

Canonical JSON means Go encoding/json output for the catalog structs:
compact separators, struct field order, no HTML-escaping concerns because
valid names/ids/classes match ^[a-z][a-z0-9-]{0,47}$ (pure ASCII).
"""
import hashlib
import json
import struct

SIGNATURE = b"MRDNMPB\x01"
HEADER_SIZE = 8 + 4 + 32
MAX_IMAGE = 16 << 20

# Field sets mirror the Go structs so unknown keys are rejected like
# json.Decoder.DisallowUnknownFields does in folio.Decode.
_BOX_KEYS = ("min_x", "min_y", "max_x", "max_y")
_POINT_KEYS = ("x", "y")
_SHAPE_KEYS = ("mark", "extent")
_FEATURE_KEYS = ("id", "class", "shape")
_DEFINITION_KEYS = ("name", "kind", "bounds", "exclusive")
_LAYER_KEYS = ("definition", "revision", "features")
_IMAGE_KEYS = ("revision", "layers")


class ImageError(ValueError):
    pass


def _check_keys(obj, allowed, where):
    if not isinstance(obj, dict):
        raise ImageError(f"{where}: expected object")
    for key in obj:
        if key not in allowed:
            raise ImageError(f"{where}: unknown field {key!r}")


def _validate(image):
    _check_keys(image, _IMAGE_KEYS, "image")
    if not isinstance(image.get("revision"), int):
        raise ImageError("image: revision must be an integer")
    layers = image.get("layers")
    if not isinstance(layers, list):
        raise ImageError("image: layers must be a list")
    for layer in layers:
        _check_keys(layer, _LAYER_KEYS, "layer")
        _check_keys(layer.get("definition"), _DEFINITION_KEYS, "definition")
        bounds = layer["definition"].get("bounds")
        _check_keys(bounds, _BOX_KEYS, "bounds")
        if not isinstance(layer.get("features"), list):
            raise ImageError("layer: features must be a list")
        for feature in layer["features"]:
            _check_keys(feature, _FEATURE_KEYS, "feature")
            shape = feature.get("shape")
            _check_keys(shape, _SHAPE_KEYS, "shape")
            if "extent" in shape:
                _check_keys(shape["extent"], _BOX_KEYS, "extent")
            if "mark" in shape:
                _check_keys(shape["mark"], _POINT_KEYS, "mark")


def canonical_payload(image):
    """Serialize exactly like Go json.Marshal on the catalog structs."""
    _validate(image)
    return json.dumps(image, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def encode(image):
    payload = canonical_payload(image)
    if len(payload) > MAX_IMAGE - HEADER_SIZE:
        raise ImageError("image exceeds maximum size")
    digest = hashlib.sha256(payload).digest()
    return SIGNATURE + struct.pack(">I", len(payload)) + digest + payload


def decode(raw):
    if not isinstance(raw, (bytes, bytearray)):
        raise ImageError("image must be bytes")
    if len(raw) < HEADER_SIZE or len(raw) > MAX_IMAGE or bytes(raw[:8]) != SIGNATURE:
        raise ImageError("invalid image size or version")
    payload = bytes(raw[HEADER_SIZE:])
    if struct.unpack(">I", raw[8:12])[0] != len(payload):
        raise ImageError("image payload length mismatch")
    if bytes(raw[12:HEADER_SIZE]) != hashlib.sha256(payload).digest():
        raise ImageError("image checksum mismatch")
    try:
        image = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ImageError(f"decode image: {exc}") from exc
    _validate(image)
    if canonical_payload(image) != payload:
        raise ImageError("noncanonical image payload")
    return image


def box(x0, y0, x1, y1):
    return {"min_x": x0, "min_y": y0, "max_x": x1, "max_y": y1}


def clip(inner, outer):
    """plane.Box.Clip: positive-area intersection, None when empty."""
    cut = box(
        max(inner["min_x"], outer["min_x"]),
        max(inner["min_y"], outer["min_y"]),
        min(inner["max_x"], outer["max_x"]),
        min(inner["max_y"], outer["max_y"]),
    )
    if cut["max_x"] > cut["min_x"] and cut["max_y"] > cut["min_y"]:
        return cut
    return None


def area(b):
    return (b["max_x"] - b["min_x"]) * (b["max_y"] - b["min_y"])


def overlaps(a, b):
    """Positive-area intersection, the exclusivity conflict predicate."""
    return clip(a, b) is not None


def sha256_hex(raw):
    return hashlib.sha256(raw).hexdigest()
