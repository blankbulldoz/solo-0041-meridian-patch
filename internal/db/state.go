// Package db publishes immutable catalog views after durable commits.
package db

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
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
	quotas   map[string]catalog.Quota
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

func (s *state) quotaNames() []string {
	names := make([]string, 0, len(s.quotas))
	for name := range s.quotas {
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
	for _, name := range s.quotaNames() {
		image.Quotas = append(image.Quotas, s.quotas[name])
	}
	return image
}

func fromImage(image catalog.CatalogImage) (*state, error) {
	if len(image.Layers) > catalog.MaxLayers || (image.Revision == 0 && len(image.Layers) > 0) {
		return nil, fmt.Errorf("invalid layer count or revision")
	}
	if len(image.Quotas) > catalog.MaxQuotas {
		return nil, fmt.Errorf("invalid quota count")
	}
	s := &state{revision: image.Revision, layers: make(map[string]*layer), quotas: make(map[string]catalog.Quota)}
	var total int
	var previousLayer string
	for _, entry := range image.Layers {
		name := entry.Definition.Name
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
	var previousQuota string
	for _, quota := range image.Quotas {
		if quota.Name <= previousQuota {
			return nil, fmt.Errorf("invalid image quota order for %q", quota.Name)
		}
		previousQuota = quota.Name
		if err := quota.Validate(); err != nil {
			return nil, fmt.Errorf("invalid image quota: %v", err)
		}
		for i := 1; i < len(quota.Layers); i++ {
			if quota.Layers[i] <= quota.Layers[i-1] {
				return nil, fmt.Errorf("invalid image quota layer order for %q", quota.Name)
			}
		}
		for _, name := range quota.Layers {
			l := s.layers[name]
			if l == nil {
				return nil, fmt.Errorf("image quota %q references missing layer %q", quota.Name, name)
			}
			if l.definition.Kind != plane.Extent {
				return nil, fmt.Errorf("image quota %q layer %q is not an extent layer", quota.Name, name)
			}
		}
		s.quotas[quota.Name] = quota
	}
	for _, name := range s.quotaNames() {
		quota := s.quotas[name]
		if area := s.quotaArea(quota); area > quota.MaxArea {
			return nil, fmt.Errorf("image quota %q coverage %d exceeds cap %d", name, area, quota.MaxArea)
		}
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
