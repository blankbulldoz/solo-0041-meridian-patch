#!/usr/bin/env python3
"""Rebuild a sample from its JSON payload and compare with the shipped image.

This proves the payload files alone are enough to reproduce the shipped
image: POST /strata with the layer definition, POST /edits with one batch of
400 inserts, then export and compare bytes with images/sample-X.mpb.  The
shipped image is the revision-normalized fixed point of that export, so the
comparison is done after one restore-export round trip.
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

HERE = Path(__file__).resolve().parent
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, status=200, headers=None):
        body = value
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            body = json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
            args['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=body, method=method, headers=args)
        try:
            response = urlopen(req, timeout=20)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            assert response.status == status, (method, path, response.status, status, raw[:512])
            payload = json.loads(raw) if response.headers.get_content_type() == 'application/json' else raw
            return payload, response.headers


def start(binary, data):
    child = subprocess.Popen([str(binary), '-listen', '127.0.0.1:0', '-data', str(data)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    readable, _, _ = select.select([child.stderr], [], [], 15)
    if not readable:
        child.kill()
        raise RuntimeError('service did not start within 15 seconds')
    startup = json.loads(child.stderr.readline())
    if 'address' not in startup:
        raise RuntimeError(startup)
    return child, Session('http://' + startup['address'])


def stop(child):
    if child.poll() is None:
        child.terminate()
    try:
        child.communicate(timeout=10)
    except subprocess.TimeoutExpired:
        child.kill()
        child.communicate(timeout=10)


def normalized(binary, raw, work, tag):
    data = work / f'normalize-{tag}'
    child, session = start(binary, data)
    try:
        session.request('PUT', '/image', raw, 200,
                        {'Content-Type': IMAGE, 'If-Meridian-Revision': '0'})
        return session.request('GET', '/image')[0]
    finally:
        stop(child)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--sample', choices=('a', 'b'), required=True)
    args = parser.parse_args()

    payload = json.loads((HERE / 'payloads' / f'sample-{args.sample}.json').read_text())
    shipped = (HERE / 'images' / f'sample-{args.sample}.mpb').read_bytes()
    work = Path(tempfile.mkdtemp(prefix='meridian-payload-check-'))
    child, session = start(Path(args.binary), work / 'raw')
    try:
        session.request('POST', '/strata', payload['layer'], 201)
        commit = session.request('POST', '/edits', dict(edits=payload['edits']), 200)[0]
        assert commit['revision'] == 2
        exported = session.request('GET', '/image')[0]
    finally:
        stop(child)

    rebuilt = normalized(Path(args.binary), exported, work, args.sample)
    print(json.dumps(dict(
        sample=args.sample,
        payload_edits=len(payload['edits']),
        exported_sha256=hashlib.sha256(exported).hexdigest(),
        rebuilt_sha256=hashlib.sha256(rebuilt).hexdigest(),
        shipped_sha256=hashlib.sha256(shipped).hexdigest(),
        rebuilt_matches_shipped=rebuilt == shipped,
    ), sort_keys=True))
    assert rebuilt == shipped


if __name__ == '__main__':
    main()
