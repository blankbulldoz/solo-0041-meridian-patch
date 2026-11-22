package delta

import (
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
)

// ValidateImage applies the full set of structural, capacity, geometry,
// exclusivity and revision rules that a database enforces when it commits an
// image. It backs the offline CLI path, where a reconstructed image has already
// been assigned concrete revisions.
func ValidateImage(image catalog.CatalogImage) error {
	if len(image.Layers) > catalog.MaxLayers {
		return malformed("image exceeds %d layers", catalog.MaxLayers)
	}
	if image.Revision == 0 && len(image.Layers) > 0 {
		return malformed("non-empty image must carry a non-zero revision")
	}
	return validateContent(image)
}

// validateContent checks everything except revision bookkeeping. A freshly
// reconstructed image intentionally carries revision zero until the importer
// assigns real revisions, so the Apply path calls this variant directly.
func validateContent(image catalog.CatalogImage) error {
	if len(image.Layers) > catalog.MaxLayers {
		return malformed("image exceeds %d layers", catalog.MaxLayers)
	}
	var total int
	var previousLayer string
	for _, entry := range image.Layers {
		name := entry.Definition.Name
		if name <= previousLayer || entry.Features == nil {
			return malformed("invalid layer order for %q", name)
		}
		previousLayer = name
		if len(entry.Features) > catalog.MaxLayerRows {
			return malformed("layer %q exceeds feature capacity", name)
		}
		total += len(entry.Features)
		if total > catalog.MaxRows {
			return malformed("database exceeds feature capacity")
		}
		rows := make(map[string]catalog.Feature, len(entry.Features))
		var previousID string
		for _, feature := range entry.Features {
			if feature.ID <= previousID {
				return malformed("invalid feature order in %q", name)
			}
			previousID = feature.ID
			if err := feature.Validate(entry.Definition); err != nil {
				return malformed("%v", err)
			}
			rows[feature.ID] = feature
		}
		if entry.Definition.Exclusive {
			tree := packed.Build(entry.Features)
			left, right, overlap := tree.Conflict(entry.Features)
			if overlap {
				return malformed("exclusive layer %s: %s intersects %s", name, left, right)
			}
		}
	}
	return nil
}
