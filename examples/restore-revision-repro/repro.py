#!/usr/bin/env python3
"""复现：旧备份恢复后图层修订变大、书签失效、二次恢复被拒。

场景角色：
  - 归档组：拿着 rev4 时导出的旧备份镜像
  - 在线库：备份后继续接受普通写入
  - 读客户端：在 alpha 层上翻页（书签）
  - 值守者：拿着恢复前的目标库条件（If-Meridian-Revision: 6）重试恢复
"""
import base64
import json
import select
import struct
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

BINARY = sys.argv[1] if len(sys.argv) > 1 else '/tmp/meridian-bin/meridian'
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
BOUNDS = dict(min_x=-20, min_y=-20, max_x=20, max_y=20)
WIN = dict(min_x=-20, min_y=-20, max_x=20, max_y=20)


def banner(text):
    print('\n' + '=' * 72)
    print(text)
    print('=' * 72)


def show(label, value):
    print(f'  {label}: {json.dumps(value, ensure_ascii=False, sort_keys=True)}')


class Session:
    def __init__(self, url):
        self.url = url

    def request(self, method, path, value=None, headers=None):
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            args['Content-Type'] = 'application/json'
        req = Request(self.url + PREFIX + path, data=value, method=method, headers=args)
        try:
            resp = urlopen(req, timeout=5)
        except HTTPError as e:
            resp = e
        with resp:
            raw = resp.read()
            body = json.loads(raw) if resp.headers.get_content_type() == 'application/json' else raw
            return resp.status, body, resp.headers

    def get(self, path):
        return self.request('GET', path)

    def post(self, path, value):
        return self.request('POST', path, value)

    def put_image(self, raw, revision):
        return self.request('PUT', '/image', raw,
                            {'Content-Type': IMAGE, 'If-Meridian-Revision': str(revision)})


def decode_mpb(raw):
    """MPB v1: 8B 签名 | 4B BE 长度 | 32B SHA-256 | 规范 JSON。"""
    assert raw[:8] == b'MRDNMPB\x01', raw[:8]
    (length,) = struct.unpack('>I', raw[8:12])
    payload = raw[44:44 + length]
    return json.loads(payload)


def decode_bookmark(token):
    """书签 = base64url(JSON payload) + '.' + base64url(HMAC)。payload 可读。"""
    body = token.split('.')[0]
    pad = '=' * (-len(body) % 4)
    return json.loads(base64.urlsafe_b64decode(body + pad))


def op(layer, fid, shape, mode='insert'):
    kind = 'mark' if 'x' in shape else 'extent'
    return dict(layer=layer, id=fid, mode=mode,
                value=dict(id=fid, **{'class': 'fixture'}, shape={kind: shape}))


def box(x0, y0, x1, y1):
    return dict(min_x=x0, min_y=y0, max_x=x1, max_y=y1)


def main():
    data = tempfile.mkdtemp(prefix='meridian-repro-')
    child = subprocess.Popen([BINARY, '-listen', '127.0.0.1:0', '-data', data],
                             stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        select.select([child.stderr], [], [], 10)
        startup = json.loads(child.stderr.readline())
        s = Session('http://' + startup['address'])
        print(f'server: {startup["address"]}  data: {data}')

        banner('A. 建库：alpha(extent) + beta(mark)，并写入地物')
        st, r, _ = s.post('/strata', dict(name='alpha', kind='extent', bounds=BOUNDS, exclusive=False))
        show(f'POST /strata alpha -> {st}', r)
        st, r, _ = s.post('/strata', dict(name='beta', kind='mark', bounds=BOUNDS, exclusive=False))
        show(f'POST /strata beta  -> {st}', r)
        st, r, _ = s.post('/edits', dict(edits=[
            op('alpha', 'alpha-01', box(0, 0, 2, 2)),
            op('alpha', 'alpha-02', box(2, 0, 4, 2)),
            op('alpha', 'alpha-03', box(4, 0, 6, 2)),
            op('alpha', 'alpha-04', box(6, 0, 8, 2)),
            op('alpha', 'alpha-05', box(8, 0, 10, 2)),
        ]))
        show(f'POST /edits alpha x5 -> {st}  (alpha 图层修订)', r)
        st, r, _ = s.post('/edits', dict(edits=[
            op('beta', 'beta-01', dict(x=1, y=1)),
            op('beta', 'beta-02', dict(x=3, y=1)),
        ]))
        show(f'POST /edits beta x2  -> {st}  (beta 图层修订)', r)

        banner('B. 归档组导出备份（旧备份诞生）')
        st, img, hdr = s.get('/image')
        backup_rev = int(hdr['Meridian-Revision'])
        print(f'  GET /image -> {st}, Meridian-Revision: {backup_rev}, {len(img)} 字节')
        snap = decode_mpb(img)
        show('镜像内记录的全库修订', snap['revision'])
        for l in snap['layers']:
            show(f"镜像内图层 {l['definition']['name']!r} 修订/条数",
                 {'revision': l['revision'], 'features': [f['id'] for f in l['features']]})
        Path('/tmp/repro/backup.mpb').write_bytes(img)
        print('  备份已存 /tmp/repro/backup.mpb')

        banner('C. 备份后在线库继续演进（beta 普通写入）')
        st, r, _ = s.post('/edits', dict(edits=[op('beta', 'beta-03', dict(x=5, y=1))]))
        show(f'POST /edits beta-03 -> {st}', r)
        st, r, _ = s.post('/edits', dict(edits=[op('beta', 'beta-04', dict(x=7, y=1))]))
        show(f'POST /edits beta-04 -> {st}', r)
        st, r, _ = s.get('/status')
        show(f'GET /status -> {st}', r)
        st, r, _ = s.get('/strata')
        show('GET /strata（备份后、恢复前）',
             {l['name']: {'revision': l['revision'], 'count': l['count']} for l in r['layers']})

        banner('D. 书签：异层普通改动后仍能续页（基线行为）')
        st, p1, _ = s.post('/strata/alpha/window', dict(bounds=WIN, limit=2))
        show(f'alpha 第1页 -> {st}  revision/layer_revision',
             {'revision': p1['revision'], 'layer_revision': p1['layer_revision'],
              'ids': [f['id'] for f in p1['features']]})
        bm1 = p1['next']
        show('书签1 scope（解码）', decode_bookmark(bm1))
        st, r, _ = s.post('/edits', dict(edits=[op('beta', 'beta-05', dict(x=9, y=1))]))
        show(f'异层写入 beta-05 -> {st}  (全库修订前进，alpha 图层修订不变)', r)
        st, p2, _ = s.post('/strata/alpha/window', dict(bounds=WIN, limit=2, bookmark=bm1))
        print(f'  持书签1续页 -> HTTP {st}（异层普通改动，书签照常可用）')
        show('  alpha 第2页', {'revision': p2['revision'], 'layer_revision': p2['layer_revision'],
                          'ids': [f['id'] for f in p2['features']]})
        bm2 = p2['next']
        show('书签2 scope（解码，恢复后要用它）', decode_bookmark(bm2))
        # beta 层也取一张书签，用于观察受影响范围
        st, pb, _ = s.post('/strata/beta/window', dict(bounds=WIN, limit=1))
        bm_beta = pb['next']
        show('beta 书签 scope（解码）', decode_bookmark(bm_beta))

        banner('E. 归档组用旧备份执行恢复（第一次恢复，成功）')
        pre_restore_rev = s.get('/status')[1]['revision']
        print(f'  恢复前目标库全库修订 = {pre_restore_rev}（值守者记下这个条件）')
        st, r, _ = s.put_image(img, pre_restore_rev)
        print(f'  PUT /image If-Meridian-Revision: {pre_restore_rev} -> HTTP {st}')
        show('恢复响应（注意每个图层的 revision）', r)
        st, strata, _ = s.get('/strata')
        show('GET /strata（恢复后）',
             {l['name']: {'revision': l['revision'], 'count': l['count']} for l in strata['layers']})
        print('  对照：备份镜像里记的图层修订是 alpha=3, beta=4；'
              '恢复后全部变成新全库修订 —— “数字全比原来的大”')
        st, p, _ = s.post('/strata/alpha/window', dict(bounds=WIN, limit=256))
        show('恢复后 alpha 全量窗口读',
             {'revision': p['revision'], 'layer_revision': p['layer_revision'],
              'ids': [f['id'] for f in p['features']]})
        show('备份镜像里 alpha 的记录', [f['id'] for f in snap['layers'][0]['features']])
        st, pb2, _ = s.post('/strata/beta/window', dict(bounds=WIN, limit=256))
        show('恢复后 beta 全量窗口读（备份后写入的 beta-03/04/05 已不在）',
             [f['id'] for f in pb2['features']])
        st, img2, hdr2 = s.get('/image')
        snap2 = decode_mpb(img2)
        show('恢复后再导出镜像的修订',
             {'image_revision': snap2['revision'],
              'layers': {l['definition']['name']: l['revision'] for l in snap2['layers']}})

        banner('F. 这次书签不能继续了')
        st, r, _ = s.post('/strata/alpha/window', dict(bounds=WIN, limit=2, bookmark=bm2))
        print(f'  持书签2续页 -> HTTP {st}')
        show('响应', r)
        st, r, _ = s.post('/strata/beta/window', dict(bounds=WIN, limit=1, bookmark=bm_beta))
        print(f'  beta 书签续页 -> HTTP {st}（连内容变过的层也一样，波及所有层）')
        show('响应', r)

        banner('G. 值守者拿着恢复前的目标库条件二次恢复，被拒')
        st, r, _ = s.put_image(img, pre_restore_rev)
        print(f'  PUT /image If-Meridian-Revision: {pre_restore_rev} -> HTTP {st}')
        show('响应', r)
        cur = s.get('/status')[1]['revision']
        st, r, _ = s.put_image(img, cur)
        print(f'  对照：刷新条件后 PUT /image If-Meridian-Revision: {cur} -> HTTP {st}')
        show('响应（修订再次整体重排）', r)

        banner('H. 受影响范围汇总观测')
        st, strata, _ = s.get('/strata')
        show('最终 GET /strata',
             {l['name']: {'revision': l['revision'], 'count': l['count']} for l in strata['layers']})
        print(f'  备份后、恢复前在线库曾有 5 个 beta 点；备份镜像只有 2 个；'
              f'恢复后 beta 只剩镜像里的 2 个。')
    finally:
        child.terminate()
        child.communicate(timeout=5)


if __name__ == '__main__':
    main()
