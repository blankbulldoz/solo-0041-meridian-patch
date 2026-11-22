//go:build unix

package folio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// The writer lock is built from two locks on the same file:
//
//   - a BSD flock guard, which older binaries (flock-only) already honour; and
//   - a POSIX record lock (fcntl F_SETLK, whole file), which the target
//     environment uses to verify the holder.
//
// flock and POSIX record locks live in separate namespaces on Linux and
// macOS: holding one does not block the other. A migration that switched
// straight from flock to record locks would therefore let a legacy binary and
// a new binary hold "their" lock at the same time and both write the image.
// Acquiring both makes the guard the cross-version arbiter while the record
// lock remains the verifiable, deployment-facing lock.

func acquireGuard(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func releaseGuard(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func acquireRecord(file *os.File) error {
	lock := syscall.Flock_t{
		Type:   int16(syscall.F_WRLCK),
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    0, // to EOF, so the whole file is covered
	}
	return syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &lock)
}

func releaseRecord(file *os.File) error {
	lock := syscall.Flock_t{
		Type:   int16(syscall.F_UNLCK),
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    0,
	}
	return syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &lock)
}

// probeRecord reports whether another process holds a conflicting write
// record lock. F_GETLK never takes a lock, so it is safe against a live
// service. A query is ignored for locks held by the caller, which is why the
// probe must run in its own process.
func probeRecord(file *os.File) (held bool, holderPID int, err error) {
	lock := syscall.Flock_t{
		Type:   int16(syscall.F_WRLCK),
		Whence: int16(io.SeekStart),
		Start:  0,
		Len:    0,
	}
	if err := syscall.FcntlFlock(file.Fd(), syscall.F_GETLK, &lock); err != nil {
		return false, 0, err
	}
	if lock.Type == int16(syscall.F_UNLCK) {
		return false, 0, nil
	}
	return true, int(lock.Pid), nil
}

// probeGuard takes the flock guard for an instant. flock has no query call, so
// a free directory is briefly claimed and released. A service starting in that
// window refuses to open (safe direction) and can simply be retried.
func probeGuard(file *os.File) (held bool, err error) {
	if err := acquireGuard(file); err != nil {
		if lockContended(err) {
			return true, nil
		}
		return false, err
	}
	return false, releaseGuard(file)
}

// ErrDirectoryBusy is returned when another writer owns the data directory.
// Callers can branch on it to distinguish "retry later" from a real failure.
var ErrDirectoryBusy = errors.New("data directory is owned by another writer")

func lockContended(err error) bool {
	return errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.EWOULDBLOCK)
}

func recordUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOLCK) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP)
}

// describeLockError turns a bare errno into an operator-actionable message.
func describeLockError(layer string, err error) error {
	switch {
	case lockContended(err):
		return fmt.Errorf("%w: %s is held by another process", ErrDirectoryBusy, layer)
	case recordUnsupported(err):
		return fmt.Errorf("%s is unsupported by the data directory filesystem: %w", layer, err)
	default:
		return fmt.Errorf("%s failed: %w", layer, err)
	}
}

// fileIdentity returns the device and inode numbers shown by /proc/locks, so
// an operator can correlate the lock file entry with the holder.
func fileIdentity(info os.FileInfo) (device, inode uint64) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev), uint64(stat.Ino)
	}
	return 0, 0
}
