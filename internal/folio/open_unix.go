//go:build unix && !legacylock

package folio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Open claims the directory writer lock. Both the flock guard and the POSIX
// record lock are taken on meridian.writer; either may be contended, and both
// must succeed before the image may be touched.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, LockFileName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := acquireGuard(lock); err != nil {
		lock.Close()
		return nil, fmt.Errorf("acquire data directory: %w", describeLockError("flock guard", err))
	}
	if err := acquireRecord(lock); err != nil {
		releaseGuard(lock)
		lock.Close()
		return nil, fmt.Errorf("acquire data directory: %w", describeLockError("record lock", err))
	}
	return &Store{dir: dir, lock: lock}, nil
}

// Close drops the record lock before the guard. The record lock is the lock
// operators inspect, so a closing process disappears from that view first and
// a still-running process is never reported as holding nothing.
func (s *Store) Close() error {
	recordErr := releaseRecord(s.lock)
	guardErr := releaseGuard(s.lock)
	closeErr := s.lock.Close()
	return errors.Join(recordErr, guardErr, closeErr)
}
