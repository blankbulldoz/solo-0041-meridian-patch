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
		if l := s.layers[name]; l != nil {
			out.Layers = append(out.Layers, l.summary())
		}
	}
	return out
}

func (d *Database) List() Commit {
	s := d.head.Load()
	return summaries(s, s.names())
}

// The apply* helpers interpret a change against an immutable view and produce
// the next immutable view at revision. Formal commits and temporary branches
// both use them, so a branch pack has exactly the same staging, ordering and
// final-state checks as a durable pack.
func applyRegister(current *state, revision uint64, def catalog.Definition) (*state, []string, error) {
	if current.layers[def.Name] != nil {
		return nil, nil, conflict("layer %q already exists", def.Name)
	}
	if len(current.layers) == catalog.MaxLayers {
		return nil, nil, exhausted("layer capacity reached")
	}
	l, err := buildLayer(def, revision, make(map[string]catalog.Feature))
	if err != nil {
		return nil, nil, err
	}
	next := &state{revision: revision, branch: current.branch, layers: maps.Clone(current.layers)}
	next.layers[def.Name] = l
	return next, []string{def.Name}, nil
}

func applyReframe(current *state, revision uint64, name string, layout catalog.Layout) (*state, []string, error) {
	old, err := current.getLayer(name)
	if err != nil {
		return nil, nil, err
	}
	if old.definition.Layout == layout {
		return current, nil, nil
	}
	def := old.definition
	def.Layout = layout
	l, err := buildLayer(def, revision, old.rows)
	if err != nil {
		return nil, nil, err
	}
	next := &state{revision: revision, branch: current.branch, layers: maps.Clone(current.layers)}
	next.layers[name] = l
	return next, []string{name}, nil
}

func applyEditBatch(current *state, revision uint64, batch catalog.EditBatch) (*state, []string, error) {
	if len(batch.Edits) == 0 || len(batch.Edits) > catalog.MaxEdits {
		return nil, nil, invalid("edit batch must contain 1..%d operations", catalog.MaxEdits)
	}
	staged := make(map[string]map[string]catalog.Feature)
	changed := make([]string, 0)
	for i, edit := range batch.Edits {
		l, err := current.getLayer(edit.Layer)
		if err != nil {
			return nil, nil, err
		}
		if staged[edit.Layer] == nil {
			staged[edit.Layer] = maps.Clone(l.rows)
			changed = append(changed, edit.Layer)
		}
		if err := applyEdit(l.definition, staged[edit.Layer], edit, i); err != nil {
			return nil, nil, err
		}
	}
	slices.Sort(changed)
	next := &state{revision: revision, branch: current.branch, layers: maps.Clone(current.layers)}
	for _, name := range changed {
		l, err := buildLayer(current.layers[name].definition, revision, staged[name])
		if err != nil {
			return nil, nil, err
		}
		next.layers[name] = l
	}
	var total int
	for _, l := range next.layers {
		total += len(l.rows)
	}
	if total > catalog.MaxRows {
		return nil, nil, exhausted("database feature capacity reached")
	}
	return next, changed, nil
}

func (d *Database) Register(def catalog.Definition, expected *uint64) (Commit, error) {
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		next, _, err := applyRegister(current, revision, def)
		return next, err
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{def.Name}), nil
}

func (d *Database) Reframe(name string, layout catalog.Layout, expected *uint64) (Commit, error) {
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		next, _, err := applyReframe(current, revision, name, layout)
		return next, err
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{name}), nil
}

func (d *Database) Edit(batch catalog.EditBatch) (Commit, error) {
	var changed []string
	s, err := d.commit(batch.IfRevision, func(current *state, revision uint64) (*state, error) {
		next, names, err := applyEditBatch(current, revision, batch)
		changed = names
		return next, err
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
