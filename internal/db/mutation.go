package db

import (
	"maps"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

type Commit struct {
	Revision uint64            `json:"revision"`
	Layers   []catalog.Summary `json:"layers"`
	// Replayed reports that a repeated request identifier returned the original
	// commit instead of applying the batch again.
	Replayed bool `json:"replayed,omitempty"`
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
		next := &state{revision: revision, layers: maps.Clone(current.layers), requests: current.requests}
		next.layers[def.Name] = l
		return next, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{def.Name}), nil
}

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
		next := &state{revision: revision, layers: maps.Clone(current.layers), requests: current.requests}
		next.layers[name] = l
		return next, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, []string{name}), nil
}

func (d *Database) Edit(batch catalog.EditBatch) (Commit, error) {
	if batch.RequestID != "" && !catalog.ValidRequestID(batch.RequestID) {
		return Commit{}, invalid("request_id must be 1..64 characters of letters, digits, '_' or '-'")
	}
	if len(batch.Edits) == 0 || len(batch.Edits) > catalog.MaxEdits {
		return Commit{}, invalid("edit batch must contain 1..%d operations", catalog.MaxEdits)
	}
	digest := ""
	if batch.RequestID != "" {
		digest = catalog.EditDigest(batch)
	}
	// The unlocked probe answers the common retry without contending with
	// readers or writers. It is only an optimisation: the write lock is taken
	// regardless, and the ledger is re-checked inside that critical section, so
	// two copies of one identifier can never both be applied.
	if replay, found, err := d.lookupReplay(d.head.Load(), batch.RequestID, digest); found || err != nil {
		return replay, err
	}
	d.write.Lock()
	defer d.write.Unlock()
	if replay, found, err := d.lookupReplay(d.head.Load(), batch.RequestID, digest); found || err != nil {
		return replay, err
	}
	changed := make([]string, 0)
	s, err := d.commitLocked(batch.IfRevision, func(current *state, revision uint64) (*state, error) {
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
		next := &state{revision: revision, layers: maps.Clone(current.layers), requests: current.requests}
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
		if batch.RequestID != "" {
			record := catalog.RequestRecord{ID: batch.RequestID, Digest: digest, Revision: revision}
			for _, name := range changed {
				record.Layers = append(record.Layers, next.layers[name].summary())
			}
			next.requests = catalog.AppendRequest(current.requests, record)
		}
		return next, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, changed), nil
}

func (d *Database) lookupReplay(current *state, id, digest string) (Commit, bool, error) {
	if id == "" {
		return Commit{}, false, nil
	}
	record, found := catalog.FindRequest(current.requests, id)
	if !found {
		return Commit{}, false, nil
	}
	if record.Digest != digest {
		return Commit{}, true, conflict("request_id %q was already used with different content", id)
	}
	return Commit{Revision: record.Revision, Layers: slices.Clone(record.Layers), Replayed: true}, true, nil
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
