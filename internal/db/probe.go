package db

import (
	"fmt"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

// ImageProbe is the offline inspection result for one MPB v1 image. It never
// opens a data directory and never takes a write lock, so it can validate
// backup archives while the production service keeps running.
type ImageProbe struct {
	Revision     uint64            `json:"revision"`
	LayerCount   int               `json:"layer_count"`
	FeatureCount int               `json:"feature_count"`
	Layers       []catalog.Summary `json:"layers"`
	Bytes        int               `json:"bytes"`
	SHA256       string            `json:"sha256"`
}

// InspectImage runs every check the server applies at restart: signature,
// length, SHA-256, canonical JSON, structure, revision ordering and geometry
// (bounds, shapes and exclusive-layer conflicts), then rebuilds every index.
func InspectImage(raw []byte) (ImageProbe, error) {
	var probe ImageProbe
	if len(raw) > folio.MaxImage {
		return probe, fmt.Errorf("image exceeds maximum size")
	}
	probe.Bytes = len(raw)
	probe.SHA256 = folio.Digest(raw)
	image, err := folio.Decode(raw)
	if err != nil {
		return probe, err
	}
	s, err := fromImage(image)
	if err != nil {
		return probe, err
	}
	probe.Revision = s.revision
	probe.LayerCount = len(s.layers)
	probe.Layers = make([]catalog.Summary, 0, len(s.layers))
	for _, name := range s.names() {
		l := s.layers[name]
		probe.FeatureCount += len(l.rows)
		probe.Layers = append(probe.Layers, l.summary())
	}
	return probe, nil
}
