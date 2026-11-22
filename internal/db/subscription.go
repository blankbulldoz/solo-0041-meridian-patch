package db

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/bookmark"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/journal"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// Event kinds describe what happened to one feature identity relative to the
// subscription window.
const (
	EventEntered = "entered"
	EventLeft    = "left"
	EventChanged = "changed"
)

// MaxSubscriptionWait keeps long polls inside the server write timeout.
const MaxSubscriptionWait = 25 * time.Second

// SubscriptionRequest opens a change stream over a fixed window.
type SubscriptionRequest struct {
	Bounds plane.Box `json:"bounds"`
	Class  string    `json:"class,omitempty"`
}

// Snapshot is the immutable window content at subscription start plus the
// signed token used to stream later revisions.
type Snapshot struct {
	ReadStamp
	spatial.MatchSet
	OldestRevision uint64 `json:"oldest_revision"`
	Subscription   string `json:"subscription"`
}

// Subscribe returns the current window contents and a signed subscription
// token. The token binds the layer, window, class and starting revision, so
// later fetches cannot silently change predicate.
func (d *Database) Subscribe(name string, req SubscriptionRequest) (Snapshot, error) {
	if err := validateWindow(req.Bounds, req.Class); err != nil {
		return Snapshot{}, err
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return Snapshot{}, err
	}
	match := spatial.Window(l.tree, req.Bounds, req.Class, "")
	copyFeatures(match.Features)
	token := d.signer.SealSubscription(bookmark.Subscription{
		Layer:    name,
		Bounds:   req.Bounds,
		Class:    req.Class,
		Revision: s.revision,
	})
	return Snapshot{
		ReadStamp:      stamp(s, l),
		MatchSet:       match,
		OldestRevision: d.journal.Oldest(),
		Subscription:   token,
	}, nil
}

// EventBatch is one revision's window-relevant changes. Batches are emitted in
// commit order and a batch is never split across responses.
type EventBatch struct {
	Revision uint64              `json:"revision"`
	Events   []SubscriptionEvent `json:"events"`
}

// SubscriptionEvent reports one feature transition. The feature payload is the
// current feature for entered/changed and the last-known feature for left.
type SubscriptionEvent struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Feature catalog.Feature `json:"feature"`
}

// EventsResponse carries every batch strictly after AfterRevision, or an empty
// batch list when the cursor already matches the head.
type EventsResponse struct {
	Revision       uint64       `json:"revision"`
	OldestRevision uint64       `json:"oldest_revision"`
	Batches        []EventBatch `json:"batches"`
}

// EventsRequest resumes a stream. Wait long-polls when no newer retained
// revision exists yet.
type EventsRequest struct {
	Subscription  string `json:"subscription"`
	AfterRevision uint64 `json:"after_revision"`
	WaitMillis    int    `json:"wait_millis,omitempty"`
}

// Events returns committed changes for a subscription. It fails with a resync
// error when the cursor points at a revision the retention window no longer
// covers, so callers know the stream must be re-established from a snapshot.
func (d *Database) Events(ctx context.Context, req EventsRequest) (EventsResponse, error) {
	sub, err := d.signer.OpenSubscription(req.Subscription)
	if err != nil {
		return EventsResponse{}, invalid("subscription token is invalid or expired; re-create it from a snapshot")
	}
	if req.AfterRevision < sub.Revision {
		return EventsResponse{}, invalid("after_revision %d predates the subscription revision %d", req.AfterRevision, sub.Revision)
	}
	wait := time.Duration(req.WaitMillis) * time.Millisecond
	if req.WaitMillis < 0 || wait > MaxSubscriptionWait {
		return EventsResponse{}, invalid("wait_millis must be 0..%d", MaxSubscriptionWait/time.Millisecond)
	}
	// Resolve the layer before sleeping so a deleted or never-existing layer
	// fails immediately instead of holding a long-poll connection.
	if _, err := d.head.Load().getLayer(sub.Layer); err != nil {
		return EventsResponse{}, err
	}
	if wait > 0 {
		d.journal.Wait(ctx, req.AfterRevision, wait)
		if err := ctx.Err(); err != nil {
			return EventsResponse{}, err
		}
	}
	s := d.head.Load()
	if req.AfterRevision > s.revision {
		return EventsResponse{}, invalid("after_revision %d is ahead of current revision %d", req.AfterRevision, s.revision)
	}
	oldest, entries := d.journal.Since(req.AfterRevision)
	if oldest > 0 && req.AfterRevision < oldest {
		return EventsResponse{}, &Error{
			Code:    "resync",
			Message: "retained history starts at revision " + strconv.FormatUint(oldest, 10) + "; re-create the subscription from a snapshot",
		}
	}
	response := EventsResponse{Revision: s.revision, OldestRevision: d.journal.Oldest(), Batches: make([]EventBatch, 0, len(entries))}
	for _, entry := range entries {
		if entry.Revision <= req.AfterRevision {
			continue
		}
		batch := projectBatch(entry, sub)
		if len(batch.Events) > 0 {
			response.Batches = append(response.Batches, batch)
		}
	}
	return response, nil
}

// projectBatch translates one journal entry into window-scoped events. Each
// feature is classified against its pre- and post-commit window membership.
func projectBatch(entry *journal.Entry, sub bookmark.Subscription) EventBatch {
	batch := EventBatch{Revision: entry.Revision}
	for _, change := range entry.Layers[sub.Layer] {
		beforeIn := change.From != nil && matchesSubscription(*change.From, sub)
		afterIn := change.To != nil && matchesSubscription(*change.To, sub)
		switch {
		case !beforeIn && afterIn:
			batch.Events = append(batch.Events, SubscriptionEvent{ID: change.ID, Kind: EventEntered, Feature: change.To.Copy()})
		case beforeIn && !afterIn:
			batch.Events = append(batch.Events, SubscriptionEvent{ID: change.ID, Kind: EventLeft, Feature: change.From.Copy()})
		case beforeIn && afterIn:
			batch.Events = append(batch.Events, SubscriptionEvent{ID: change.ID, Kind: EventChanged, Feature: change.To.Copy()})
		}
	}
	sort.Slice(batch.Events, func(i, j int) bool { return batch.Events[i].ID < batch.Events[j].ID })
	return batch
}

func matchesSubscription(feature catalog.Feature, sub bookmark.Subscription) bool {
	if sub.Class != "" && feature.Class != sub.Class {
		return false
	}
	return feature.Shape.InWindow(sub.Bounds)
}
