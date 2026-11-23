#!/usr/bin/env python3
"""Two-instance Meridian takeover drill on one machine.

The old and the new instance use the same data directory, just as an upgrade
would. The drill proves, in order:

1. writer exclusivity - while the old instance keeps writing, a new instance
   cannot acquire the directory and never binds an HTTP listener;
2. gated takeover - after the old instance stops, the new instance announces
   readiness only when image verification, per-layer index audit and a real
   locate probe all pass;
3. fault classification - a busy lock, a corrupt image and a probe/catalog
   disagreement each stop startup with a distinct stage;
4. rollback gates - when a file-level swap is safe, and when only a CAS image
   restore is safe.

Every child process is kept in its own log file and every drill step appends a
JSON event to the drill log. Use --old-binary/--new-binary to drill a real
version upgrade; by default both roles use one build of the current source.
"""
import argparse
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
PREFIX = '/meridian/v1'
IMAGE = 'application/vnd.meridian.image'
STATE = 'meridian-state.mpb'
LOCK = 'meridian.writer'

FIELD, BED, PINS, DRIFT = 'ochre-field', 'mica-bed', 'plumb-marks', 'drift-bed'
BOUNDS = dict(min_x=-24, min_y=-24, max_x=24, max_y=24)
PROBE_POINT = (4, 1)
PROBE_LAYER = FIELD
PROBE_EXPECT = ['dowel', 'ferrule']

LOCK_BUSY = 'LOCK_BUSY'
IMAGE_CORRUPT = 'IMAGE_CORRUPT'
PROBE_MISMATCH = 'PROBE_MISMATCH'
IMAGE_DRIFT = 'IMAGE_DRIFT'
READY = 'READY'


class DrillFailure(AssertionError):
    pass


def check(condition, message):
    if not condition:
        raise DrillFailure(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def short(value):
    return value[:16] if isinstance(value, str) else value


def image_signature(raw):
    """Digest of the image payload with revision fields removed.

    Two images with the same signature hold the same layers and features; only
    their database and layer revision numbers differ.
    """
    payload = json.loads(raw[44:])
    payload['revision'] = 0
    for layer in payload['layers']:
        layer['revision'] = 0
    return digest(json.dumps(payload, sort_keys=True, separators=(',', ':')).encode())


class Session:
    def __init__(self, base_url):
        self.base_url = base_url

    def request(self, method, path, value=None, status=200, headers=None):
        args = dict(headers or {})
        if isinstance(value, (dict, list)):
            value = json.dumps(value).encode()
            args['Content-Type'] = 'application/json'
        request = Request(self.base_url + PREFIX + path, data=value, method=method, headers=args)
        try:
            response = urlopen(request, timeout=10)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            check(response.status == status,
                  '%s %s returned %d, wanted %d: %r' % (method, path, response.status, status, raw[:300]))
            content_type = response.headers.get_content_type()
            body = json.loads(raw) if content_type == 'application/json' else raw
            return body, response.headers

    def get(self, path, status=200):
        return self.request('GET', path, status=status)[0]

    def post(self, path, value, status=200):
        return self.request('POST', path, value, status=status)[0]

    def status(self):
        return self.get('/status')

    def edit(self, *items, status=200, revision=None):
        request = dict(edits=list(items))
        if revision is not None:
            request['if_revision'] = revision
        return self.post('/edits', request, status=status)

    def probe(self):
        return self.post('/strata/%s/locate' % PROBE_LAYER,
                         dict(x=PROBE_POINT[0], y=PROBE_POINT[1]))

    def image(self):
        raw, headers = self.request('GET', '/image')
        return raw, int(headers['Meridian-Revision'])

    def restore(self, raw, revision, status=200):
        return self.request('PUT', '/image', raw, status, {
            'Content-Type': IMAGE, 'If-Meridian-Revision': str(revision),
        })[0]


def feature(layer, identifier, shape, category='fixture'):
    kind = 'mark' if 'x' in shape else 'extent'
    item = dict(layer=layer, id=identifier, mode='insert')
    item['value'] = dict(id=identifier, **{'class': category}, shape={kind: shape})
    return item


class Instance:
    """One Meridian process pinned to the shared data directory."""

    def __init__(self, drill, label, binary, data, probe=None, evidence=True):
        self.drill = drill
        self.label = label
        self.binary = Path(binary)
        self.data = Path(data)
        self.probe = probe
        self.evidence = evidence
        self.child = None
        self.records = []
        self.address = None
        self.log_path = drill.logs / ('%s.stderr.jsonl' % label)
        self.report_path = drill.logs / ('%s.readiness.json' % label)
        self._ready = threading.Event()
        self._exited = threading.Event()

    def start(self):
        check(self.child is None or self.child.poll() is not None,
              '%s is already running; stop it before restarting' % self.label)
        args = [str(self.binary), '-listen', '127.0.0.1:0', '-data', str(self.data)]
        if self.probe:
            args += ['-probe', self.probe, '-require-probe']
        self.records = []
        self.address = None
        self._ready = threading.Event()
        self._exited = threading.Event()
        if self.evidence:
            args += ['-readiness-report', str(self.report_path)]
            self.report_path.unlink(missing_ok=True)
        self.drill.log('instance', 'start', role=self.label, binary=str(self.binary),
                       data=str(self.data), probe=self.probe, argv=args)
        self.child = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                                      text=True, bufsize=1)
        thread = threading.Thread(target=self._pump, daemon=True)
        thread.start()
        return self

    def _pump(self):
        process = self.child
        with open(self.log_path, 'a', encoding='utf-8') as log:
            for line in process.stderr:
                line = line.rstrip('\n')
                log.write(line + '\n')
                log.flush()
                try:
                    record = json.loads(line)
                except json.JSONDecodeError:
                    record = {'raw': line}
                self.records.append(record)
                if 'address' in record:
                    self.address = record['address']
                    self._ready.set()
        self._exited.set()

    def wait_ready(self, timeout=20):
        check(self._ready.wait(timeout),
              '%s did not announce an address within %ss; log: %s' % (self.label, timeout, self.log_path))
        return self.startup_record()

    def wait_exit(self, timeout=20):
        check(self._exited.wait(timeout), '%s did not exit within %ss' % (self.label, timeout))
        return self.child.wait(timeout=timeout)

    def wait_restart_ready(self, timeout=20):
        check(self._ready.wait(timeout),
              '%s did not announce an address within %ss after restart; log: %s'
              % (self.label, timeout, self.log_path))
        return self.startup_record()

    def startup_record(self):
        for record in self.records:
            if record.get('msg') == 'meridian ready':
                return record
        raise DrillFailure('%s logged an address without a ready record' % self.label)

    def readiness_report(self):
        check(self.report_path.exists(), '%s did not write %s' % (self.label, self.report_path))
        return json.loads(self.report_path.read_text())

    def gate_record(self):
        for record in self.records:
            if record.get('msg') == 'meridian readiness gates passed':
                return record
        return None

    def failure_record(self):
        for record in self.records:
            if record.get('msg') == 'meridian not ready':
                return record
        return None

    def stop(self, timeout=25):
        if self.child is None or self.child.poll() is not None:
            return 0
        self.drill.log('instance', 'signal', role=self.label, signal='SIGTERM')
        self.child.send_signal(signal.SIGTERM)
        code = self.wait_exit(timeout)
        self.drill.log('instance', 'exit', role=self.label, code=code)
        return code

    def kill(self):
        if self.child is not None and self.child.poll() is None:
            self.child.kill()
            self.child.wait(timeout=10)


class Drill:
    def __init__(self, workdir, logs):
        self.workdir = Path(workdir)
        self.logs = Path(logs)
        self.events_path = self.logs / 'takeover-events.jsonl'
        self.classifications = []
        self.rollback_gates = []
        self.instances = []

    def log(self, phase, event, **detail):
        record = {'ts': time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime()), 'phase': phase, 'event': event}
        record.update(detail)
        with open(self.events_path, 'a', encoding='utf-8') as handle:
            handle.write(json.dumps(record, sort_keys=True) + '\n')
        keys = ' '.join('%s=%s' % (k, detail[k]) for k in sorted(detail))
        print('  [%s] %-9s %s %s' % (phase, event, keys, ''))
        return record

    def classify(self, kind, detail):
        self.classifications.append(dict(kind=kind, **detail))
        self.log('classify', kind, **detail)

    def gate(self, name, rule, outcome):
        self.rollback_gates.append(dict(name=name, rule=rule, outcome=outcome))
        self.log('rollback', name, rule=rule, outcome=outcome)

    def instance(self, label, binary, data, probe=None, evidence=True):
        instance = Instance(self, label, binary, data, probe, evidence)
        self.instances.append(instance)
        return instance


def wait_for(predicate, timeout, message):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.05)
    raise DrillFailure(message)


def seed(session):
    session.post('/strata', dict(name=FIELD, kind='extent', bounds=BOUNDS, exclusive=False), 201)
    session.post('/strata', dict(name=BED, kind='extent', bounds=BOUNDS, exclusive=False), 201)
    session.post('/strata', dict(name=PINS, kind='mark', bounds=BOUNDS, exclusive=True), 201)
    session.post('/strata', dict(name=DRIFT, kind='extent', bounds=BOUNDS, exclusive=False), 201)
    session.edit(
        feature(FIELD, 'prong', dict(min_x=0, min_y=0, max_x=4, max_y=3)),
        feature(FIELD, 'dowel', dict(min_x=4, min_y=0, max_x=8, max_y=3)),
        feature(FIELD, 'ferrule', dict(min_x=2, min_y=1, max_x=6, max_y=4), 'sleeve'),
        feature(BED, 'shim', dict(min_x=3, min_y=0, max_x=5, max_y=2)),
        feature(PINS, 'brad', dict(x=4, y=1)),
        feature(PINS, 'stud', dict(x=6, y=1)),
    )


def background_writer(session, stop, counters):
    """Keep the old instance writing for the whole exclusivity phase."""
    index = 0
    while not stop.is_set():
        items = []
        for _ in range(8):
            index += 1
            x = -20 + (index * 3) % 36
            y = -20 + (index * 5) % 36
            items.append(feature(DRIFT, 'drift-%05d' % index,
                                 dict(min_x=x, min_y=y, max_x=x + 2, max_y=y + 2)))
        try:
            result = session.edit(*items)
        except Exception as error:  # the old writer must stay healthy
            counters['errors'].append(repr(error))
            return
        counters['writes'] += 1
        counters['revision'] = result['revision']
        time.sleep(0.02)


def probe_spec(layer, point, expected):
    return '%s,%d,%d,%s' % (layer, point[0], point[1], '|'.join(expected))


def load_snapshot(session):
    revision = session.status()['revision']
    image, image_revision = session.image()
    check(image_revision == revision, 'image revision %d does not match status revision %d'
          % (image_revision, revision))
    strata = session.get('/strata')
    located = session.probe()
    observed = sorted(row['id'] for row in located['features'])
    audits = {}
    for layer in strata['layers']:
        audit = session.get('/strata/%s/index-audit' % layer['name'])
        audits[layer['name']] = dict(records=audit['records'], checked_nodes=audit['checked_nodes'],
                                     revision=audit['layer_revision'])
    return dict(revision=revision, image=image, image_revision=image_revision,
                digest=digest(image), strata=strata, observed=observed, audits=audits)


def phase_exclusivity(drill, old, session, snapshot):
    """A new instance must not become writer while the old one keeps writing."""
    print('\nPhase 1 - writer exclusivity while the old instance is writing')
    stop = threading.Event()
    counters = dict(writes=0, revision=snapshot['revision'], errors=[])
    writer = threading.Thread(target=background_writer, args=(session, stop, counters), daemon=True)
    writer.start()
    start_revision = session.status()['revision']
    attempts = []
    try:
        wait_for(lambda: counters['writes'] >= 2, 15,
                 'background writer did not commit while the old instance started')
        for attempt in range(3):
            before = session.status()['revision']
            contender = drill.instance('contender-%d' % (attempt + 1), drill.new_binary, drill.data,
                                       probe=probe_spec(PROBE_LAYER, PROBE_POINT, snapshot['observed']))
            started = time.monotonic()
            contender.start()
            code = contender.wait_exit(20)
            elapsed = round(time.monotonic() - started, 3)
            check(code == STAGE_EXIT['lock'],
                  'contender %d exited %d, expected %d (lock) while the old writer held the lock: %s'
                  % (attempt + 1, code, STAGE_EXIT['lock'], contender.log_path))
            check(contender.address is None, 'contender %d bound a listener while the old writer held the lock'
                  % (attempt + 1))
            check(contender.gate_record() is None, 'contender %d passed readiness gates while the lock was held'
                  % (attempt + 1))
            failure = contender.failure_record()
            check(failure is not None, 'contender %d exited without a readiness failure record' % (attempt + 1))
            check(failure.get('stage') == 'lock',
                  'contender %d failed at stage %r, expected lock: %s'
                  % (attempt + 1, failure.get('stage'), failure.get('error')))
            check('acquire data directory' in str(failure.get('error', '')),
                  'contender %d lock failure did not name the directory lock: %s'
                  % (attempt + 1, failure.get('error')))
            after = session.status()
            check(after['writable'], 'old writer stopped being writable after a takeover attempt')
            check(after['revision'] >= before,
                  'old writer revision went backwards after a takeover attempt')
            attempts.append(dict(attempt=attempt + 1, exit=code, stage=failure['stage'],
                                 seconds=elapsed,
                                 old_revision_before=before, old_revision_after=after['revision'],
                                 error=failure['error']))
            drill.log('exclusivity', 'contender-rejected', attempt=attempt + 1, exit=code,
                      stage=failure['stage'], seconds=elapsed, old_revision=after['revision'])
    finally:
        stop.set()
        writer.join(timeout=10)
    check(not counters['errors'], 'background writer failed: %s' % counters['errors'][:2])
    check(counters['writes'] >= 2, 'old writer committed only %d writes during the phase' % counters['writes'])
    foreground = session.edit(feature(BED, 'review-mark', dict(min_x=-2, min_y=-2, max_x=-1, max_y=-1)))
    check(foreground['revision'] > start_revision,
          'old writer could not commit a foreground edit after the takeover attempts')
    final_revision = session.status()['revision']
    check(final_revision > start_revision,
          'old writer did not advance its revision during the exclusivity phase')
    drill.log('exclusivity', 'old-writer-kept-writing', writes=counters['writes'],
              start_revision=start_revision, final_revision=final_revision,
              foreground_revision=foreground['revision'])
    drill.classify(LOCK_BUSY, dict(stage='lock', attempts=len(attempts), exit=attempts[0]['exit'],
                                   seconds=attempts[0]['seconds'], old_writer_revision=final_revision,
                                   detail='meridian.writer flock held; no listener bound'))
    return attempts, final_revision


def phase_takeover(drill, old, new, snapshot, data):
    print('\nPhase 2 - gated takeover after the old instance stops')
    code = old.stop()
    check(code == 0, 'old instance exited %d on SIGTERM' % code)
    old_pid_lock = data / LOCK
    check(old_pid_lock.exists(), 'writer lock file disappeared after the old instance stopped')

    new.probe = probe_spec(PROBE_LAYER, PROBE_POINT, snapshot['observed'])
    new.start()
    startup = new.wait_ready()
    gates = new.gate_record()
    check(gates is not None, 'new instance never logged readiness gates')

    # The lock must move with the writer, not disappear: a second contender is
    # refused now that the new instance owns the directory.
    rebound = drill.instance('contender-vs-new', drill.new_binary, drill.data, probe=new.probe)
    rebound.start()
    rebound_code = rebound.wait_exit(20)
    check(rebound_code == STAGE_EXIT['lock'],
          'second contender exited %d after the new writer took over, expected %d (lock)'
          % (rebound_code, STAGE_EXIT['lock']))
    check(rebound.address is None, 'second contender bound a listener after the new writer took over')
    check(Session('http://' + new.address).status()['writable'],
          'new writer is not writable after the second contender was refused')
    drill.log('exclusivity', 'contender-vs-new-rejected', exit=rebound_code, new_address=new.address)

    status = Session('http://' + new.address).status()
    check(status['revision'] == snapshot['revision'],
          'new instance revision %d differs from pre-upgrade revision %d'
          % (status['revision'], snapshot['revision']))
    check(gates.get('image_verified') is True, 'readiness log did not confirm image verification')
    check(gates.get('image_digest') == snapshot['digest'],
          'readiness image digest %s does not match the pre-upgrade image digest %s'
          % (short(gates.get('image_digest')), short(snapshot['digest'])))
    check(gates.get('image_revision') == snapshot['revision'],
          'readiness image revision %r does not match pre-upgrade revision %d'
          % (gates.get('image_revision'), snapshot['revision']))
    check(gates.get('audited_layers') == len(snapshot['strata']['layers']),
          'readiness audited %r layers, catalog has %d'
          % (gates.get('audited_layers'), len(snapshot['strata']['layers'])))
    check(gates.get('probe_expected') == snapshot['observed'],
          'readiness probe expectation %r differs from the old writer observation %r'
          % (gates.get('probe_expected'), snapshot['observed']))
    check(gates.get('probe_observed') == snapshot['observed'],
          'readiness probe observed %r differs from the old writer observation %r'
          % (gates.get('probe_observed'), snapshot['observed']))

    session = Session('http://' + new.address)
    for layer in snapshot['strata']['layers']:
        name = layer['name']
        audit = session.get('/strata/%s/index-audit' % name)
        check(audit['records'] == snapshot['audits'][name]['records'],
              'layer %s audit records %d differ from pre-upgrade %d'
              % (name, audit['records'], snapshot['audits'][name]['records']))
        check(audit['layer_revision'] == snapshot['audits'][name]['revision'],
              'layer %s audit revision %d differs from pre-upgrade %d'
              % (name, audit['layer_revision'], snapshot['audits'][name]['revision']))
        check('index.%s' % name in gates,
              'readiness log has no index gate entry for layer %s' % name)
        check(gates['index.%s' % name] == 'records=%d checked_nodes=%d revision=%d'
              % (audit['records'], audit['checked_nodes'], snapshot['audits'][name]['revision']),
              'readiness index gate for %s is %r, live audit reports records=%d checked_nodes=%d revision=%d'
              % (name, gates['index.%s' % name], audit['records'], audit['checked_nodes'],
                 snapshot['audits'][name]['revision']))
    located = session.probe()
    check(sorted(row['id'] for row in located['features']) == snapshot['observed'],
          'live probe after takeover returned %r, expected %r'
          % (sorted(row['id'] for row in located['features']), snapshot['observed']))
    check('address' in startup, 'ready record has no address')

    evidence = new.readiness_report()
    check(evidence['data_dir'] == str(data), 'readiness report data_dir %r is not the shared directory'
          % evidence['data_dir'])
    check(evidence['lock'] == 'acquired', 'readiness report lock %r is not acquired' % evidence['lock'])
    check(evidence['revision'] == snapshot['revision'],
          'readiness report revision %d differs from pre-upgrade %d'
          % (evidence['revision'], snapshot['revision']))
    check(evidence['image']['verified'] is True and evidence['image']['source'] == 'file',
          'readiness report did not verify the on-disk image: %r' % evidence['image'])
    check(evidence['image']['digest'] == snapshot['digest'],
          'readiness report image digest %r differs from pre-upgrade %r'
          % (short(evidence['image']['digest']), short(snapshot['digest'])))
    check(evidence['image']['bytes'] == len(snapshot['image']),
          'readiness report image bytes %r differ from the exported image length %d'
          % (evidence['image']['bytes'], len(snapshot['image'])))
    audited = {entry['layer']: entry for entry in evidence['index']}
    check(set(audited) == {layer['name'] for layer in snapshot['strata']['layers']},
          'readiness report audited %r, catalog has %r'
          % (sorted(audited), sorted(layer['name'] for layer in snapshot['strata']['layers'])))
    for name, pre in snapshot['audits'].items():
        entry = audited[name]
        check(entry['records'] == pre['records'] and entry['revision'] == pre['revision'],
              'readiness report audit for %s is %r, pre-upgrade was %r' % (name, entry, pre))
    probe = evidence['probe']
    check(probe is not None, 'readiness report has no probe section')
    check(probe['expected'] == snapshot['observed'] and probe['observed'] == snapshot['observed'],
          'readiness report probe %r does not match the pre-upgrade observation %r'
          % (probe, snapshot['observed']))
    check(not probe.get('missing') and not probe.get('unexpected'),
          'readiness report probe carries a mismatch: %r' % probe)
    check(probe['layer_revision'] == snapshot['audits'][PROBE_LAYER]['revision'],
          'readiness report probe layer revision %r is not the pre-upgrade layer revision %r'
          % (probe['layer_revision'], snapshot['audits'][PROBE_LAYER]['revision']))

    order = [index for index, record in enumerate(new.records)
             if record.get('msg') in ('meridian readiness gates passed', 'meridian ready')]
    check(len(order) == 2 and order[0] < order[1],
          'readiness gates were not logged before the address announcement: %r'
          % [new.records[i].get('msg') for i in order])
    drill.classify(READY, dict(stage='ready', revision=status['revision'],
                               digest=short(snapshot['digest']), audited_layers=len(snapshot['audits']),
                               probe=snapshot['observed'], address=new.address,
                               readiness_report=str(new.report_path)))
    return dict(session=session, gates=gates, startup=startup, revision=status['revision'],
                evidence=evidence)


STAGE_EXIT = {'lock': 10, 'image': 11, 'index': 12, 'probe': 13}


def run_rejected_start(drill, label, binary, data, probe, stage):
    instance = drill.instance(label, binary, data, probe=probe)
    instance.start()
    code = instance.wait_exit(20)
    check(code == STAGE_EXIT[stage],
          '%s exited %d, expected %d for stage %s (log: %s)'
          % (label, code, STAGE_EXIT[stage], stage, instance.log_path))
    check(code != 0, '%s exited 0, expected a readiness failure' % label)
    check(instance.address is None, '%s bound a listener despite a readiness failure' % label)
    check(instance.gate_record() is None, '%s passed readiness gates despite a failure' % label)
    check(not instance.report_path.exists(), '%s wrote a readiness report despite a failure' % label)
    failure = instance.failure_record()
    check(failure is not None, '%s exited without a readiness failure record' % label)
    check(failure.get('stage') == stage,
          '%s logged stage %r, expected %r: %s' % (label, failure.get('stage'), stage, failure.get('error')))
    return instance, code, failure


def phase_corrupt_image(drill, new_binary, snapshot, data):
    print('\nPhase 3 - corrupt image is distinguished from a busy lock')
    scratch = drill.workdir / 'corrupt-copy'
    if scratch.exists():
        shutil.rmtree(scratch)
    shutil.copytree(data, scratch)
    state = scratch / STATE
    original = state.read_bytes()
    check(digest(original) == snapshot['digest'],
          'scratch copy digest %s differs from the verified pre-upgrade digest %s'
          % (short(digest(original)), short(snapshot['digest'])))
    damaged = bytearray(original)
    damaged[-1] ^= 0x01
    state.write_bytes(bytes(damaged))
    instance, code, failure = run_rejected_start(
        drill, 'corrupt-image', new_binary, scratch,
        probe_spec(PROBE_LAYER, PROBE_POINT, snapshot['observed']), 'image')
    check(failure.get('stage') == 'image',
          'corrupt image failed at stage %r: %s' % (failure.get('stage'), failure.get('error')))
    check('checksum' in str(failure.get('error', '')).lower(),
          'corrupt image failure did not report a checksum problem: %s' % failure.get('error'))
    check(state.read_bytes() == bytes(damaged), 'failed startup rewrote the corrupt image')
    check(digest((data / STATE).read_bytes()) == snapshot['digest'],
          'failed startup on the scratch copy mutated the shared data directory')
    drill.classify(IMAGE_CORRUPT, dict(stage=failure['stage'], exit=code, error=failure['error'],
                                       detail='one payload byte flipped; checksum rejected before serving'))
    drill.log('corrupt-image', 'verified-failure', exit=code, stage=failure['stage'])

    # File-level rollback is safe here: the failed writer never committed, so
    # the pre-upgrade image is still the newest state.
    check(digest((data / STATE).read_bytes()) == snapshot['digest'],
          'file-level rollback is not safe: shared image no longer matches the pre-upgrade digest')
    state.write_bytes(original)
    restored = drill.instance('corrupt-copy-restored', new_binary, scratch,
                              probe=probe_spec(PROBE_LAYER, PROBE_POINT, snapshot['observed']))
    restored.start()
    restored.wait_ready()
    check(Session('http://' + restored.address).status()['revision'] == snapshot['revision'],
          'restored copy did not come back at the pre-upgrade revision')
    restored.stop()
    drill.gate('file-swap-allowed', 'revision == pre-upgrade revision and digest == pre-upgrade digest',
               'restored verified image bytes; service ready at revision %d' % snapshot['revision'])
    return failure


def repack_image(raw, mutate):
    """Re-encode an MPB v1 image so the result passes its own signature,
    length and SHA-256 checks. Used to model damage that a checksum cannot see:
    a valid image whose content no longer matches the pre-upgrade truth."""
    check(raw[:8] == b'MRDNMPB\x01', 'image has an unexpected signature: %r' % raw[:8])
    payload = json.loads(raw[44:])
    mutate(payload)
    # Keep the original key order: the image decoder rejects any payload that
    # is not byte-identical to its own canonical re-encoding.
    body = json.dumps(payload, separators=(',', ':')).encode()
    return raw[:8] + len(body).to_bytes(4, 'big') + hashlib.sha256(body).digest() + body, body


def phase_semantic_drift(drill, new_binary, snapshot, data):
    print('\nPhase 4 - checksum-clean image whose content no longer matches the probe')
    scratch = drill.workdir / 'drift-copy'
    if scratch.exists():
        shutil.rmtree(scratch)
    shutil.copytree(data, scratch)
    state = scratch / STATE

    def move_feature(payload):
        for layer in payload['layers']:
            if layer['definition']['name'] != PROBE_LAYER:
                continue
            for item in layer['features']:
                if item['id'] == 'ferrule':
                    extent = item['shape']['extent']
                    shift = 10
                    extent['min_x'] += shift
                    extent['max_x'] += shift
                    return
        raise DrillFailure('seed image has no ferrule to move')

    repacked, body = repack_image(state.read_bytes(), move_feature)
    state.write_bytes(repacked)
    check(digest(repacked) != snapshot['digest'], 'drifted image kept the pre-upgrade digest')
    instance, code, failure = run_rejected_start(
        drill, 'semantic-drift', new_binary, scratch,
        probe_spec(PROBE_LAYER, PROBE_POINT, snapshot['observed']), 'probe')
    check('checksum' not in str(failure.get('error', '')).lower(),
          'drifted image was rejected by checksum, not by the probe: %s' % failure.get('error'))
    check(failure.get('probe_unexpected') is None and 'ferrule' in failure.get('probe_missing', []),
          'drifted image was not classified as a probe-only miss: %r' % failure)
    drill.classify(IMAGE_DRIFT,
                   dict(stage=failure['stage'], exit=code, error=failure['error'],
                        checksum_valid=True, missing=failure.get('probe_missing'),
                        detail='image re-signed after moving a feature; index audit passed, locate probe caught it'))
    drill.log('semantic-drift', 'verified-failure', exit=code, stage=failure['stage'],
              missing=failure.get('probe_missing'))
    return failure


def phase_probe_mismatch(drill, new_binary, snapshot, data):
    print('\nPhase 5 - probe disagreement with an intact image')
    wrong = ['dowel']
    instance, code, failure = run_rejected_start(
        drill, 'probe-mismatch', new_binary, data,
        probe_spec(PROBE_LAYER, PROBE_POINT, wrong), 'probe')
    check(failure.get('stage') == 'probe',
          'wrong probe failed at stage %r: %s' % (failure.get('stage'), failure.get('error')))
    check(failure.get('probe_expected') == wrong,
          'probe failure log expected %r, wanted %r' % (failure.get('probe_expected'), wrong))
    check(failure.get('probe_observed') == snapshot['observed'],
          'probe failure log observed %r, wanted %r'
          % (failure.get('probe_observed'), snapshot['observed']))
    check(failure.get('probe_unexpected') == [row for row in snapshot['observed'] if row not in wrong],
          'probe failure log did not name the unexpected feature: %r' % failure.get('probe_unexpected'))
    check(digest((data / STATE).read_bytes()) == snapshot['digest'],
          'failed probe startup mutated the shared image')
    drill.classify(PROBE_MISMATCH, dict(stage=failure['stage'], exit=code, error=failure['error'],
                                        expected=wrong, observed=snapshot['observed'],
                                        unexpected=failure.get('probe_unexpected'),
                                        detail='image and index passed; locate gate rejected'))
    drill.log('probe-mismatch', 'verified-failure', exit=code, stage=failure['stage'])
    return failure


def phase_rollback(drill, new, snapshot, data):
    print('\nPhase 6 - rollback gates and their logs')
    session = Session('http://' + new.address)

    # A post-upgrade commit moves the revision. The pre-upgrade image then has
    # a stale target revision: file-level swap would silently discard a commit.
    added = session.edit(feature(PINS, 'pin-late', dict(x=5, y=1)))
    advanced = added['revision']
    check(advanced > snapshot['revision'], 'post-upgrade edit did not advance the revision')
    drill.log('rollback', 'post-upgrade-commit', revision=advanced)
    new.stop()

    current = (data / STATE).read_bytes()
    file_swap_safe = (advanced == snapshot['revision'] and digest(current) == snapshot['digest'])
    check(not file_swap_safe, 'rollback gate wrongly allowed a file swap after a commit')
    drill.gate('file-swap-refused', 'revision advanced beyond the pre-upgrade revision',
               'refused: would discard committed revision %d' % advanced)

    # The supported path after a commit is a CAS image restore on a live
    # writer. It preserves history by producing a new revision.
    new.start()
    new.wait_restart_ready()
    session = Session('http://' + new.address)
    stale = 409
    session.restore(snapshot['image'], snapshot['revision'], stale)
    check(session.status()['revision'] == advanced,
          'stale CAS restore changed the revision from %d' % advanced)
    drill.log('rollback', 'stale-cas-rejected', status=stale, revision=advanced)
    restored = session.restore(snapshot['image'], advanced)
    check(restored['revision'] == advanced + 1,
          'CAS restore produced revision %d, wanted %d' % (restored['revision'], advanced + 1))
    image, revision = session.image()
    check(revision == advanced + 1, 'image revision %d after restore, wanted %d' % (revision, advanced + 1))
    check(image_signature(image) == image_signature(snapshot['image']),
          'CAS restore did not reproduce the pre-upgrade feature content')
    check(sorted(row['id'] for row in session.probe()['features']) == snapshot['observed'],
          'probe after CAS restore does not match the pre-upgrade observation')
    session.get('/strata/%s/index-audit' % PROBE_LAYER)
    drill.gate('cas-restore', 'live writer, If-Meridian-Revision = current revision, verified image',
               'restored pre-upgrade content at revision %d' % (advanced + 1))
    drill.log('rollback', 'cas-restored', revision=advanced + 1,
              signature=short(image_signature(image)))
    new.stop()

    # Never swap files after a fenced/uncertain commit: reopen and audit.
    drill.gate('file-swap-forbidden', 'writer fenced or commit_uncertain',
               'not attempted: reopen the directory and verify image, index and probe first')
    return dict(revision=advanced + 1, signature=image_signature(image))


def phase_final_start(drill, new_binary, data, expected_signature):
    print('\nPhase 7 - final restart from the shared directory')
    final = drill.instance('final', new_binary, data,
                           probe=probe_spec(PROBE_LAYER, PROBE_POINT, PROBE_EXPECT))
    final.start()
    startup = final.wait_ready()
    session = Session('http://' + final.address)
    for layer in session.get('/strata')['layers']:
        session.get('/strata/%s/index-audit' % layer['name'])
    image, revision = session.image()
    check(image_signature(image) == expected_signature,
          'final restart content signature differs from the CAS-restored image')
    drill.log('final', 'ready', revision=revision, address=startup['address'])
    final.stop()
    return revision


def build_binary(destination):
    environment = dict(os.environ)
    go = environment.get('GO') or shutil.which('go')
    if go is None:
        raise SystemExit('go toolchain not found; set GO or pass --binary/--old-binary/--new-binary')
    destination.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([go, 'build', '-o', str(destination), './cmd/meridian'], check=True, cwd=ROOT, timeout=180)
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--workdir', type=Path, help='state, logs and backups; must be absent or empty')
    parser.add_argument('--keep', action='store_true', help='keep a temporary workdir and print its path')
    parser.add_argument('--force', action='store_true', help='clear a non-empty workdir')
    parser.add_argument('--binary', type=Path, help='one binary for both instances')
    parser.add_argument('--old-binary', type=Path, help='pre-upgrade instance binary')
    parser.add_argument('--new-binary', type=Path, help='post-upgrade instance binary')
    args = parser.parse_args()

    temporary = None
    if args.workdir is None:
        temporary = tempfile.mkdtemp(prefix='meridian-takeover-')
        workdir = Path(temporary)
    else:
        workdir = args.workdir.resolve()
        if workdir.exists() and any(workdir.iterdir()):
            if not args.force:
                raise SystemExit('workdir %s is not empty; pass --force to clear it' % workdir)
            shutil.rmtree(workdir)
    workdir.mkdir(parents=True, exist_ok=True)
    logs, data, backup = workdir / 'logs', workdir / 'data', workdir / 'backup'
    logs.mkdir(exist_ok=True)
    backup.mkdir(exist_ok=True)
    drill = Drill(workdir, logs)
    drill.data = data
    drill.logs = logs

    old_binary = args.old_binary or args.binary
    new_binary = args.new_binary or args.binary
    if old_binary is None or new_binary is None:
        built = build_binary(workdir / 'build' / 'meridian')
        old_binary = old_binary or built
        new_binary = new_binary or built
    old_binary, new_binary = Path(old_binary), Path(new_binary)
    for binary in (old_binary, new_binary):
        check(binary.exists(), 'binary %s does not exist' % binary)
    drill.old_binary, drill.new_binary, drill.data = old_binary, new_binary, data
    drill.upgrade = old_binary.read_bytes() != new_binary.read_bytes()
    drill.log('setup', 'binaries', old=str(old_binary), new=str(new_binary),
              old_sha=short(digest(old_binary.read_bytes())), new_sha=short(digest(new_binary.read_bytes())),
              upgrade=drill.upgrade, workdir=str(workdir))

    old = new = None
    try:
        print('Phase 0 - seed the shared data directory with the old instance')
        # A real pre-upgrade binary does not know the readiness flags, so only
        # the new role writes evidence.
        old = drill.instance('old', old_binary, data, evidence=not drill.upgrade)
        old.start()
        old.wait_ready()
        session = Session('http://' + old.address)
        seed(session)
        snapshot = load_snapshot(session)
        check(snapshot['observed'] == PROBE_EXPECT,
              'seed probe observed %r, expected %r' % (snapshot['observed'], PROBE_EXPECT))
        (backup / 'pre-upgrade.mpb').write_bytes(snapshot['image'])
        check(digest((data / STATE).read_bytes()) == snapshot['digest'],
              'exported pre-upgrade image does not match the on-disk image')
        drill.log('setup', 'snapshot', revision=snapshot['revision'], digest=short(snapshot['digest']),
                  bytes=len(snapshot['image']), layers=len(snapshot['strata']['layers']),
                  probe=snapshot['observed'], backup=str(backup / 'pre-upgrade.mpb'))

        phase_exclusivity(drill, old, session, snapshot)

        snapshot = load_snapshot(session)
        (backup / 'pre-upgrade.mpb').write_bytes(snapshot['image'])
        check(digest((data / STATE).read_bytes()) == snapshot['digest'],
              'post-exclusivity image changed between export and disk read')
        drill.log('setup', 'pre-upgrade-snapshot', revision=snapshot['revision'],
                  digest=short(snapshot['digest']), probe=snapshot['observed'])

        new = drill.instance('new', new_binary, data)
        takeover = phase_takeover(drill, old, new, snapshot, data)
        new.stop()
        new.wait_exit()

        phase_corrupt_image(drill, new_binary, snapshot, data)
        phase_semantic_drift(drill, new_binary, snapshot, data)
        phase_probe_mismatch(drill, new_binary, snapshot, data)

        new = drill.instance('new-after-gates', new_binary, data)
        new.start()
        new.wait_ready()
        rollback = phase_rollback(drill, new, snapshot, data)
        phase_final_start(drill, new_binary, data, rollback['signature'])

        enabled = str(workdir) if (temporary is None or args.keep) else None
        summary = dict(drill='meridian-takeover', result='pass', workdir=enabled,
                       classifications=drill.classifications, rollback_gates=drill.rollback_gates,
                       event_log=str(drill.events_path), pre_upgrade_revision=snapshot['revision'],
                       pre_upgrade_digest=snapshot['digest'], takeover_revision=takeover['revision'],
                       rollback_revision=rollback['revision'])
        (workdir / 'summary.json').write_text(json.dumps(summary, indent=2, sort_keys=True) + '\n')
        print_summary(drill, summary)
        return summary
    except DrillFailure as error:
        drill.log('drill', 'failed', error=str(error))
        print('\nDRILL FAILED: %s' % error, file=sys.stderr)
        print('Logs kept in %s' % workdir, file=sys.stderr)
        raise SystemExit(1)
    except Exception as error:  # keep the event log useful for unexpected faults
        drill.log('drill', 'error', error=repr(error))
        print('\nDRILL ERROR: %r' % error, file=sys.stderr)
        print('Logs kept in %s' % workdir, file=sys.stderr)
        raise
    finally:
        for instance in drill.instances:
            instance.kill()
        if temporary is not None and not args.keep:
            shutil.rmtree(temporary, ignore_errors=True)


def print_summary(drill, summary):
    print('\nClassification')
    for entry in summary['classifications']:
        detail = ' '.join('%s=%s' % (key, entry[key]) for key in sorted(entry)
                          if key not in ('kind', 'detail'))
        print('  %-16s %s' % (entry['kind'], detail))
    print('\nRollback gates')
    for gate in summary['rollback_gates']:
        print('  %-18s %s' % (gate['name'], gate['outcome']))
        print('  %-18s rule: %s' % ('', gate['rule']))
    print('\nRevision trail: pre-upgrade %d -> takeover %d -> rollback %d'
          % (summary['pre_upgrade_revision'], summary['takeover_revision'], summary['rollback_revision']))
    print('Drill log: %s' % summary['event_log'])
    print('Result: PASS')


if __name__ == '__main__':
    main()
