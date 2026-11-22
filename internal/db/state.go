// Package db publishes immutable catalog views after durable commits.
package db

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
)

type layer struct {
	definition catalog.Definition
	revision   uint64
	rows       map[string]catalog.Feature
	tree       packed.Tree
}

type state struct {
	revision uint64
	layers   map[string]*layer
}

func (l *layer) summary() catalog.Summary {
	return catalog.Summary{Definition: l.definition, Revision: l.revision, Count: len(l.rows)}
}

func buildLayer(def catalog.Definition, revision uint64, rows map[string]catalog.Feature) (*layer, error) {
	if err := def.Validate(); err != nil {
		return nil, invalid("%v", err)
	}
	if len(rows) > catalog.MaxLayerRows {
		return nil, exhausted("layer %s exceeds feature capacity", def.Name)
	}
	l := &layer{definition: def, revision: revision, rows: rows}
	ordered := l.features()
	for _, row := range ordered {
		if err := row.Validate(def); err != nil {
			return nil, invalid("%v", err)
		}
	}
	l.tree = packed.Build(ordered)
	if def.Exclusive {
		left, right, overlap := l.tree.Conflict(ordered)
		if overlap {
			return nil, conflict("exclusive layer %s: %s intersects %s", def.Name, left, right)
		}
	}
	return l, nil
}

func (l *layer) features() []catalog.Feature {
	rows := make([]catalog.Feature, 0, len(l.rows))
	for _, row := range l.rows {
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b catalog.Feature) int { return cmp.Compare(a.ID, b.ID) })
	return rows
}

func (s *state) names() []string {
	names := make([]string, 0, len(s.layers))
	for name := range s.layers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *state) image() catalog.CatalogImage {
	image := catalog.CatalogImage{Revision: s.revision, Layers: make([]catalog.LayerImage, 0, len(s.layers))}
	for _, name := range s.names() {
		l := s.layers[name]
		image.Layers = append(image.Layers, catalog.LayerImage{
			Definition: l.definition,
			Revision:   l.revision,
			Features:   l.features(),
		})
	}
	return image
}

func fromImage(image catalog.CatalogImage) (*state, error) {
	if len(image.Layers) > catalog.MaxLayers || (image.Revision == 0 && len(image.Layers) > 0) {
		return nil, fmt.Errorf("invalid layer count or revision")
	}
	s := &state{revision: image.Revision, layers: make(map[string]*layer)}
	var total int
	var previousLayer string
	for _, entry := range image.Layers {
		name := entry.Definition.Name
		// A layer revision only has to be positive and never exceed the database
		// revision. An incremental package can leave an untouched layer at an older
		// revision while the database revision advances, so equality is not required.
		if name <= previousLayer || entry.Revision == 0 || entry.Revision > s.revision || entry.Features == nil {
			return nil, fmt.Errorf("invalid image layer order or revision for %q", name)
		}
		previousLayer = name
		if len(entry.Features) > catalog.MaxLayerRows {
			return nil, fmt.Errorf("layer %q exceeds feature capacity", name)
		}
		total += len(entry.Features)
		if total > catalog.MaxRows {
			return nil, fmt.Errorf("database exceeds feature capacity")
		}
		rows := make(map[string]catalog.Feature, len(entry.Features))
		var previousID string
		for _, feature := range entry.Features {
			if feature.ID <= previousID {
				return nil, fmt.Errorf("invalid image feature order in %q", name)
			}
			previousID = feature.ID
			rows[feature.ID] = feature
		}
		l, err := buildLayer(entry.Definition, entry.Revision, rows)
		if err != nil {
			return nil, err
		}
		s.layers[name] = l
	}
	return s, nil
}

func (s *state) getLayer(name string) (*layer, error) {
	l := s.layers[name]
	if l == nil {
		return nil, missing("layer %q does not exist", name)
	}
	return l, nil
}
