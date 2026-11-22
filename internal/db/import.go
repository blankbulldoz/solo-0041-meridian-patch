package db

import (
	"maps"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// Import applies one integer grid transform to every item and commits all
// touched layers in a single revision. The stages mirror Edit so that
// import failures leave the previous view readable:
//
//  1. validate batch structure and the transform itself;
//  2. resolve every target layer, transforming each shape in input order;
//  3. reject duplicate IDs within the batch or collisions with existing
//     features (imports are inserts, never silent replacements);
//  4. rebuild each touched layer, re-running geometry, bounds and
//     exclusivity checks against the target definitions;
//  5. enforce the whole-database feature capacity before publishing.
func (d *Database) Import(batch catalog.ImportBatch) (Commit, error) {
	if err := batch.Validate(); err != nil {
		return Commit{}, invalid("%v", err)
	}
	changed := make([]string, 0)
	s, err := d.commit(batch.IfRevision, func(current *state, revision uint64) (*state, error) {
		staged := make(map[string]map[string]catalog.Feature)
		for i, item := range batch.Items {
			l, err := current.getLayer(item.Layer)
			if err != nil {
				return nil, err
			}
			if staged[item.Layer] == nil {
				staged[item.Layer] = maps.Clone(l.rows)
				changed = append(changed, item.Layer)
			}
			feature, err := item.Value.ApplyTransform(batch.Transform)
			if err != nil {
				return nil, invalid("import item %d: %v", i, err)
			}
			rows := staged[item.Layer]
			if _, exists := rows[feature.ID]; exists {
				return nil, conflict("import item %d: feature %q already exists in layer %q", i, feature.ID, item.Layer)
			}
			if err := feature.Validate(l.definition); err != nil {
				return nil, invalid("import item %d: %v", i, err)
			}
			rows[feature.ID] = feature
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
