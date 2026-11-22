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
	reviews *reviews
}

func Open(dir string) (*Database, error) {
	store, err := folio.Open(dir)
	if err != nil {
		return nil, err
	}
	d, err := openStore(store)
	if err != nil {
		store.Close()
		return nil, err
	}
	return d, nil
}

func openStore(store *folio.Store) (*Database, error) {
	signer, err := bookmark.New()
	if err != nil {
		return nil, err
	}
	d := &Database{store: store, signer: signer, reviews: newReviews(DefaultReviewPolicy())}
	s := &state{layers: make(map[string]*layer)}
	raw, present, err := store.Read()
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if present {
		s, err = readImage(raw)
		if err != nil {
			return nil, fmt.Errorf("reopen state: %w", err)
		}
	}
	d.head.Store(s)
	return d, nil
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
