package db

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// ReviewPolicy bounds how many review sessions stay live and how long each
// pinned view is retained before the session expires.
type ReviewPolicy struct {
	Max int
	TTL time.Duration
}

// DefaultReviewPolicy applies unless SetReviewPolicy installs another policy.
func DefaultReviewPolicy() ReviewPolicy {
	return ReviewPolicy{Max: 64, TTL: 30 * time.Minute}
}

func (p ReviewPolicy) normalized() ReviewPolicy {
	defaults := DefaultReviewPolicy()
	if p.Max < 1 {
		p.Max = defaults.Max
	}
	if p.TTL <= 0 {
		p.TTL = defaults.TTL
	}
	return p
}

// ReviewInfo describes one live review session and its pinned revision.
type ReviewInfo struct {
	Session   string `json:"session"`
	Revision  uint64 `json:"revision"`
	ExpiresAt int64  `json:"expires_at"`
}

// ReviewClosed confirms that a review session is no longer usable.
type ReviewClosed struct {
	Session string `json:"session"`
	Closed  bool   `json:"closed"`
}

type review struct {
	view    *state
	expires time.Time
}

// reviews tracks live sessions. Sessions pin immutable states, so retention
// is a set of pointers; expiry is reaped lazily on registry access.
type reviews struct {
	mu   sync.Mutex
	max  int
	ttl  time.Duration
	live map[string]review
}

func newReviews(policy ReviewPolicy) *reviews {
	policy = policy.normalized()
	return &reviews{max: policy.Max, ttl: policy.TTL, live: make(map[string]review)}
}

func (r *reviews) configure(policy ReviewPolicy) {
	policy = policy.normalized()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.max = policy.Max
	r.ttl = policy.TTL
}

// reapLocked drops expired sessions; the caller must hold r.mu.
func (r *reviews) reapLocked(now time.Time) {
	for id, session := range r.live {
		if !now.Before(session.expires) {
			delete(r.live, id)
		}
	}
}

func (r *reviews) open(view *state) (ReviewInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.reapLocked(now)
	if len(r.live) >= r.max {
		return ReviewInfo{}, exhausted("review session capacity reached; close a review or wait for expiry")
	}
	var id string
	for {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return ReviewInfo{}, fmt.Errorf("mint review session: %w", err)
		}
		id = base64.RawURLEncoding.EncodeToString(raw)
		if _, taken := r.live[id]; !taken {
			break
		}
	}
	expires := now.Add(r.ttl)
	r.live[id] = review{view: view, expires: expires}
	return ReviewInfo{Session: id, Revision: view.revision, ExpiresAt: expires.Unix()}, nil
}

func (r *reviews) lookup(id string) (*state, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.live[id]
	if !ok {
		return nil, sessionGone()
	}
	if !time.Now().Before(session.expires) {
		delete(r.live, id)
		return nil, sessionGone()
	}
	return session.view, nil
}

func (r *reviews) close(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.live[id]
	if !ok || !time.Now().Before(session.expires) {
		delete(r.live, id)
		return sessionGone()
	}
	delete(r.live, id)
	return nil
}

// SetReviewPolicy adjusts review session limits. Call before serving traffic.
func (d *Database) SetReviewPolicy(policy ReviewPolicy) {
	d.reviews.configure(policy)
}

// OpenReview pins the current database view for a bounded review session.
func (d *Database) OpenReview() (ReviewInfo, error) {
	return d.reviews.open(d.head.Load())
}

// CloseReview ends a review session before its expiry.
func (d *Database) CloseReview(session string) (ReviewClosed, error) {
	if err := d.reviews.close(session); err != nil {
		return ReviewClosed{}, err
	}
	return ReviewClosed{Session: session, Closed: true}, nil
}

// view resolves the state a read observes: the pinned view of a live review
// session when a token is supplied, otherwise the current head.
func (d *Database) view(session string) (*state, error) {
	if session == "" {
		return d.head.Load(), nil
	}
	return d.reviews.lookup(session)
}
