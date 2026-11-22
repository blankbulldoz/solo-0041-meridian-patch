# Writer-lock migration: `flock` → POSIX record locks

This is the cutover procedure for moving one data directory from the legacy
single-`flock` writer lock to the target environment's POSIX record lock
(`fcntl(F_SETLK)`). It covers the rolling swap, the compatibility checks that
must gate the swap, and the conditions that force a rollback.

## Why both locks

On Linux and macOS, BSD `flock` locks and POSIX record locks are **separate
namespaces on the same file**. Holding one does not block the other. The
migration build takes both on `meridian.writer`:

| lock | holder | purpose |
| --- | --- | --- |
| `flock(LOCK_EX)` guard | legacy binaries and the new binary | cross-version arbiter; a pre-migration process still honours it |
| `fcntl(F_SETLK, F_WRLCK)` record lock, whole file | the new binary | the lock the target environment verifies the holder with |

Without the guard, a rolling swap has a split-brain window: the legacy process
holds its `flock`, the new process takes the record lock (it does not see the
`flock`), and both write `meridian-state.mpb`. The harness reproduces this
exactly — `--probe-lock record` reports `acquired: true` while a legacy process
is serving.

Both locks are on `meridian.writer`, which is never renamed. The image itself
is replaced by `rename(2)` (a new inode every commit), so a record lock placed
on `meridian-state.mpb` would silently stop protecting after the first commit.

## Ordering rules the build enforces

- Acquire guard, then record lock. Release record lock, then guard, then close.
  A process therefore appears in the verifiable namespace last on start and
  disappears from it first on stop.
- A contended directory exits with code **75** (`EX_TEMPFAIL`), so a supervisor
  retries instead of alerting. Any other exit code is a real failure.
- Cluster/remote state: nothing here coordinates two hosts. The lock is
  advisory and local; see the NFS/SMB warning below.

## Pre-cutover checks

Run these with the **new** binary. They never start HTTP service and never take
the directory for longer than the probe.

```sh
NEW=./meridian
DATA=/srv/meridian/data

# 1. What holds the directory, and does the filesystem support record locks?
$NEW -data "$DATA" -check-lock
```

`classification` is one of:

| classification | meaning | action |
| --- | --- | --- |
| `free` | nothing holds either lock | safe to take over |
| `legacy-holder` | only the `flock` guard is held | a pre-migration process is live; stop it before takeover |
| `migrated-holder` | guard **and** record lock held | this build owns the directory |
| `partial-holder` | only a record lock held, no guard | an unguarded build is live (this is the state a naive migration produces); find the pid and stop it |

The report also prints `device_key` + `inode` in the `major:minor:inode` form
used by `/proc/locks`, and `record_holder_pid` from `F_GETLK`. On Linux,
`grep <device_key>:<inode> /proc/locks` shows the kernel's own view of the
holder; `lslocks -p <pid>` shows the same without guessing.

```sh
# 2. Prove the two acquisition orders behave as the cutover requires.
$NEW -data "$DATA" -probe-lock legacy   # must succeed when no writer is live
$NEW -data "$DATA" -probe-lock record   # must succeed when no writer is live
$NEW -data "$DATA" -probe-lock both     # must succeed when no writer is live
```

During the rollout, while a legacy process is serving, the same two probes are
the compatibility gate:

- `-probe-lock legacy` must **fail with 75** (the legacy process holds the guard).
- `-probe-lock both` must **fail with 75** (the new binary refuses too).
- `-probe-lock record` will **succeed** — that is expected and is the reason
  the guard exists. Never deploy a record-lock-only build before every legacy
  process is stopped.

## Cutover (stop-and-swap, the supported path)

The process cannot be zero-downtime for writes: the directory is a
single-writer resource and the record lock must be observed by the target
environment. The swap is therefore: drain, stop, verify, start.

```sh
NEW=./meridian
DATA=/srv/meridian/data

# 1. Stop the legacy service gracefully (SIGTERM). It closes the flock.
systemctl stop meridian

# 2. Confirm nothing holds the directory and the image is readable.
$NEW -data "$DATA" -check-lock          # expect classification=free
$NEW -data "$DATA" -probe-lock both     # expect acquired:true

# 3. Ship the new binary and start it on the same directory.
systemctl start meridian

# 4. Confirm the holder is verifiable from the record-lock namespace.
$NEW -data "$DATA" -check-lock          # expect classification=migrated-holder
```

The new process re-reads and re-validates `meridian-state.mpb` before serving
(revision, SHA-256, canonical JSON, geometry), so the existing image is carried
over unchanged and `GET /status` reports the same revision.

### Rolling replacement of multiple processes per directory

Only one process may serve a given data directory, so "rolling" applies to
fleet-wide rollout of the binary across independent directories, not to two
processes on one directory. For each directory:

1. Run the pre-cutover checks against the new binary.
2. If `classification=legacy-holder`, wait for the legacy process to drain.
3. Stop it, re-check `free`, then start the new binary.
4. Accept that new binary only when `-check-lock` reports
   `migrated-holder`; otherwise roll back (below).

Do not stage a new binary and a legacy binary on the *same* directory at the
same time expecting the record lock to keep them apart — it will not, and the
guard is the only thing that does.

## Rollback conditions

Roll back to the legacy binary when any of these hold:

- `-probe-lock record` exits non-zero with `unsupported by the data directory
  filesystem` (`ENOLCK`/`EOPNOTSUPP`) — the filesystem cannot provide the lock
  the target environment verifies. Keep the legacy binary and the guard.
- `-check-lock` reports a non-empty `warnings` entry for `nfs`, `cifs/smb`,
  `overlayfs`, or `fuse`, and the reviewer's mount is the same one — record
  locks there depend on a remote/upper-layer lock manager that may not be
  consistent across hosts. Stay on the legacy build until the check passes on
  the real mount from every host that can open the directory.
- `GET /status` reports `writable: false` after a commit
  (`commit_uncertain` / fenced writer). The image may be one rename ahead of
  the reader's view; reopen the directory with the new binary to re-validate,
  and if it fails to reopen, restore the last `GET /image` export with
  `PUT /image` against the target revision.
- `meridian.writer` was deleted or replaced while a holder was live. The
  holder fences its next commit (`unlink`/replace is detected via device+inode).
  Stop the holder, restore the file, and restart — do not let two processes
  share the directory in that state.

Once every directory in the fleet reports `migrated-holder`, the legacy
compatibility build (`-tags legacylock`) is no longer needed.

## Compatibility harness

`examples/lock-migration.py` builds a real legacy binary with
`-tags legacylock` and a real new binary, then exercises the failure modes:

```sh
go build -o ./meridian ./cmd/meridian
go build -tags legacylock -o ./meridian-legacy ./cmd/meridian
python3 examples/lock-migration.py --scenario race \
    --new-binary ./meridian --legacy-binary ./meridian-legacy
```

| scenario | what it proves |
| --- | --- |
| `race` | 8 new processes racing an empty directory yield one survivor; the rest exit 75. A mixed legacy+new race also yields one survivor, because both take the guard. |
| `legacy-blocks` | a live legacy process makes the new binary exit 75, while `-probe-lock record` still succeeds — the split-brain hazard the guard closes. |
| `takeover` | after the legacy process stops, the new binary serves the existing revision, accepts writes, and persists them across restart. |
| `reader` | an image stream opened against the legacy process is followed by a takeover; the new process exports byte-identical image bytes and stable repeats, and writes produce a new revision. |
| `kill` | `SIGKILL` of either binary releases the directory immediately and the image is recovered by the next process. |
| `replaced` | unlinking `meridian.writer` under a live holder fences that holder (`503`) instead of allowing two writers. |
| `capability` | reports the filesystem class and lock device key for the target mount. |

All scenarios use temporary directories and loopback-only ports; they are safe
to run next to a served database.

## Operational notes

- `meridian.writer` must not be deleted while the service runs. It is safe to
  keep in backups; it carries no state, only the lock bytes.
- `-check-lock`'s guard probe takes `flock` for an instant because `flock` has
  no query call. A service starting in that instant exits 75 and is retried;
  this is the safe direction. The record probe (`F_GETLK`) never takes a lock.
- macOS has the same two independent lock namespaces, so the dual lock is
  correct there as well; `/proc/locks` is Linux-only, use `lsof -p <pid>` on
  macOS to correlate the holder.
