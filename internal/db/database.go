package db

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/bookmark"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

type Database struct {
	store   *folio.Store
	signer  bookmark.Signer
	head    atomic.Pointer[state]
	write   sync.Mutex
	blocked atomic.Bool
	closed  bool
}

// Open acquires the writer lock and verifies the stored image and indexes
// without running a locate probe. Prefer OpenChecked with a probe for takeover.
func Open(dir string) (*Database, error) {
	d, _, err := OpenChecked(dir, nil)
	return d, err
}

func (d *Database) Close() error {
	d.write.Lock()
	defer d.write.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.blocked.Store(true)
	return d.store.Close()
}

type Status struct {
	Revision uint64 `json:"revision"`
	Writable bool   `json:"writable"`
}

func (d *Database) Status() Status {
	return Status{Revision: d.head.Load().revision, Writable: !d.blocked.Load()}
}

func (d *Database) commit(expected *uint64, prepare func(*state, uint64) (*state, error)) (*state, error) {
	d.write.Lock()
	defer d.write.Unlock()
	if d.blocked.Load() {
		return nil, &Error{Code: "unavailable", Message: "database is fenced; reopen the data directory"}
	}
	current := d.head.Load()
	if expected != nil && *expected != current.revision {
		return nil, conflict("expected database revision %d, current revision is %d", *expected, current.revision)
	}
	if current.revision == math.MaxUint64 {
		return nil, exhausted("database revision space is exhausted")
	}
	next, err := prepare(current, current.revision+1)
	if err != nil {
		return nil, err
	}
	if next == current {
		return current, nil
	}
	raw, err := folio.Encode(next.image())
	if err != nil {
		return nil, fmt.Errorf("encode commit: %w", err)
	}
	renamed, err := d.store.Replace(raw)
	if err != nil {
		if renamed {
			d.blocked.Store(true)
			return nil, errors.Join(&Error{Code: "commit_uncertain", Message: "replace occurred but durability was not confirmed; reopen before retrying"}, err)
		}
		return nil, fmt.Errorf("persist commit: %w", err)
	}
	d.head.Store(next)
	return next, nil
}
