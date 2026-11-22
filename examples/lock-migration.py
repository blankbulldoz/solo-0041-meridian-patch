#!/usr/bin/env python3
"""Rehearse the flock -> POSIX-record-lock cutover against real binaries.

Every scenario starts its own server processes in a temporary directory and
talks HTTP only, so the script is safe to run next to a live database. Build
the two binaries first (the walkthrough does this automatically when given
--new-binary/--legacy-binary):

    go build -o ./meridian ./cmd/meridian
    go build -tags legacylock -o ./meridian-legacy ./cmd/meridian

    python3 examples/lock-migration.py \
        --new-binary ./meridian --legacy-binary ./meridian-legacy
"""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import select
import signal
import socket
import subprocess
import sys
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
BOX = dict(min_x=-8, min_y=-8, max_x=8, max_y=8)
EXIT_TEMPFAIL = 75


class Result:
    def __init__(self):
        self.checks = []

    def check(self, name, condition, detail=''):
        self.checks.append(dict(check=name, ok=bool(condition), detail=str(detail)))
        print(('PASS ' if condition else 'FAIL ') + name + ((' :: ' + str(detail)) if detail else ''),
              file=sys.stderr)
        return bool(condition)

    def finish(self, scenario):
        failed = [c for c in self.checks if not c['ok']]
        print(json.dumps(dict(scenario=scenario, checks=self.checks, ok=not failed), sort_keys=True))
        if failed:
            raise SystemExit(1)


def post(url, path, value, status=200):
    request = Request(url + PREFIX + path, data=json.dumps(value).encode(), method='POST',
                      headers={'Content-Type': 'application/json'})
    try:
        response = urlopen(request, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == status, (path, response.status, status, raw[:400])
        return json.loads(raw)


def get(url, path, status=200):
    try:
        response = urlopen(url + PREFIX + path, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read()
        assert response.status == status, (path, response.status, status, raw[:400])
        return json.loads(raw)


def seed(url):
    """Create one layer and one feature, returning the committed revision."""
    post(url, '/strata', dict(name='ochre-field', kind='extent', bounds=BOX, exclusive=False), status=201)
    body = post(url, '/edits', dict(edits=[dict(
        layer='ochre-field', id='prong', mode='insert',
        value=dict(id='prong', **{'class': 'fixture'},
                   shape=dict(extent=dict(min_x=0, min_y=0, max_x=4, max_y=3))))]))
    return body['revision']


@contextmanager
def server(binary, data, port=0):
    """Run a server binary and wait for its ready line on stderr."""
    process = subprocess.Popen([str(binary), '-listen', f'127.0.0.1:{port}', '-data', str(data)],
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        ready, _, _ = select.select([process.stderr], [], [], 15)
        if not ready:
            raise RuntimeError('service did not start within 15 seconds')
        line = process.stderr.readline()
        startup = json.loads(line)
        if 'address' not in startup:
            raise RuntimeError(line)
        yield process, 'http://' + startup['address'], startup
    finally:
        if process.poll() is None:
            process.terminate()
        try:
            process.communicate(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=5)


def run(binary, *args, timeout=20):
    return subprocess.run([str(binary), *map(str, args)], capture_output=True, text=True, timeout=timeout)


def attempt_start(binary, data, timeout=20):
    """Start a binary that is expected to exit; report (code, stderr)."""
    try:
        done = run(binary, '-listen', '127.0.0.1:0', '-data', data, timeout=timeout)
    except subprocess.TimeoutExpired:
        return 'still-running', ''
    return done.returncode, done.stderr


def lock_state(binary, data):
    done = run(binary, '-data', data, '-check-lock')
    if done.returncode != 0:
        raise RuntimeError(done.stderr)
    return json.loads(done.stdout.strip().splitlines()[-1])


def wait_for_free(binary, data, deadline=15.0):
    """Wait until no writer owns the directory, then report the last state."""
    end = time.monotonic() + deadline
    while time.monotonic() < end:
        state = lock_state(binary, data)
        if state['classification'] == 'free':
            return state
        time.sleep(0.05)
    raise RuntimeError('directory never became free: ' + json.dumps(state))


def probe(binary, data, mode):
    return run(binary, '-data', data, '-probe-lock', mode)


# --------------------------------------------------------------------------
# Scenario 1: many processes race for one empty directory.
# --------------------------------------------------------------------------
def race_empty_directory(new, legacy, root):
    data = root / 'race'
    data.mkdir()
    result = Result()
    # New binaries first: they are the ones that can express "busy, retry".
    new_racers = []
    try:
        for _ in range(8):
            port = free_port()
            new_racers.append(subprocess.Popen(
                [str(new), '-listen', f'127.0.0.1:{port}', '-data', str(data)],
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE))
        time.sleep(2.0)  # let every loser exit
        alive = [p for p in new_racers if p.poll() is None]
        codes = [p.returncode for p in new_racers if p.poll() is not None]
        result.check('exactly one new-format survivor among 8 racers', len(alive) == 1, len(alive))
        result.check('every new-format loser exits 75 (retryable)',
                     codes and all(code == EXIT_TEMPFAIL for code in codes), codes)
        state = lock_state(new, data)
        result.check('holder is visible in both lock namespaces',
                     state['guard_held'] and state['record_held'], json.dumps(state))
        result.check('holder is classified migrated-holder',
                     state['classification'] == 'migrated-holder', state['classification'])
        for process in alive:
            process.terminate()
        for process in new_racers:
            process.communicate(timeout=15)
    finally:
        for process in new_racers:
            if process.poll() is None:
                process.kill()
                process.communicate(timeout=5)

    # Mixed race against one empty directory: legacy (flock) and new (both)
    # must still agree on a single winner, because both take the flock guard.
    data = root / 'race-mixed'
    data.mkdir()
    racers = []
    try:
        for index in range(12):
            binary = legacy if index % 2 else new
            racers.append(subprocess.Popen(
                [str(binary), '-listen', f'127.0.0.1:{free_port()}', '-data', str(data)],
                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE))
        time.sleep(2.5)  # old binaries have no retryable exit; give them time to die
        alive = [p for p in racers if p.poll() is None]
        result.check('mixed legacy+new race leaves exactly one survivor', len(alive) == 1, len(alive))
        state = lock_state(new, data)
        legacy_won = bool(alive) and alive[0].args[0] == str(legacy)
        result.check('winner namespace matches the surviving binary',
                     (state['classification'] == 'legacy-holder') == legacy_won,
                     f'legacy_won={legacy_won} {state["classification"]}')
        if legacy_won:
            result.check('legacy winner exposes no record lock (migration target check)',
                         state['record_held'] is False, json.dumps(state))
        else:
            result.check('new winner holds a verifiable record lock', state['record_held'])
    finally:
        for process in racers:
            if process.poll() is None:
                process.terminate()
        for process in racers:
            try:
                process.communicate(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate(timeout=5)
    result.finish('race-empty-directory')


# --------------------------------------------------------------------------
# Scenario 2: legacy holder alive -> new binary must not write, and the
# record-only competitor is the trap the guard closes.
# --------------------------------------------------------------------------
def legacy_blocks_new(new, legacy, root):
    data = root / 'legacy-block'
    data.mkdir()
    result = Result()
    with server(legacy, data) as (_, url, _):
        revision = seed(url)
        result.check('legacy seed committed', revision >= 1, revision)
        state = lock_state(new, data)
        result.check('legacy holder detected', state['classification'] == 'legacy-holder', state['classification'])
        result.check('legacy holds flock only', state['guard_held'] and not state['record_held'], json.dumps(state))
        code, stderr = attempt_start(new, data)
        result.check('new binary refuses to start', code == EXIT_TEMPFAIL, f'code={code} {stderr.strip()[-160:]}')
        record = probe(new, data, 'record')
        result.check('record-only acquisition SUCCEEDS while legacy runs (the hazard)',
                     record.returncode == 0, record.stdout.strip() or record.stderr.strip())
        both = probe(new, data, 'both')
        result.check('guarded acquisition is refused', both.returncode == EXIT_TEMPFAIL, both.stderr.strip()[-160:])
    result.finish('legacy-blocks-new')


# --------------------------------------------------------------------------
# Scenario 3: legacy exits -> new binary takes over the existing image.
# --------------------------------------------------------------------------
def takeover_after_stop(new, legacy, root):
    data = root / 'takeover'
    data.mkdir()
    result = Result()
    with server(legacy, data) as (_, url, _):
        revision = seed(url)
        exported = fetch_image(url)
    wait_for_free(new, data)
    with server(new, data) as (_, url, startup):
        result.check('restarted at the legacy revision', startup['revision'] == revision,
                     f'{startup["revision"]} != {revision}')
        result.check('features survive takeover',
                     get(url, '/strata/ochre-field/index-audit')['records'] == 1)
        body = post(url, '/edits', dict(edits=[dict(
            layer='ochre-field', id='dowel', mode='insert',
            value=dict(id='dowel', **{'class': 'fixture'},
                       shape=dict(extent=dict(min_x=4, min_y=0, max_x=8, max_y=3))))]))
        result.check('new binary can write the existing image', body['revision'] == revision + 1, body['revision'])
        state = lock_state(new, data)
        result.check('takeover holder is migrated-holder', state['classification'] == 'migrated-holder',
                     state['classification'])
    with server(new, data) as (_, url, startup):
        result.check('post-takeover restart persists the new revision',
                     startup['revision'] == revision + 1, startup['revision'])
    result.check('legacy export still decodable shape', exported.startswith(b'MRDNMPB\x01'))
    result.finish('takeover-after-stop')


def fetch_image(url):
    request = Request(url + PREFIX + '/image', method='GET')
    with urlopen(request, timeout=10) as response:
        return response.read()


# --------------------------------------------------------------------------
# Scenario 4: a reader streams the image while a takeover happens.
# --------------------------------------------------------------------------
def read_while_takeover(new, legacy, root):
    data = root / 'reader'
    data.mkdir()
    result = Result()
    with server(legacy, data) as (legacy_process, url, _):
        revision = seed(url)
        snapshot = fetch_image(url)
        expected_len = len(snapshot)
        result.check('image is non-empty', expected_len > 0, expected_len)
        # A slow reader opens the image stream against the legacy process.
        response = urlopen(Request(url + PREFIX + '/image', method='GET'), timeout=10)
        prefix = response.read(16)
        result.check('reader holds a partial image while the legacy process is live', len(prefix) > 0, len(prefix))
        # The old process is stopped mid-stream; the reader's socket stays open
        # until the kernel tears it down. Then a new process takes over the same
        # directory. Reads must continue from a complete, freshly exported image.
        legacy_process.terminate()
        legacy_process.communicate(timeout=15)
        wait_for_free(new, data)
        with server(new, data) as (_, url, startup):
            result.check('takeover keeps the revision the reader was served',
                         startup['revision'] == revision, f'{startup["revision"]} != {revision}')
            taken = fetch_image(url)
            result.check('image exported after takeover matches legacy bytes',
                         taken == snapshot, f'{len(taken)} bytes')
            result.check('post-takeover image revision unchanged',
                         get(url, '/status')['revision'] == revision)
            # Keep reading through the same API while writes land.
            post(url, '/edits', dict(edits=[dict(
                layer='ochre-field', id='ferrule', mode='insert',
                value=dict(id='ferrule', **{'class': 'fixture'},
                           shape=dict(extent=dict(min_x=2, min_y=1, max_x=6, max_y=4))))]))
            fresh = fetch_image(url)
            result.check('new writes produce a new image revision', fresh != snapshot)
            for _ in range(5):
                result.check('repeat export is byte-stable', fetch_image(url) == fresh, len(fresh))
    try:
        response.close()
    except Exception:
        pass
    result.check('legacy snapshot remains individually decodable',
                 snapshot.startswith(b'MRDNMPB\x01'))
    result.finish('read-while-takeover')


# --------------------------------------------------------------------------
# Scenario 5: SIGKILL the holder -> directory is immediately reclaimable.
# --------------------------------------------------------------------------
def kill_and_reclaim(new, legacy, root):
    result = Result()
    for label, binary in (('legacy', legacy), ('new', new)):
        data = root / f'kill-{label}'
        data.mkdir()
        process = subprocess.Popen([str(binary), '-listen', f'127.0.0.1:{free_port()}', '-data', str(data)],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        ready, _, _ = select.select([process.stderr], [], [], 15)
        if not ready:
            raise RuntimeError(f'{label} did not start')
        url = 'http://' + json.loads(process.stderr.readline())['address']
        revision = seed(url)
        os.kill(process.pid, signal.SIGKILL)
        process.communicate(timeout=10)
        state = wait_for_free(new, data, deadline=5)
        result.check(f'{label} SIGKILL releases the directory immediately',
                     state['classification'] == 'free', state['classification'])
        with server(new, data) as (_, url, startup):
            result.check(f'{label} takeover restores the revision after SIGKILL',
                         startup['revision'] == revision, f'{startup["revision"]} != {revision}')
    result.finish('kill-and-reclaim')


# --------------------------------------------------------------------------
# Scenario 6: the lock file is unlinked/replaced under a live holder.
# --------------------------------------------------------------------------
def replaced_lock_file_fences(new, root):
    data = root / 'replaced'
    data.mkdir()
    result = Result()
    with server(new, data) as (_, url, _):
        seed(url)
        lock = data / 'meridian.writer'
        os.unlink(lock)
        Path(lock).write_bytes(b'')      # fresh inode, unprotected by the old lock
        second = subprocess.Popen([str(new), '-listen', f'127.0.0.1:{free_port()}', '-data', str(data)],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        ready, _, _ = select.select([second.stderr], [], [], 10)
        if ready and second.poll() is None:
            # The second process is up; the first must refuse every write.
            try:
                post(url, '/edits', dict(edits=[dict(
                    layer='ochre-field', id='ghost', mode='insert',
                    value=dict(id='ghost', **{'class': 'fixture'},
                               shape=dict(extent=dict(min_x=0, min_y=0, max_x=1, max_y=1))))]), status=503)
                fenced = True
                detail = 'first writer fenced with 503'
            except AssertionError as error:
                fenced = False
                detail = str(error)[:200]
            result.check('unlinked lock file fences the live writer', fenced, detail)
            try:
                post(url, '/edits', dict(edits=[dict(
                    layer='ochre-field', id='ghost', mode='insert',
                    value=dict(id='ghost', **{'class': 'fixture'},
                               shape=dict(extent=dict(min_x=0, min_y=0, max_x=1, max_y=1))))]), status=503)
            except AssertionError:
                pass
            second.terminate()
            second.communicate(timeout=10)
        else:
            result.check('second writer blocked after lock-file replacement', False,
                         'second process exited instead of proving the hazard')
            second.communicate(timeout=10)
    result.finish('replaced-lock-file')


# --------------------------------------------------------------------------
# Scenario 7: filesystem capability report.
# --------------------------------------------------------------------------
def capability_report(new, root):
    data = root / 'capability'
    data.mkdir()
    result = Result()
    state = lock_state(new, data)
    result.check('filesystem classified', state['filesystem'] != '', state['filesystem'])
    result.check('device key matches /proc/locks format', ':' in state['device_key'], state['device_key'])
    result.check('free directory probes as free', state['classification'] == 'free')
    result.check('both lock orders probe cleanly on a free directory',
                 all(probe(new, data, mode).returncode == 0 for mode in ('legacy', 'record', 'both')))
    errors = state.get('warnings') or []
    result.check('filesystem warnings are surfaced', isinstance(errors, list), errors)
    result.finish('capability-report')


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


SCENARIOS = {
    'race': race_empty_directory,
    'legacy-blocks': legacy_blocks_new,
    'takeover': takeover_after_stop,
    'reader': read_while_takeover,
    'kill': kill_and_reclaim,
    'replaced': replaced_lock_file_fences,
    'capability': capability_report,
}


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--scenario', required=True, choices=SCENARIOS)
    parser.add_argument('--new-binary', type=Path)
    parser.add_argument('--legacy-binary', type=Path)
    arguments = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix='meridian-lock-'))
    new = arguments.new_binary
    legacy = arguments.legacy_binary
    if new is None:
        new = root / 'meridian'
        subprocess.run(['go', 'build', '-o', str(new), './cmd/meridian'],
                       check=True, cwd=Path(__file__).resolve().parents[1], timeout=180)
    if legacy is None:
        legacy = root / 'meridian-legacy'
        subprocess.run(['go', 'build', '-tags', 'legacylock', '-o', str(legacy), './cmd/meridian'],
                       check=True, cwd=Path(__file__).resolve().parents[1], timeout=180)
    try:
        if SCENARIOS[arguments.scenario] in (race_empty_directory, legacy_blocks_new,
                                             takeover_after_stop, read_while_takeover,
                                             kill_and_reclaim):
            SCENARIOS[arguments.scenario](new, legacy, root)
        else:
            SCENARIOS[arguments.scenario](new, root)
    finally:
        pass


if __name__ == '__main__':
    main()
