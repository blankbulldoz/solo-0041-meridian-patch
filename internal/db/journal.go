package db

import (
	"reflect"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/journal"
)

// diffStates captures every per-layer identity change between two adjacent
// immutable views. Running inside the write commit, it covers edits, layer
// registration, reframes and image restores uniformly, so one commit can never
// become visible as a partial change.
func diffStates(before, after *state) *journal.Entry {
	entry := &journal.Entry{Revision: after.revision}
	names := make(map[string]struct{}, len(before.layers)+len(after.layers))
	for name := range before.layers {
		names[name] = struct{}{}
	}
	for name := range after.layers {
		names[name] = struct{}{}
	}
	for name := range names {
		changes := diffLayer(before.layers[name], after.layers[name])
		if len(changes) > 0 {
			if entry.Layers == nil {
				entry.Layers = make(map[string][]journal.LayerChange)
			}
			entry.Layers[name] = changes
		}
	}
	return entry
}

func diffLayer(before, after *layer) []journal.LayerChange {
	var oldRows, newRows map[string]catalog.Feature
	if before != nil {
		oldRows = before.rows
	}
	if after != nil {
		newRows = after.rows
	}
	ids := make(map[string]struct{}, len(oldRows)+len(newRows))
	for id := range oldRows {
		ids[id] = struct{}{}
	}
	for id := range newRows {
		ids[id] = struct{}{}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	slices.Sort(ordered)
	changes := make([]journal.LayerChange, 0)
	for _, id := range ordered {
		oldFeature, hadOld := oldRows[id]
		newFeature, hasNew := newRows[id]
		switch {
		case hadOld && !hasNew:
			feature := oldFeature
			changes = append(changes, journal.LayerChange{ID: id, From: &feature})
		case !hadOld && hasNew:
			feature := newFeature
			changes = append(changes, journal.LayerChange{ID: id, To: &feature})
		case hadOld && hasNew && !reflect.DeepEqual(oldFeature, newFeature):
			oldCopy, newCopy := oldFeature, newFeature
			changes = append(changes, journal.LayerChange{ID: id, From: &oldCopy, To: &newCopy})
		}
	}
	return changes
}
