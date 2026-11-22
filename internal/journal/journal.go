// Package journal keeps the recent committed revisions so short-lived
// subscribers can catch up after a disconnect without replaying the database.
package journal

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// RetainedRevisions bounds how many committed revisions stay available for
// catch-up. Each revision keeps at most one batch of 512 edit operations.
const RetainedRevisions = 64

// LayerChange records one feature identity as it stood before and after a
// commit. A nil side means the feature did not exist in that state.
type LayerChange struct {
	ID   string
	From *catalog.Feature
	To   *catalog.Feature
}

// Entry is everything that changed in one durable database revision.
type Entry struct {
	Revision uint64
	Layers   map[string][]LayerChange
}

// Journal is a bounded, append-only ring of commit entries. Appends run under
// the database write lock; concurrent reads are guarded by an RWMutex. The
// notify channel wakes long-polling readers.
type Journal struct {
	mu      sync.RWMutex
	entries []*Entry
	notify  chan struct{}
}

func New() *Journal {
	return &Journal{notify: make(chan struct{})}
}

// Append publishes one commit and drops revisions older than the retention
// window. It returns the oldest revision still available afterwards.
func (j *Journal) Append(entry *Entry) uint64 {
	j.mu.Lock()
	j.entries = append(j.entries, entry)
	if len(j.entries) > RetainedRevisions {
		j.entries = slices.Delete(j.entries, 0, len(j.entries)-RetainedRevisions)
	}
	oldest := j.oldestLocked()
	close(j.notify)
	j.notify = make(chan struct{})
	j.mu.Unlock()
	return oldest
}

// Oldest reports the first retained revision, or 0 when nothing is retained.
func (j *Journal) Oldest() uint64 {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.oldestLocked()
}

func (j *Journal) oldestLocked() uint64 {
	if len(j.entries) == 0 {
		return 0
	}
	return j.entries[0].Revision
}

// Since returns retained entries with revisions strictly greater than cursor,
// in commit order, together with the oldest retained revision.
func (j *Journal) Since(cursor uint64) (uint64, []*Entry) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	oldest := j.oldestLocked()
	start, _ := slices.BinarySearchFunc(j.entries, cursor+1, func(e *Entry, want uint64) int {
		if e.Revision < want {
			return -1
		}
		return 1
	})
	out := append([]*Entry(nil), j.entries[start:]...)
	return oldest, out
}

// Changed reports whether the journal has reached a revision above cursor.
func (j *Journal) Changed(cursor uint64) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return len(j.entries) > 0 && j.entries[len(j.entries)-1].Revision > cursor
}

// Wait blocks until the journal passes cursor, the context is cancelled or
// the timeout elapses. A non-positive timeout returns immediately.
func (j *Journal) Wait(ctx context.Context, cursor uint64, timeout time.Duration) {
	if timeout <= 0 || j.Changed(cursor) {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		j.mu.RLock()
		if j.changedLocked(cursor) {
			j.mu.RUnlock()
			return
		}
		ch := j.notify
		j.mu.RUnlock()
		select {
		case <-ch:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (j *Journal) changedLocked(cursor uint64) bool {
	return len(j.entries) > 0 && j.entries[len(j.entries)-1].Revision > cursor
}
