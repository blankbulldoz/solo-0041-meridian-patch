package folio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const stateName = "meridian-state.mpb"

// LockedError reports that another process holds the exclusive writer lock.
// Callers can distinguish it from image or index startup failures.
type LockedError struct{ Err error }

func (e *LockedError) Error() string { return fmt.Sprintf("acquire data directory: %v", e.Err) }
func (e *LockedError) Unwrap() error { return e.Err }

// IsLocked reports whether err means another writer owns the data directory.
func IsLocked(err error) bool {
	var locked *LockedError
	return errors.As(err, &locked)
}

type Store struct {
	dir  string
	lock *os.File
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "meridian.writer"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, &LockedError{Err: err}
	}
	return &Store{dir: dir, lock: lock}, nil
}

func (s *Store) Close() error { return s.lock.Close() }

// Dir reports the directory this store has locked.
func (s *Store) Dir() string { return s.dir }

func (s *Store) Read() ([]byte, bool, error) {
	file, err := os.Open(filepath.Join(s.dir, stateName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxImage+1))
	return raw, true, err
}

// Replace reports whether the rename happened, even if directory sync failed.
// After that failure the process must not resume writes against the old state.
func (s *Store) Replace(raw []byte) (renamed bool, err error) {
	directory, err := os.Open(s.dir)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	temp, err := os.CreateTemp(s.dir, ".meridian-stage-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temp.Name(), filepath.Join(s.dir, stateName)); err != nil {
		return false, err
	}
	return true, directory.Sync()
}
