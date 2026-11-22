package db

import "sync"

// Stable window sessions pin the immutable view captured by the first page so
// later pages read one snapshot even while the layer changes. Retention is
// bounded: opening beyond the cap evicts the least recently used session, and
// abandoned ids are remembered only so reuse can be told apart from expiry.
const (
	maxWindowSessions  = 32
	maxWindowAbandoned = 32
)

type windowSession struct {
	view *state
	used uint64
}

type windowSessions struct {
	mu        sync.Mutex
	tick      uint64
	nextID    uint64
	sessions  map[uint64]*windowSession
	abandoned map[uint64]bool
	retired   []uint64
}

func newWindowSessions() *windowSessions {
	return &windowSessions{sessions: make(map[uint64]*windowSession), abandoned: make(map[uint64]bool)}
}

// open pins view for a new session and returns its id, evicting the least
// recently used session when the registry is full.
func (w *windowSessions) open(view *state) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.sessions) >= maxWindowSessions {
		var evict uint64
		oldest := ^uint64(0)
		for id, session := range w.sessions {
			if session.used < oldest {
				evict, oldest = id, session.used
			}
		}
		delete(w.sessions, evict)
	}
	w.nextID++
	w.tick++
	w.sessions[w.nextID] = &windowSession{view: view, used: w.tick}
	return w.nextID
}

// view returns the pinned view for id and marks the session recently used.
func (w *windowSessions) view(id uint64) (*state, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	session, ok := w.sessions[id]
	if !ok {
		return nil, w.gone(id)
	}
	w.tick++
	session.used = w.tick
	return session.view, nil
}

// abandon releases the pinned view and records the id as abandoned.
func (w *windowSessions) abandon(id uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.sessions[id]; !ok {
		return w.gone(id)
	}
	delete(w.sessions, id)
	w.abandoned[id] = true
	w.retired = append(w.retired, id)
	if len(w.retired) > maxWindowAbandoned {
		delete(w.abandoned, w.retired[0])
		w.retired = w.retired[1:]
	}
	return nil
}

// gone reports why an unknown session id cannot be served. The caller holds
// the mutex.
func (w *windowSessions) gone(id uint64) error {
	if w.abandoned[id] {
		return windowAbandoned("stable window session was abandoned")
	}
	return windowExpired("stable window session has expired")
}
