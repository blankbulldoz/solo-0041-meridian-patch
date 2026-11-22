// Package archive converts MPB v1 images into plain-data archives that an
// archiver can store, diff, and check without the database process. Every
// row carries a content-based identity digest, and revisions together with
// coordinate-derived quantities are emitted as decimal text so values beyond
// the JavaScript safe integer range (2^53-1) survive the transfer intact.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// Format identifies the plain-data archive layout written by Unpack.
const Format = "mpb-archive/v1"

// Model is the validated content of one MPB image in canonical order.
type Model struct {
	Revision uint64
	Layers   []Layer
	Source   string // file or directory the model was loaded from, for reports
}

// Layer is one validated layer with its features sorted by ID.
type Layer struct {
	Definition catalog.Definition
	Revision   uint64
	Features   []catalog.Feature
}

// Features returns the total number of feature rows in the model.
func (m *Model) Features() int {
	total := 0
	for _, l := range m.Layers {
		total += len(l.Features)
	}
	return total
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func boxFields(b plane.Box) string {
	return strings.Join([]string{
		strconv.FormatInt(b.MinX, 10), strconv.FormatInt(b.MinY, 10),
		strconv.FormatInt(b.MaxX, 10), strconv.FormatInt(b.MaxY, 10),
	}, ",")
}

func shapeFields(s plane.Shape) string {
	if s.Mark != nil {
		return "mark:" + strconv.FormatInt(s.Mark.X, 10) + "," + strconv.FormatInt(s.Mark.Y, 10)
	}
	return "extent:" + boxFields(*s.Extent)
}

// LayerDigest identifies one layer definition row. Revisions and feature
// membership are deliberately excluded so the row keeps its identity across
// commits, re-exports, and restores.
func LayerDigest(d catalog.Definition) string {
	return digest("mpb-archive/layer/v1", d.Name, string(d.Kind), boxFields(d.Bounds),
		strconv.FormatBool(d.Exclusive))
}

// FeatureDigest identifies one feature row within its layer. The digest
// covers the layer name, feature ID, class, and shape coordinates, so a row
// can be matched across archives even when every revision changed.
func FeatureDigest(layer string, f catalog.Feature) string {
	return digest("mpb-archive/feature/v1", layer, f.ID, f.Class, shapeFields(f.Shape))
}

// ClassDigest folds the sorted feature digests of one (class, kind) group
// into a single order-independent checksum for the summary table.
func ClassDigest(class string, kind plane.Kind, members []string) string {
	parts := append([]string{"mpb-archive/class/v1", class, string(kind)}, members...)
	return digest(parts...)
}

// Envelope returns the bounding box of a feature; a mark has a degenerate
// envelope with Min == Max.
func Envelope(f catalog.Feature) plane.Box {
	return f.Shape.Envelope()
}

// Area returns the envelope area: zero for marks, positive for extents. With
// coordinates up to 10^9 the area reaches 4*10^18, beyond 2^53-1.
func Area(f catalog.Feature) int64 {
	return f.Shape.Envelope().Area()
}

// OriginDistanceSquared returns the squared distance from the shape closure
// to the coordinate origin. It reaches 8*10^18, beyond 2^53-1.
func OriginDistanceSquared(f catalog.Feature) int64 {
	return f.Shape.Envelope().DistanceSquared(plane.Point{X: 0, Y: 0})
}

// Decode parses and fully validates raw MPB v1 bytes: signature, length,
// SHA-256, canonical JSON, catalog rules, geometry rules, and the exclusive
// policy — the same facts the database checks when restoring an image.
func Decode(raw []byte) (*Model, error) {
	image, err := folio.Decode(raw)
	if err != nil {
		return nil, err
	}
	return Validate(image)
}

// Validate checks an already-decoded image against the revision, ordering,
// capacity, geometry, and exclusion rules applied on database restore, and
// returns the canonical model.
func Validate(image catalog.CatalogImage) (*Model, error) {
	if len(image.Layers) > catalog.MaxLayers || (image.Revision == 0 && len(image.Layers) > 0) {
		return nil, fmt.Errorf("invalid layer count or revision")
	}
	m := &Model{Revision: image.Revision, Layers: make([]Layer, 0, len(image.Layers))}
	total := 0
	previousLayer := ""
	for _, entry := range image.Layers {
		def := entry.Definition
		if err := def.Validate(); err != nil {
			return nil, err
		}
		name := def.Name
		if name <= previousLayer || entry.Revision == 0 || entry.Revision > image.Revision || entry.Features == nil {
			return nil, fmt.Errorf("invalid image layer order or revision for %q", name)
		}
		previousLayer = name
		if len(entry.Features) > catalog.MaxLayerRows {
			return nil, fmt.Errorf("layer %s exceeds feature capacity", name)
		}
		total += len(entry.Features)
		if total > catalog.MaxRows {
			return nil, fmt.Errorf("database exceeds feature capacity")
		}
		previousID := ""
		for _, f := range entry.Features {
			if f.ID <= previousID {
				return nil, fmt.Errorf("invalid image feature order in %q", name)
			}
			previousID = f.ID
			if err := f.Validate(def); err != nil {
				return nil, err
			}
		}
		if def.Exclusive {
			tree := packed.Build(entry.Features)
			if left, right, overlap := tree.Conflict(entry.Features); overlap {
				return nil, fmt.Errorf("exclusive layer %s: %s intersects %s", name, left, right)
			}
		}
		m.Layers = append(m.Layers, Layer{Definition: def, Revision: entry.Revision, Features: entry.Features})
	}
	return m, nil
}
