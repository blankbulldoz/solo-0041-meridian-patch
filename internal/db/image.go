package db

import (
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func readImage(raw []byte) (*state, error) {
	image, err := folio.Decode(raw)
	if err != nil {
		return nil, err
	}
	return fromImage(image)
}

func (d *Database) Export() ([]byte, uint64, error) {
	return d.ExportSelection(ExportRequest{})
}

// ExportRequest selects a subset of layers and optionally a single half-open
// window. A nil Window exports whole layers; an empty Layers list means every
// layer. The request is resolved against one immutable snapshot.
type ExportRequest struct {
	Layers []string
	Window *plane.Box
}

// ExportSelection encodes a portable MPB v1 image for the requested layers
// and window. Marks are selected by half-open ownership and extents are
// clipped to the window's positive-area intersection. The result remains a
// canonical image that the existing restore path can validate and install.
func (d *Database) ExportSelection(request ExportRequest) ([]byte, uint64, error) {
	if err := validateExportRequest(request); err != nil {
		return nil, 0, err
	}
	s := d.head.Load()
	names := s.names()
	if len(request.Layers) > 0 {
		// MPB v1 layers are canonical by name; selection order in the request
		// is intentionally ignored so the image stays recoverable.
		names = append(make([]string, 0, len(request.Layers)), request.Layers...)
		slices.Sort(names)
	}
	image := catalog.CatalogImage{Revision: s.revision, Layers: make([]catalog.LayerImage, 0, len(names))}
	for _, name := range names {
		l, err := s.getLayer(name)
		if err != nil {
			return nil, 0, err
		}
		image.Layers = append(image.Layers, layerExportImage(l, request.Window))
	}
	raw, err := folio.Encode(image)
	return raw, s.revision, err
}

func validateExportRequest(request ExportRequest) error {
	seen := make(map[string]bool, len(request.Layers))
	for _, name := range request.Layers {
		if !catalog.ValidName(name) {
			return invalid("invalid export layer name %q", name)
		}
		if seen[name] {
			return invalid("export layer %q is selected more than once", name)
		}
		seen[name] = true
	}
	if request.Window != nil {
		if err := request.Window.Validate(); err != nil {
			return invalid("%v", err)
		}
	}
	return nil
}

// layerExportImage projects one layer onto the optional window. Layer bounds
// use the same half-open interpretation as feature selection: when the source
// bounds and the window share a positive-area rectangle the layer is reframed
// to that intersection; otherwise the layer stays empty with its source
// bounds, which remain the smallest enclosing valid box for every emitted row.
func layerExportImage(l *layer, window *plane.Box) catalog.LayerImage {
	definition := l.definition
	features := make([]catalog.Feature, 0)
	if window != nil {
		if clipped, intersects := l.definition.Bounds.Clip(*window); intersects {
			definition.Bounds = clipped
			for _, row := range l.features() {
				if projected, visible := row.Shape.Clip(*window); visible {
					row.Shape = projected
					features = append(features, row)
				}
			}
		}
	} else {
		for _, row := range l.features() {
			features = append(features, row.Copy())
		}
	}
	return catalog.LayerImage{Definition: definition, Revision: l.revision, Features: features}
}

func (d *Database) Restore(raw []byte, expected uint64) (Commit, error) {
	candidate, err := readImage(raw)
	if err != nil {
		return Commit{}, invalid("restore image: %v", err)
	}
	s, err := d.commit(&expected, func(_ *state, revision uint64) (*state, error) {
		candidate.revision = revision
		for _, l := range candidate.layers {
			l.revision = revision
		}
		return candidate, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, s.names()), nil
}
