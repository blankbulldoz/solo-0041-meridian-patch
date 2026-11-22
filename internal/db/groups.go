package db

import (
	"fmt"
	"maps"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// GroupCommit reports the committed revision, the layers changed by accepted
// groups, and one outcome per declared group in request order.
type GroupCommit struct {
	Commit
	Groups []GroupOutcome `json:"groups"`
}

type GroupOutcome struct {
	Index    int            `json:"index"`
	Status   string         `json:"status"`
	Conflict *GroupConflict `json:"conflict,omitempty"`
}

// GroupConflict explains why a group was skipped: either it moved a feature
// an earlier accepted group already moved, or it selected an exclusive range
// the staged baseline already holds.
type GroupConflict struct {
	Reason     string `json:"reason"`
	Layer      string `json:"layer"`
	Feature    string `json:"feature,omitempty"`
	AcceptedBy *int   `json:"accepted_by,omitempty"`
	First      string `json:"first,omitempty"`
	Second     string `json:"second,omitempty"`
	Detail     string `json:"detail"`
}

type featureRef struct {
	layer string
	id    string
}

func acceptedGroup(index int) GroupOutcome {
	return GroupOutcome{Index: index, Status: "accepted"}
}

func skippedMovedGroup(index int, layer, id string, acceptedBy int) GroupOutcome {
	return GroupOutcome{
		Index:  index,
		Status: "skipped",
		Conflict: &GroupConflict{
			Reason:     "feature_moved",
			Layer:      layer,
			Feature:    id,
			AcceptedBy: &acceptedBy,
			Detail:     fmt.Sprintf("feature %q in layer %q was already moved by group %d", id, layer, acceptedBy),
		},
	}
}

func skippedExclusiveGroup(index int, layer string, pair conflictPair) GroupOutcome {
	return GroupOutcome{
		Index:  index,
		Status: "skipped",
		Conflict: &GroupConflict{
			Reason: "exclusive_range",
			Layer:  layer,
			First:  pair.left,
			Second: pair.right,
			Detail: fmt.Sprintf("exclusive layer %s: %s intersects %s", layer, pair.left, pair.right),
		},
	}
}

// EditGroups applies an import batch. Groups are indivisible and are evaluated
// in request order against a baseline of the committed state plus every
// previously accepted group. A group that moves a feature an earlier accepted
// group already moved, or that selects an exclusive range the baseline already
// holds, is skipped as a whole. Accepted groups land together in a single new
// revision; when every group is skipped the revision stays unchanged.
func (d *Database) EditGroups(batch catalog.GroupBatch) (GroupCommit, error) {
	if len(batch.Groups) == 0 || len(batch.Groups) > catalog.MaxGroups {
		return GroupCommit{}, invalid("group batch must contain 1..%d groups", catalog.MaxGroups)
	}
	var operations int
	for index, group := range batch.Groups {
		if len(group.Edits) == 0 {
			return GroupCommit{}, invalid("group %d must contain at least one edit", index)
		}
		operations += len(group.Edits)
		for item, edit := range group.Edits {
			if err := edit.Validate(); err != nil {
				return GroupCommit{}, invalid("group %d edit %d: %v", index, item, err)
			}
		}
	}
	if operations > catalog.MaxEdits {
		return GroupCommit{}, invalid("group batch must contain at most %d operations", catalog.MaxEdits)
	}
	outcomes := make([]GroupOutcome, len(batch.Groups))
	changed := make([]string, 0)
	s, err := d.commit(batch.IfRevision, func(current *state, revision uint64) (*state, error) {
		staged := make(map[string]*layer)
		moved := make(map[featureRef]int)
		accepted := 0
		for index, group := range batch.Groups {
			if outcome, skip := groupMoved(group, moved, index); skip {
				outcomes[index] = outcome
				continue
			}
			rows, err := stageGroup(current, staged, group, index)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(rows))
			for name := range rows {
				names = append(names, name)
			}
			slices.Sort(names)
			built := make(map[string]*layer, len(names))
			var outcome GroupOutcome
			skip := false
			for _, name := range names {
				l, pair, err := stageLayer(current.layers[name].definition, revision, rows[name])
				if err != nil {
					return nil, err
				}
				if pair != nil {
					outcome = skippedExclusiveGroup(index, name, *pair)
					skip = true
					break
				}
				built[name] = l
			}
			if skip {
				outcomes[index] = outcome
				continue
			}
			for _, name := range names {
				if staged[name] == nil {
					changed = append(changed, name)
				}
				staged[name] = built[name]
			}
			for _, edit := range group.Edits {
				moved[featureRef{edit.Layer, edit.ID}] = index
			}
			accepted++
			outcomes[index] = acceptedGroup(index)
		}
		if accepted == 0 {
			return current, nil
		}
		slices.Sort(changed)
		next := &state{revision: revision, layers: maps.Clone(current.layers)}
		for _, name := range changed {
			next.layers[name] = staged[name]
		}
		var total int
		for _, l := range next.layers {
			total += len(l.rows)
		}
		if total > catalog.MaxRows {
			return nil, exhausted("database feature capacity reached")
		}
		return next, nil
	})
	if err != nil {
		return GroupCommit{}, err
	}
	return GroupCommit{Commit: summaries(s, changed), Groups: outcomes}, nil
}

// groupMoved reports whether any edit in the group moves a feature that an
// earlier accepted group already moved, and if so explains the skip.
func groupMoved(group catalog.EditGroup, moved map[featureRef]int, index int) (GroupOutcome, bool) {
	for _, edit := range group.Edits {
		if earlier, ok := moved[featureRef{edit.Layer, edit.ID}]; ok {
			return skippedMovedGroup(index, edit.Layer, edit.ID, earlier), true
		}
	}
	return GroupOutcome{}, false
}

// stageGroup applies the group's edits in order to per-layer copies of the
// staged baseline rows. State errors reject the whole request, as in Edit.
func stageGroup(current *state, staged map[string]*layer, group catalog.EditGroup, index int) (map[string]map[string]catalog.Feature, error) {
	rows := make(map[string]map[string]catalog.Feature)
	for item, edit := range group.Edits {
		base := staged[edit.Layer]
		if base == nil {
			l, err := current.getLayer(edit.Layer)
			if err != nil {
				return nil, err
			}
			base = l
		}
		if rows[edit.Layer] == nil {
			rows[edit.Layer] = maps.Clone(base.rows)
		}
		if err := applyGroupEdit(base.definition, rows[edit.Layer], edit, index, item); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func applyGroupEdit(def catalog.Definition, rows map[string]catalog.Feature, edit catalog.Edit, group, item int) error {
	_, present := rows[edit.ID]
	if edit.Mode == "insert" && present {
		return conflict("group %d edit %d: feature %q already exists", group, item, edit.ID)
	}
	if edit.Mode != "insert" && !present {
		return missing("group %d edit %d: feature %q does not exist", group, item, edit.ID)
	}
	if edit.Mode == "remove" {
		delete(rows, edit.ID)
		return nil
	}
	if err := edit.Value.Validate(def); err != nil {
		return invalid("group %d edit %d: %v", group, item, err)
	}
	rows[edit.ID] = edit.Value.Copy()
	return nil
}
