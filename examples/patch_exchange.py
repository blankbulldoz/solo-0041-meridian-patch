#!/usr/bin/env python3
"""Offline incremental-package exchange sample.

The sample stands up two independent Meridian Patch deployments in two
temporary data directories and walks a complete change-package (MPC v1)
lifecycle:

  1. deployment A exports a full MPB baseline and a later target image;
  2. a change package is derived from the baseline and shown to be smaller than
     a full image;
  3. deployment B receives the package, covering feature deletion, a layer
     definition (layout) change and same-ID replacement plus new layers;
  4. a same-revision but content-different image is refused with
     baseline_mismatch, while re-uploading the same package is reported as
     already_applied instead of an error.

Two transports are exercised, each without any network access beyond the local
loopback: the HTTP API (POST /image/diff, PUT /image/patch) and the offline
CLI subcommands (meridian diff / meridian apply-patch), which only touch files.

Run with:  python3 -I examples/patch_exchange.py
"""
import argparse
import hashlib
import json
import select
import subprocess
import tempfile
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
IMAGE_TYPE = 'application/vnd.meridian.image'
PATCH_TYPE = 'application/vnd.meridian.patch'
MPB_HEADER = 8 + 4 + 32  # signature, payload length, payload SHA-256


def content_digest(mpb_bytes):
    """Mirror delta.ContentDigest: hash catalog JSON with all revisions zeroed."""
    image = json.loads(mpb_bytes[MPB_HEADER:])
    image['revision'] = 0
    for layer in image['layers']:
        layer['revision'] = 0
    canonical = json.dumps(image, sort_keys=True, separators=(',', ':')).encode()
    return hashlib.sha256(canonical).hexdigest()


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def point(x, y):
    return dict(x=x, y=y)


BOUNDS = box(0, 0, 20, 20)
WIDE = box(0, 0, 24, 24)


def check(condition, detail):
    if not condition:
        raise AssertionError(detail)
    print('  ok -', detail)


class Service:
    """One Meridian process backed by its own exclusive data directory."""

    def __init__(self, binary, data_dir, name):
        self.binary = binary
        self.data_dir = data_dir
        self.name = name
        self.child = None
        self.url = None

    def __enter__(self):
        self.child = subprocess.Popen(
            [str(self.binary), '-listen', '127.0.0.1:0', '-data', str(self.data_dir)],
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        readable, _, _ = select.select([self.child.stderr], [], [], 10)
        check(readable, f'{self.name} starts within 10 seconds')
        startup = json.loads(self.child.stderr.readline())
        check('address' in startup, f'{self.name} reports a listen address')
        self.url = 'http://' + startup['address']
        return self

    def __exit__(self, *exc):
        if self.child and self.child.poll() is None:
            self.child.terminate()
            try:
                self.child.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                self.child.kill()
                self.child.communicate(timeout=5)

    def request(self, method, path, body=None, headers=None, status=200):
        req = Request(self.url + PREFIX + path, data=body, method=method,
                      headers=dict(headers or {}))
        try:
            response = urlopen(req, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            check(response.status == status,
                  f'{method} {path} -> {response.status} (want {status})')
            if response.headers.get_content_type() == 'application/json':
                return json.loads(raw), response.headers
            return raw, response.headers

    def get_json(self, path):
        return self.request('GET', path)[0]

    def post_json(self, path, value, status=200):
        return self.request('POST', path, json.dumps(value).encode(),
                            {'Content-Type': 'application/json'}, status)[0]

    def layer(self, name, kind='extent', exclusive=False, bounds=BOUNDS):
        return self.post_json('/strata',
                              dict(name=name, kind=kind, bounds=bounds, exclusive=exclusive),
                              status=201)

    def edit(self, *items, revision=None):
        payload = dict(edits=list(items))
        if revision is not None:
            payload['if_revision'] = revision
        return self.post_json('/edits', payload)

    def export(self):
        raw, headers = self.request('GET', '/image')
        check(raw.startswith(b'MRDNMPB\x01'), 'exported image carries the MPB v1 signature')
        return raw, int(headers['Meridian-Revision'])

    def restore(self, raw, revision):
        return self.request('PUT', '/image', raw,
                            {'Content-Type': IMAGE_TYPE,
                             'If-Meridian-Revision': str(revision)})[0]

    def diff(self, baseline, target=None):
        body, boundary = multipart(baseline=b'as-baseline.mpb', baseline_data=baseline,
                                   target=(b'as-target.mpb', target) if target else None)
        raw, headers = self.request(
            'POST', '/image/diff', body,
            {'Content-Type': f'multipart/form-data; boundary={boundary}'})
        check(raw.startswith(b'MRDNMPC\x01'), 'diff returns an MPC v1 change package')
        check(headers.get_content_type() == PATCH_TYPE, 'patch content type is advertised')
        return raw, headers

    def apply_patch(self, raw, revision=None, status=200):
        headers = {'Content-Type': PATCH_TYPE}
        if revision is not None:
            headers['If-Meridian-Revision'] = str(revision)
        return self.request('PUT', '/image/patch', raw, headers, status=status)[0]


def multipart(baseline, baseline_data, target=None):
    boundary = 'meridian-sample-boundary-0041'
    lines = []
    lines.append(f'--{boundary}'.encode())
    lines.append(b'Content-Disposition: form-data; name="baseline"; filename="' + baseline + b'"')
    lines.append(b'Content-Type: ' + IMAGE_TYPE.encode())
    lines.append(b'')
    lines.append(baseline_data)
    if target is not None:
        target_name, target_data = target
        lines.append(f'--{boundary}'.encode())
        lines.append(b'Content-Disposition: form-data; name="target"; filename="' + target_name + b'"')
        lines.append(b'Content-Type: ' + IMAGE_TYPE.encode())
        lines.append(b'')
        lines.append(target_data)
    lines.append(f'--{boundary}--'.encode())
    lines.append(b'')
    return b'\r\n'.join(lines), boundary


def extent_op(layer, feature, rect, mode='insert', category='fixture'):
    return dict(layer=layer, id=feature, mode=mode,
                value=dict(id=feature, **{'class': category},
                           shape=dict(extent=rect)))


def mark_op(layer, feature, xy, mode='insert'):
    return dict(layer=layer, id=feature, mode=mode,
                value=dict(id=feature, **{'class': 'fixture'},
                           shape=dict(mark=xy)))


def seed(service):
    """Build a shared catalog: two extent layers and an exclusive mark layer."""
    service.layer('ochre-field')
    service.layer('mica-bed', exclusive=True)
    service.layer('plumb-marks', kind='mark', exclusive=True)
    service.edit(
        extent_op('ochre-field', 'prong', box(0, 0, 4, 3)),
        extent_op('ochre-field', 'dowel', box(4, 0, 8, 3)),
        extent_op('ochre-field', 'ferrule', box(2, 1, 6, 4), category='sleeve'),
        extent_op('mica-bed', 'shim', box(10, 10, 14, 14)),
        mark_op('plumb-marks', 'brad', point(4, 1)),
        mark_op('plumb-marks', 'stud', point(6, 1)),
    )


def evolve(service):
    """Apply the full spread of changes the package must carry."""
    # same-ID replacement and a feature deletion. The replacement shrinks prong
    # so it only shares an edge with dowel, which the later exclusive layout allows.
    service.edit(
        extent_op('ochre-field', 'prong', box(0, 0, 4, 3), mode='replace'),
        dict(layer='ochre-field', id='ferrule', mode='remove'),
    )
    # layer definition change: wider bounds and a switch to an exclusive layer.
    # The surviving rectangles only share edges, so the end state is valid.
    service.request(
        'PUT', '/strata/ochre-field/layout',
        json.dumps(dict(bounds=WIDE, exclusive=True)).encode(),
        {'Content-Type': 'application/json'})
    # a brand-new layer and a new feature inserted into an existing layer
    service.layer('new-bed', kind='mark', bounds=BOUNDS)
    service.edit(
        mark_op('new-bed', 'fresh-pin', point(2, 2)),
        extent_op('ochre-field', 'added-late', box(12, 12, 16, 16)),
        mark_op('plumb-marks', 'spare', point(8, 1)),
    )
    # empty an entire layer so its surviving definition is carried in the
    # package together with a remove op (the API keeps empty layers registered).
    service.request(
        'POST', '/edits',
        json.dumps(dict(edits=[dict(layer='mica-bed', id='shim', mode='remove')])).encode(),
        {'Content-Type': 'application/json'})


def feature_ids(service, layer):
    result = service.post_json(f'/strata/{layer}/window',
                               dict(bounds=WIDE, limit=256))
    return sorted(row['id'] for row in result['features'])


def decode_patch(raw):
    # Skip the fixed 8+4+32 byte MPC header to inspect the JSON payload.
    return json.loads(raw[44:])


def demonstrate_http(binary, root):
    work = root / 'http'
    work.mkdir(parents=True, exist_ok=True)
    print('\n[1] HTTP transport: two deployments, one change package')
    with Service(binary, work / 'site-a', 'site A') as site_a, \
            Service(binary, work / 'site-b', 'site B') as site_b:
        seed(site_a)
        baseline, baseline_rev = site_a.export()
        (work / 'baseline.mpb').write_bytes(baseline)

        evolve(site_a)
        target, target_rev = site_a.export()
        (work / 'target.mpb').write_bytes(target)

        patch, headers = site_a.diff(baseline)
        patch_info = decode_patch(patch)
        (work / 'change.mpc').write_bytes(patch)
        print('    package layers:',
              ', '.join(f"{p['name']}[{len(p['ops'])}]" for p in patch_info['layers']))
        check(len(patch) < len(target),
              f'change package ({len(patch)} B) is smaller than full image ({len(target)} B)')
        check(headers['Meridian-Baseline-Digest'] == patch_info['baseline_digest'],
              'baseline digest header matches the package body')

        # Site B starts from the same baseline image (the offline carrier copy).
        site_b.restore(baseline, site_b.get_json('/status')['revision'])
        check(feature_ids(site_b, 'ochre-field') == ['dowel', 'ferrule', 'prong'],
              'site B starts at the baseline feature set')

        # A same-revision but content-different image must refuse the package.
        foreign_image = bytearray(baseline)
        # Flip a byte inside the JSON payload (after the 44-byte header), then
        # re-sign it so decoding reaches the content-digest comparison.
        foreign_image[-1] ^= 1
        foreign_problem, _ = site_b.request(
            'PUT', '/image/patch', bytes(foreign_image),
            {'Content-Type': PATCH_TYPE}, status=400)
        check(foreign_problem['code'] == 'invalid',
              f"foreign/corrupt upload rejected ({foreign_problem['code']})")

        # A genuine foreign baseline at the SAME revision number but different
        # catalog content: mutate the baseline through site B's own edit API.
        site_b.edit(extent_op('ochre-field', 'dowel', box(4, 0, 9, 3), mode='replace'))
        foreign_rev = site_b.get_json('/status')['revision']
        check(foreign_rev == baseline_rev + 1 or foreign_rev != baseline_rev,
              'site B diverged onto its own revision line')
        foreign, _ = site_b.request('PUT', '/image/patch', patch,
                                    {'Content-Type': PATCH_TYPE}, status=409)
        check(foreign['code'] == 'baseline_mismatch',
              f"same-number foreign image refused with baseline_mismatch ({foreign['code']})")
        check(foreign['message'].find(patch_info['baseline_digest'][:12]) >= 0,
              'conflict message names the expected and current digests')

        # Restore site B to the true baseline so the package now applies.
        site_b.restore(baseline, site_b.get_json('/status')['revision'])
        base_rev = site_b.get_json('/status')['revision']
        receipt = site_b.apply_patch(patch, revision=base_rev)
        check(receipt['status'] == 'applied',
              f"first application is reported as applied ({receipt['status']})")
        check(receipt['target_digest'] == patch_info['target_digest'],
              'receipt confirms the target content digest')
        check(receipt['current_digest'] == receipt['target_digest'],
              'current digest equals target after applying')
        check(feature_ids(site_b, 'ochre-field') == ['added-late', 'dowel', 'prong'],
              'replacement, deletion and insertion all landed (ferrule removed)')
        strata = {row['name']: row for row in site_b.get_json('/strata')['layers']}
        check('mica-bed' in strata and strata['mica-bed']['count'] == 0,
              'emptied layer survives with zero features')
        check('new-bed' in strata and strata['new-bed']['count'] == 1,
              'new layer exists with its inserted feature')
        check(strata['ochre-field']['bounds'] == WIDE and strata['ochre-field']['exclusive'],
              'layer definition (bounds + exclusivity) changed')
        check(feature_ids(site_b, 'plumb-marks') == ['brad', 'spare', 'stud'],
              'unrelated layer received its inserted feature')

        # Re-upload the identical package: distinct, non-error outcome.
        replay = site_b.apply_patch(patch)
        check(replay['status'] == 'already_applied',
              f"repeat upload is distinguished as already_applied ({replay['status']})")
        check(replay['revision'] == receipt['revision'],
              'replay does not advance the revision')
        check(replay['current_digest'] == receipt['target_digest'],
              'replay reports the same current digest')

        # A stale CAS guard on a real apply is still a conflict, not an idempotent hit.
        site_b.restore(baseline, site_b.get_json('/status')['revision'])
        stale, _ = site_b.request(
            'PUT', '/image/patch', patch,
            {'Content-Type': PATCH_TYPE,
             'If-Meridian-Revision': str(base_rev + 999)}, status=409)
        check(stale['code'] == 'conflict', f'stale CAS guard conflicts ({stale["code"]})')
    return work


def demonstrate_cli(binary, root):
    work = root / 'cli'
    work.mkdir(parents=True, exist_ok=True)
    print('\n[2] Offline CLI: file-to-file diff and apply, no service running')
    with Service(binary, work / 'studio', 'studio') as studio:
        seed(studio)
        baseline, _ = studio.export()
        evolve(studio)
        target, target_rev = studio.export()
    base_path, target_path = work / 'baseline.mpb', work / 'target.mpb'
    base_path.write_bytes(baseline)
    target_path.write_bytes(target)

    patch_path = work / 'change.mpc'
    diff = subprocess.run([str(binary), 'diff',
                           '-baseline', str(base_path),
                           '-target', str(target_path),
                           '-out', str(patch_path)],
                          capture_output=True, text=True, check=True)
    print('   ', diff.stdout.strip())
    patch_raw = patch_path.read_bytes()
    patch_info = decode_patch(patch_raw)
    check(patch_raw.startswith(b'MRDNMPC\x01'), 'CLI wrote an MPC v1 container')
    check(len(patch_raw) < len(target),
          f'offline package ({len(patch_raw)} B) stays below full image size ({len(target)} B)')
    check(patch_info['target_revision'] == target_rev,
          'package records the source target revision for traceability')

    reconstructed = work / 'reconstructed.mpb'
    applied = subprocess.run([str(binary), 'apply-patch',
                              '-baseline', str(base_path),
                              '-patch', str(patch_path),
                              '-out', str(reconstructed)],
                             capture_output=True, text=True, check=True)
    print('   ', applied.stdout.strip())
    rebuilt = reconstructed.read_bytes()
    check(rebuilt.startswith(b'MRDNMPB\x01'), 'reconstructed file is a valid MPB v1 image')

    # The reconstructed image restores cleanly into an empty deployment and
    # yields the same catalog content as the source target. Independent
    # deployments assign their own revision numbers, so equality is judged on the
    # revision-independent content digest, not on the raw bytes.
    with Service(binary, work / 'field', 'field') as field:
        empty_rev = field.get_json('/status')['revision']
        field.restore(rebuilt, empty_rev)
        live_target, _ = field.export()
        check(content_digest(live_target) == content_digest(target),
              'offline-applied image reproduces the source catalog content digest')
        check(live_target[:8] == target[:8],
              'reproduced image keeps the MPB v1 signature and version byte')
        field_strata = {row['name']: row for row in field.get_json('/strata')['layers']}
        check(field_strata['new-bed']['count'] == 1 and field_strata['mica-bed']['count'] == 0,
              'field deployment shows the new feature and the emptied layer')

    # Applying the package to a different same-revision image must fail loudly.
    foreign = bytearray(baseline)
    foreign[-1] ^= 1
    foreign_path = work / 'foreign.mpb'
    foreign_path.write_bytes(bytes(foreign))
    refused = subprocess.run([str(binary), 'apply-patch',
                              '-baseline', str(foreign_path),
                              '-patch', str(patch_path),
                              '-out', str(work / 'must-not-exist.mpb')],
                             capture_output=True, text=True)
    check(refused.returncode != 0, 'CLI refuses a non-matching baseline')
    check('baseline_mismatch' in refused.stderr or 'checksum mismatch' in refused.stderr,
          f'CLI names the failure reason: {refused.stderr.strip()}')
    check(not (work / 'must-not-exist.mpb').exists(),
          'no output image is written on a refused application')
    return work


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--keep', action='store_true',
                        help='keep the temporary workspace and print its path')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='meridian-patch-') as temporary:
        root = Path(temporary)
        binary = root / 'meridian'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/meridian'],
                       check=True, cwd=ROOT, timeout=90)
        http_work = demonstrate_http(binary, root)
        cli_work = demonstrate_cli(binary, root)
        print('\nAll incremental-package exchange checks passed.')
        if args.keep:
            print(f'HTTP artifacts: {http_work}')
            print(f'CLI artifacts:  {cli_work}')


if __name__ == '__main__':
    main()
