package db

import (
	"maps"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

type Commit struct {
	Revision uint64            `json:"revision"`
	Layers   []catalog.Summary `json:"layers"`
}

func summaries(s *state, names []string) Commit {
	out := Commit{Revision: s.revision, Layers: make([]catalog.Summary, 0, len(names))}
	for _, name := range names {
		out.Layers = append(out.Layers, s.layers[name].summary())
	}
	return out
}

func (d *Database) List() Commit {
	s := d.head.Load()
	return summaries(s, s.names())
}

func (d *Database) Register(def catalog.Definition, expected *uint64) (Commit, error) {
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		if current.layers[def.Name] != nil {
			return nil, conflict("layer %q already exists", def.Name)
		}
		if len(current.layers) == catalog.MaxLayers {
			return nil, exhausted("layer capacity reached")
		}
		l, err := buildLayer(def, revision, make(map[string]catalog.Feature))
		if err != nil {
			return nil, err
		}
		next := &state{revision: revision, layers: maps.Clone(current.layers)}
		next.layers[def.Name] = l
		return next, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{def.Name}), nil
}

func (d *Database) Reframe(name string, layout catalog.Layout, edits []catalog.FeatureEdit, expected *uint64) (Commit, error) {
	if len(edits) > catalog.MaxEdits {
		return Commit{}, invalid("layout change may carry at most %d edits", catalog.MaxEdits)
	}
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		old, err := current.getLayer(name)
		if err != nil {
			return nil, err
		}
		def := old.definition
		def.Layout = layout
		rows := maps.Clone(old.rows)
		for i, edit := range edits {
			// Check the staged values against the final definition so an
			// edit that only fits the new bounds is not rejected early.
			if err := applyFeatureEdit(def, rows, edit, i, "layout edit"); err != nil {
				return nil, err
			}
		}
		if old.definition.Layout == layout && len(edits) == 0 {
			return current, nil
		}
		// buildLayer validates every surviving feature against the final
		// bounds and the final exclusivity policy in one pass, so a rejected
		// change never publishes the new layout with the old features.
		l, err := buildLayer(def, revision, rows)
		if err != nil {
			return nil, err
		}
		next := &state{revision: revision, layers: maps.Clone(current.layers)}
		next.layers[name] = l
		var total int
		for _, other := range next.layers {
			total += len(other.rows)
		}
		if total > catalog.MaxRows {
			return nil, exhausted("database feature capacity reached")
		}
		return next, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{name}), nil
}

func (d *Database) Edit(batch catalog.EditBatch) (Commit, error) {
	if len(batch.Edits) == 0 || len(batch.Edits) > catalog.MaxEdits {
		return Commit{}, invalid("edit batch must contain 1..%d operations", catalog.MaxEdits)
	}
	changed := make([]string, 0)
	s, err := d.commit(batch.IfRevision, func(current *state, revision uint64) (*state, error) {
		staged := make(map[string]map[string]catalog.Feature)
		for i, edit := range batch.Edits {
			l, err := current.getLayer(edit.Layer)
			if err != nil {
				return nil, err
			}
			if staged[edit.Layer] == nil {
				staged[edit.Layer] = maps.Clone(l.rows)
				changed = append(changed, edit.Layer)
			}
			if err := applyEdit(l.definition, staged[edit.Layer], edit, i); err != nil {
				return nil, err
			}
		}
		slices.Sort(changed)
		next := &state{revision: revision, layers: maps.Clone(current.layers)}
		for _, name := range changed {
			l, err := buildLayer(current.layers[name].definition, revision, staged[name])
			if err != nil {
				return nil, err
			}
			next.layers[name] = l
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
		return Commit{}, err
	}
	return summaries(s, changed), nil
}

func applyEdit(def catalog.Definition, rows map[string]catalog.Feature, edit catalog.Edit, index int) error {
	if err := edit.Validate(); err != nil {
		return invalid("edit %d: %v", index, err)
	}
	return applyFeatureEdit(def, rows, catalog.FeatureEdit{ID: edit.ID, Mode: edit.Mode, Value: edit.Value}, index, "edit")
}

// applyFeatureEdit stages one feature operation in rows. Shape and bounds
// checks against the definition supplied by the caller happen here; the
// final cross-feature exclusivity pass stays in buildLayer.
func applyFeatureEdit(def catalog.Definition, rows map[string]catalog.Feature, edit catalog.FeatureEdit, index int, label string) error {
	if err := edit.Validate(); err != nil {
		return invalid("%s %d: %v", label, index, err)
	}
	_, present := rows[edit.ID]
	if edit.Mode == "insert" && present {
		return conflict("%s %d: feature %q already exists", label, index, edit.ID)
	}
	if edit.Mode != "insert" && !present {
		return missing("%s %d: feature %q does not exist", label, index, edit.ID)
	}
	if edit.Mode == "remove" {
		delete(rows, edit.ID)
		return nil
	}
	if err := edit.Value.Validate(def); err != nil {
		return invalid("%s %d: %v", label, index, err)
	}
	rows[edit.ID] = edit.Value.Copy()
	return nil
}
