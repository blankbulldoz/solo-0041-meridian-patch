#!/usr/bin/env python3
"""Import a shipped sample image and record the real index observations.

For each sample the script starts a fresh server on an empty directory,
imports images/sample-X.mpb with If-Meridian-Revision: 0, then runs every
window from windows.json and records status, index statistics, index audit
and per-window visit counters plus returned features.  Request bodies are
hashed so the two samples can be proven to share byte-identical requests.
"""
import argparse
import hashlib
import json
import select
import subprocess
import sys
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'

# Supplementary class-filtered probes: same bounds, same limit, class=halo.
# They show the class mix of the candidates without changing the main run.
CLASS_PROBES = ['halo']


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
            response = urlopen(req, timeout=15)
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


def sha(value):
    raw = json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
    return hashlib.sha256(raw).hexdigest()


def run(binary, sample, layer, windows, work):
    image = (HERE / 'images' / f'sample-{sample}.mpb').read_bytes()
    data = work / f'run-{sample}'
    result = dict(
        sample=sample,
        image=dict(file=f'images/sample-{sample}.mpb',
                   bytes=len(image), sha256=hashlib.sha256(image).hexdigest()),
    )
    child, session = start(binary, data)
    try:
        before, _ = session.request('GET', '/status')
        restored, headers = session.request('PUT', '/image', image, 200, {
            'Content-Type': IMAGE, 'If-Meridian-Revision': '0',
        })
        exported, _ = session.request('GET', '/image')
        summary, headers = session.request('GET', '/strata')
        inspect, _ = session.request('GET', f'/strata/{layer}/index')
        audit, _ = session.request('GET', f'/strata/{layer}/index-audit')
        result['status'] = dict(before=before, restored_revision=restored['revision'])
        result['import'] = dict(
            restored_layers=restored['layers'],
            export_is_byte_identical=exported == image,
            export_sha256=hashlib.sha256(exported).hexdigest(),
        )
        result['catalog'] = summary
        result['layer'] = next(item for item in summary['layers'] if item['name'] == layer)
        result['index'] = dict(tree=inspect['tree'],
                               revision=inspect['revision'],
                               layer_revision=inspect['layer_revision'])
        result['audit'] = dict(records=audit['records'], checked_nodes=audit['checked_nodes'],
                               revision=audit['revision'],
                               layer_revision=audit['layer_revision'])

        windows_out = []
        for window in windows:
            request = window['request']
            payload, _ = session.request('POST', f'/strata/{layer}/window', request)
            entry = dict(
                window=window['name'],
                request=request,
                request_sha256=sha(request),
                revision=payload['revision'],
                layer_revision=payload['layer_revision'],
                visit=payload['visit'],
                feature_ids=[row['id'] for row in payload['features']],
                feature_classes=sorted({row['class'] for row in payload['features']}),
                features=payload['features'],
            )
            assert 'next' not in payload, (window['name'], 'page truncated')
            windows_out.append(entry)
        result['windows'] = windows_out

        probes = []
        for window in windows:
            for class_name in CLASS_PROBES:
                request = dict(window['request'], **{'class': class_name})
                payload, _ = session.request('POST', f'/strata/{layer}/window', request)
                probes.append(dict(
                    window=window['name'], request=request, request_sha256=sha(request),
                    visit=payload['visit'],
                    feature_ids=[row['id'] for row in payload['features']],
                ))
        result['class_probes'] = probes
    finally:
        stop(child)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, help='path to the meridian binary')
    args = parser.parse_args()
    work = Path(__import__('tempfile').mkdtemp(prefix='meridian-index-run-'))
    params = json.loads((HERE / 'params.json').read_text())
    windows = json.loads((HERE / 'windows.json').read_text())
    for sample in ('a', 'b'):
        result = run(Path(args.binary), sample, params['layer']['name'], windows, work)
        (HERE / 'results' / f'sample-{sample}.json').write_text(
            json.dumps(result, indent=2, sort_keys=True) + '\n')
        print(json.dumps(dict(sample=sample,
                              import_ok=result['import']['export_is_byte_identical'],
                              features=result['layer']['count'],
                              candidates=sum(w['visit']['candidates'] for w in result['windows']),
                              hits=sum(len(w['feature_ids']) for w in result['windows'])),
                         sort_keys=True))
    sys.exit(0)


if __name__ == '__main__':
    main()
