//go:build unix && legacylock

package folio

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// This file reproduces the pre-migration lock order exactly: one BSD flock on
// meridian.writer and nothing else. It exists so `go build -tags legacylock`
// produces a genuine legacy binary for cutover rehearsals; production builds
// never include it.

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("acquire data directory: %w", err)
	}
	return &Store{dir: dir, lock: lock}, nil
}

func (s *Store) Close() error { return s.lock.Close() }
