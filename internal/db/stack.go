package db

import (
	"errors"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// StackLayer selects one layer of an occlusion query. The request array order
// is the stacking order: the first entry is the topmost layer.
type StackLayer struct {
	Layer string `json:"layer"`
	Class string `json:"class,omitempty"`
}

// StackQuery evaluates visible exposure inside Bounds across ordered layers.
type StackQuery struct {
	Bounds plane.Box    `json:"bounds"`
	Layers []StackLayer `json:"layers"`
}

// StackLayerReport stamps one layer's exposure with its catalog revision.
type StackLayerReport struct {
	LayerRevision uint64 `json:"layer_revision"`
	spatial.LayerExposure
}

// StackResponse keeps the reports in the requested order and adds the shared
// immutable database revision, so equal areas from different layers remain
// separately attributable.
type StackResponse struct {
	Revision uint64             `json:"revision"`
	Layers   []StackLayerReport `json:"layers"`
	Visit    packed.Visit       `json:"visit"`
}

func (d *Database) Stack(q StackQuery) (StackResponse, error) {
	if err := validateWindow(q.Bounds, ""); err != nil {
		return StackResponse{}, err
	}
	if len(q.Layers) < 2 || len(q.Layers) > catalog.MaxLayers {
		return StackResponse{}, invalid("stack requires 2..%d ordered layers", catalog.MaxLayers)
	}
	seen := make(map[string]bool, len(q.Layers))
	for _, entry := range q.Layers {
		if !catalog.ValidName(entry.Layer) || (entry.Class != "" && !catalog.ValidName(entry.Class)) {
			return StackResponse{}, invalid("stack requires valid layer and class names")
		}
		if seen[entry.Layer] {
			return StackResponse{}, invalid("stack layer %q appears more than once", entry.Layer)
		}
		seen[entry.Layer] = true
	}
	s := d.head.Load()
	entries := make([]spatial.StackEntry, len(q.Layers))
	reports := make([]StackLayerReport, len(q.Layers))
	for i, entry := range q.Layers {
		l, err := s.getLayer(entry.Layer)
		if err != nil {
			return StackResponse{}, err
		}
		if l.definition.Kind != plane.Extent {
			return StackResponse{}, invalid("stack requires extent layer %q", entry.Layer)
		}
		entries[i] = spatial.StackEntry{Name: entry.Layer, Class: entry.Class, Tree: l.tree}
		reports[i] = StackLayerReport{LayerRevision: l.revision}
	}
	result, err := spatial.Stack(entries, q.Bounds)
	if errors.Is(err, spatial.ErrStackBudget) {
		return StackResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return StackResponse{}, err
	}
	for i := range result.Layers {
		reports[i].LayerExposure = result.Layers[i]
	}
	return StackResponse{Revision: s.revision, Layers: reports, Visit: result.Visit}, nil
}
