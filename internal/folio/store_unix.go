//go:build unix

package folio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Legacy companion files left by pre-migration binaries. They are never
// locked nor created by this build; they exist only so takeover tooling can
// name exactly what must be removed after the last legacy process stops.
var legacyLockNames = []string{"meridian.writer.lock", "meridian.lock"}

// LockReport is the result of inspecting a data directory without taking it.
type LockReport struct {
	Directory      string   `json:"directory"`
	GuardHeld      bool     `json:"guard_held"`        // BSD flock namespace
	RecordHeld     bool     `json:"record_held"`       // POSIX record-lock namespace
	RecordHolder   int      `json:"record_holder_pid"` // 0 when free or unknown
	Device         uint64   `json:"device"`
	DeviceKey      string   `json:"device_key"` // e.g. 00:20e, as in /proc/locks
	Inode          uint64   `json:"inode"`
	Filesystem     string   `json:"filesystem"`
	Warnings       []string `json:"warnings"`
	LegacyFiles    []string `json:"legacy_lock_files"`
	Classification string   `json:"classification"`
	Action         string   `json:"action"`
}

// LockState classifies who can currently obtain the directory. It is used by
// `meridian -check-lock` and by takeover tooling, never by a serving process.
//
// The record probe is always safe: F_GETLK never acquires a lock. The guard
// probe momentarily takes the flock, because flock has no query call; it is
// only run on the assumption that the caller tolerates a failed probe against
// a live writer (a start racing the probe refuses to open and is retried).
func LockState(dir string) (LockReport, error) {
	report := LockReport{Directory: dir, LegacyFiles: []string{}, Warnings: []string{}}
	report.Filesystem, report.Warnings = filesystemSupport(dir)
	lock, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return report, err
	}
	defer lock.Close()
	if info, err := lock.Stat(); err == nil {
		report.Device, report.Inode = fileIdentity(info)
		report.DeviceKey = lockDeviceKey(report.Device)
	}
	for _, name := range legacyLockNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			report.LegacyFiles = append(report.LegacyFiles, name)
		}
	}

	recordHeld, holder, err := probeRecord(lock)
	if err != nil {
		return report, describeLockError("record probe", err)
	}
	report.RecordHeld, report.RecordHolder = recordHeld, holder

	guardHeld, err := probeGuard(lock)
	if err != nil {
		return report, describeLockError("guard probe", err)
	}
	report.GuardHeld = guardHeld

	switch {
	case report.GuardHeld && report.RecordHeld:
		report.Classification = "migrated-holder"
		report.Action = "a new-format writer owns the directory; stop it before takeover"
	case report.GuardHeld && !report.RecordHeld:
		report.Classification = "legacy-holder"
		report.Action = "a pre-migration writer owns the directory; stop it, then run takeover"
	case !report.GuardHeld && report.RecordHeld:
		report.Classification = "partial-holder"
		report.Action = "a record lock is held without the guard; identify the pid and stop it"
	default:
		report.Classification = "free"
		report.Action = "no writer holds the directory"
	}
	if len(report.LegacyFiles) > 0 && report.Classification == "free" {
		report.Action += "; stale legacy lock files may be removed"
	}
	return report, nil
}

// GuardOnlyAcquire reproduces the lock order of a pre-migration binary: it
// takes only the flock guard and returns a release function. A nil error means
// that a legacy binary and this build could not hold the directory together
// had the legacy one started at this instant.
func GuardOnlyAcquire(dir string) (func() error, error) {
	lock, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := acquireGuard(lock); err != nil {
		lock.Close()
		return nil, err
	}
	return func() error { return errors.Join(releaseGuard(lock), lock.Close()) }, nil
}

// RecordOnlyAcquire simulates the unguarded, record-lock-only binary the
// migration is heading toward. During a rolling swap this acquisition must
// fail whenever a legacy process is still running; that is the check which
// proves the guard is what prevents a split brain.
func RecordOnlyAcquire(dir string) (func() error, error) {
	lock, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := acquireRecord(lock); err != nil {
		lock.Close()
		return nil, err
	}
	return func() error { return lock.Close() }, nil
}

// Validate proves that the locked inode is still the directory entry named
// meridian.writer. If the lock file was unlinked or replaced, the record lock
// protects nothing: a second process can create and lock a fresh inode and
// both writers proceed. Replace reports this so a commit fences instead of
// corrupting the image.
func (s *Store) Validate() error {
	held, err := s.lock.Stat()
	if err != nil {
		return fmt.Errorf("stat held lock file: %w", err)
	}
	current, err := os.Stat(filepath.Join(s.dir, LockFileName))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s was removed while held; stop and reopen the directory", ErrDirectoryBusy, LockFileName)
	}
	if err != nil {
		return fmt.Errorf("stat lock file: %w", err)
	}
	heldDev, heldIno := fileIdentity(held)
	curDev, curIno := fileIdentity(current)
	if heldDev != curDev || heldIno != curIno {
		return fmt.Errorf("%w: %s was replaced while held (inode %d -> %d); stop and reopen the directory",
			ErrDirectoryBusy, LockFileName, heldIno, curIno)
	}
	return nil
}

// HolderSummary renders a one-line explanation for logs and command output.
func (r LockReport) HolderSummary() string {
	parts := []string{fmt.Sprintf("guard=%t", r.GuardHeld), fmt.Sprintf("record=%t", r.RecordHeld)}
	if r.RecordHolder > 0 {
		parts = append(parts, fmt.Sprintf("record_pid=%d", r.RecordHolder))
	}
	parts = append(parts, fmt.Sprintf("device=%s inode=%d", r.DeviceKey, r.Inode))
	if len(r.LegacyFiles) > 0 {
		parts = append(parts, "legacy="+strings.Join(r.LegacyFiles, ","))
	}
	return strings.Join(parts, " ")
}
