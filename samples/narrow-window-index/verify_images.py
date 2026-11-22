#!/usr/bin/env python3
"""Decode both shipped MPB images and compare their catalog content.

Checks, without trusting the payload files or the server:
  * the MPB v1 header signature, payload length and SHA-256;
  * the image payload is canonical JSON (re-encoding reproduces the bytes);
  * both images carry the same layer name, kind, bounds, exclusivity and
    revision;
  * both images carry the same 400 feature IDs and the same ID -> class map;
  * exactly the 180 fringe features differ in geometry, and every other
    feature (anchors, hits, background) is byte-identical.
"""
import hashlib
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
SIGNATURE = b'MRDNMPB\x01'
HEADER = 8 + 4 + 32


def decode(path):
    raw = path.read_bytes()
    assert raw[:8] == SIGNATURE, 'bad signature'
    assert int.from_bytes(raw[8:12], 'big') == len(raw) - HEADER, 'bad length'
    assert raw[12:HEADER] == hashlib.sha256(raw[HEADER:]).digest(), 'bad checksum'
    payload = raw[HEADER:]
    image = json.loads(payload)
    # Go marshals struct fields in declaration order, so reproduce that order
    # (json.loads keeps it) rather than sorting keys.
    assert json.dumps(image, separators=(',', ':')).encode() == payload.rstrip(), \
        'payload does not re-encode to the same canonical bytes'
    return image


def index(image):
    layer = image['layers'][0]
    return layer, {f['id']: (f['class'], json.dumps(f['shape'], sort_keys=True))
                   for f in layer['features']}


def main():
    a = decode(HERE / 'images' / 'sample-a.mpb')
    b = decode(HERE / 'images' / 'sample-b.mpb')
    la, fa = index(a)
    lb, fb = index(b)

    assert len(a['layers']) == len(b['layers']) == 1
    assert la['definition'] == lb['definition'], 'layer definitions differ'
    assert la['revision'] == lb['revision'], 'layer revisions differ'
    assert a['revision'] == b['revision'], 'database revisions differ'
    assert list(fa) == list(fb), 'feature ID sets or order differ'
    assert len(fa) == 400

    classes_a = sorted(cls for cls, _ in fa.values())
    classes_b = sorted(cls for cls, _ in fb.values())
    assert classes_a == classes_b, 'class distribution differs'
    moved = [fid for fid in fa if fa[fid][1] != fb[fid][1]]
    assert sorted(moved) == sorted(fid for fid in fa if fid.startswith('fringe-')), \
        'a non-fringe feature moved'
    assert len(moved) == 180, len(moved)
    assert all(fa[fid][0] == fb[fid][0] for fid in fa), 'a feature changed class'

    print(json.dumps(dict(
        layer_definition=la['definition'],
        revisions=dict(database=a['revision'], layer=la['revision']),
        features=len(fa),
        classes={c: classes_a.count(c) for c in sorted(set(classes_a))},
        moved_features=len(moved),
        unchanged_non_fringe=len(fa) - len(moved),
        image_bytes=dict(a=(HERE / 'images' / 'sample-a.mpb').stat().st_size,
                         b=(HERE / 'images' / 'sample-b.mpb').stat().st_size),
        image_sha256=dict(
            a=hashlib.sha256((HERE / 'images' / 'sample-a.mpb').read_bytes()).hexdigest(),
            b=hashlib.sha256((HERE / 'images' / 'sample-b.mpb').read_bytes()).hexdigest()),
    ), indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
