package folio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

const stateName = "meridian-state.mpb"

// LockFileName is the single directory entry both lock namespaces live on.
const LockFileName = "meridian.writer"

type Store struct {
	dir  string
	lock *os.File
}

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
