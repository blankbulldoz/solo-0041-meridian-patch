package db

import (
	"maps"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// This file owns the boundary between the two phases of a mutating request.
//
// Interpretation (stageEdits/applyEdit) walks the request in input order and
// applies each operation to a private, per-layer working copy. The rules
// enforced here are local to one operation: its shape, its mode against the
// rows currently visible in that working copy, and the layer's existing
// definition. Later operations deliberately observe features created by
// earlier operations in the same batch.
//
// Finalization (rebuildLayers/checkDatabaseCapacity) runs only after the whole
// request has been interpreted. It rebuilds every affected layer through
// buildLayer, which applies the stage-end rules (capacity, every feature
// against the final definition, and the exclusive policy over the final
// catalog). Keeping these phases separate makes it explicit which rule fires
// per item and which rule fires once on the resulting state.

// stagedBatch holds the per-layer working copies produced while interpreting
// an edit batch, keyed by layer name.
type stagedBatch struct {
	rows map[string]map[string]catalog.Feature
}

// stageEdits interprets a batch in input order. Each touched layer starts from
// a clone of the committed rows, so a rejected batch leaves the published
// state untouched, and later edits see the output of earlier edits.
func stageEdits(current *state, edits []catalog.Edit) (stagedBatch, []string, error) {
	staged := stagedBatch{rows: make(map[string]map[string]catalog.Feature)}
	changed := make([]string, 0)
	for i, edit := range edits {
		l, err := current.getLayer(edit.Layer)
		if err != nil {
			return stagedBatch{}, nil, err
		}
		if staged.rows[edit.Layer] == nil {
			staged.rows[edit.Layer] = maps.Clone(l.rows)
			changed = append(changed, edit.Layer)
		}
		if err := applyEdit(l.definition, staged.rows[edit.Layer], edit, i); err != nil {
			return stagedBatch{}, nil, err
		}
	}
	slices.Sort(changed)
	return staged, changed, nil
}

// applyEdit enforces the interpretation-time rules for one operation against
// the working copy, including presence requirements that depend on what prior
// operations in the same batch already inserted, replaced or removed.
func applyEdit(def catalog.Definition, rows map[string]catalog.Feature, edit catalog.Edit, index int) error {
	if err := edit.Validate(); err != nil {
		return invalid("edit %d: %v", index, err)
	}
	_, present := rows[edit.ID]
	if edit.Mode == "insert" && present {
		return conflict("edit %d: feature %q already exists", index, edit.ID)
	}
	if edit.Mode != "insert" && !present {
		return missing("edit %d: feature %q does not exist", index, edit.ID)
	}
	if edit.Mode == "remove" {
		delete(rows, edit.ID)
		return nil
	}
	if err := edit.Value.Validate(def); err != nil {
		return invalid("edit %d: %v", index, err)
	}
	rows[edit.ID] = edit.Value.Copy()
	return nil
}

// rebuildLayers is the stage-end finalization for the named layers. It builds
// new immutable layer views from the staged rows, running every reusable
// final-state check (capacity, feature validity against the final definition,
// and the exclusive policy) through buildLayer before the state is published.
func rebuildLayers(current *state, revision uint64, names []string, rows map[string]map[string]catalog.Feature) (map[string]*layer, error) {
	next := maps.Clone(current.layers)
	for _, name := range names {
		l, err := buildLayer(current.layers[name].definition, revision, rows[name])
		if err != nil {
			return nil, err
		}
		next[name] = l
	}
	return next, nil
}

// checkDatabaseCapacity is a whole-database stage-end check: only the final
// row totals across every layer decide whether the database-wide cap applies.
func checkDatabaseCapacity(layers map[string]*layer) error {
	var total int
	for _, l := range layers {
		total += len(l.rows)
	}
	if total > catalog.MaxRows {
		return exhausted("database feature capacity reached")
	}
	return nil
}
