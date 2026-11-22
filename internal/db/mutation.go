package db

import (
	"maps"

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

// Reframe applies a PUT /layout request. There are no per-item operations to
// interpret: the existing rows are handed straight to finalization, so the
// final definition (including a shrunk bounds box) is what decides whether
// each committed feature still belongs in the layer.
func (d *Database) Reframe(name string, layout catalog.Layout, expected *uint64) (Commit, error) {
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		old, err := current.getLayer(name)
		if err != nil {
			return nil, err
		}
		if old.definition.Layout == layout {
			return current, nil
		}
		def := old.definition
		def.Layout = layout
		l, err := buildLayer(def, revision, old.rows)
		if err != nil {
			return nil, err
		}
		next := &state{revision: revision, layers: maps.Clone(current.layers)}
		next.layers[name] = l
		return next, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{name}), nil
}

// Edit applies a POST /edits batch in two explicit phases. Interpretation runs
// the batch in order on private working copies, then finalization rebuilds the
// affected layers and checks the stage-end rules once over the resulting
// catalog. A rejection in either phase aborts the batch and keeps the old
// revision readable.
func (d *Database) Edit(batch catalog.EditBatch) (Commit, error) {
	if len(batch.Edits) == 0 || len(batch.Edits) > catalog.MaxEdits {
		return Commit{}, invalid("edit batch must contain 1..%d operations", catalog.MaxEdits)
	}
	changed := make([]string, 0)
	s, err := d.commit(batch.IfRevision, func(current *state, revision uint64) (*state, error) {
		// Phase one: interpret each operation against the evolving working copies.
		staged, touched, err := stageEdits(current, batch.Edits)
		if err != nil {
			return nil, err
		}
		changed = touched
		// Phase two: rebuild every affected layer and apply the stage-end checks
		// to the final catalog, including the database-wide capacity.
		layers, err := rebuildLayers(current, revision, changed, staged.rows)
		if err != nil {
			return nil, err
		}
		if err := checkDatabaseCapacity(layers); err != nil {
			return nil, err
		}
		return &state{revision: revision, layers: layers}, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, changed), nil
}
