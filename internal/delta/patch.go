// Package delta builds and applies incremental change packages (MPC v1) between
// two portable MPB v1 images. A package describes the structural difference from
// a named baseline image to a target image, so it stays small relative to a full
// image when only a few features change.
package delta

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// MaxPatch bounds an encoded change package the same way MaxImage bounds an image.
const MaxPatch = 16 << 20

const headerSize = 8 + 4 + sha256.Size

// signature mirrors the MPB preamble but names the Meridian Patch Change format.
var signature = [8]byte{'M', 'R', 'D', 'N', 'M', 'P', 'C', 1}

// DigestLength is the hex-encoded length of a content digest.
const DigestLength = sha256.Size * 2

// Op is a single feature-level change inside a LayerPatch.
type Op struct {
	ID    string           `json:"id"`
	Mode  string           `json:"mode"` // insert | replace | remove
	Value *catalog.Feature `json:"value,omitempty"`
}

// LayerPatch describes one layer's changes. A present Definition replaces the
// baseline definition (layout change or a brand-new layer). Drop removes the
// layer. Ops transform the baseline feature set into the target feature set.
type LayerPatch struct {
	Name       string              `json:"name"`
	Definition *catalog.Definition `json:"definition,omitempty"`
	Drop       bool                `json:"drop,omitempty"`
	Ops        []Op                `json:"ops"`
}

// Patch is the canonical JSON payload of an MPC v1 container.
type Patch struct {
	Format           uint64       `json:"format"`
	BaselineRevision uint64       `json:"baseline_revision"`
	TargetRevision   uint64       `json:"target_revision"`
	BaselineDigest   string       `json:"baseline_digest"`
	TargetDigest     string       `json:"target_digest"`
	Layers           []LayerPatch `json:"layers"`
}

// Error distinguishes a malformed package from baseline and outcome mismatches.
// Callers map Code onto their own transport-level status codes.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func malformed(format string, args ...any) *Error {
	return &Error{Code: "malformed", Message: fmt.Sprintf(format, args...)}
}

// ContentDigest hashes the catalog content of an image while ignoring revision
// numbers. Two images with identical layers, definitions and features therefore
// share a digest even when their local revision histories differ. The digest is
// stable across processes because MPB images are canonical JSON.
func ContentDigest(image catalog.CatalogImage) (string, error) {
	normalized := normalize(image)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// normalize returns a deep copy of the image with every revision field reset so
// that only structural content contributes to the digest.
func normalize(image catalog.CatalogImage) catalog.CatalogImage {
	out := catalog.CatalogImage{Revision: 0, Layers: make([]catalog.LayerImage, 0, len(image.Layers))}
	for _, layer := range image.Layers {
		copied := catalog.LayerImage{Definition: layer.Definition, Revision: 0, Features: append([]catalog.Feature(nil), layer.Features...)}
		out.Layers = append(out.Layers, copied)
	}
	return out
}

// Diff derives the change package that turns base into target. Both images must
// already decode and validate as MPB v1 images. An empty package (no layer
// entries) means the two images carry identical catalog content.
func Diff(base, target catalog.CatalogImage) (Patch, error) {
	baseDigest, err := ContentDigest(base)
	if err != nil {
		return Patch{}, err
	}
	targetDigest, err := ContentDigest(target)
	if err != nil {
		return Patch{}, err
	}
	patch := Patch{
		Format:           1,
		BaselineRevision: base.Revision,
		TargetRevision:   target.Revision,
		BaselineDigest:   baseDigest,
		TargetDigest:     targetDigest,
		Layers:           []LayerPatch{},
	}
	if baseDigest == targetDigest {
		return patch, nil
	}

	baseLayers := make(map[string]catalog.LayerImage, len(base.Layers))
	for _, layer := range base.Layers {
		baseLayers[layer.Definition.Name] = layer
	}
	targetLayers := make(map[string]catalog.LayerImage, len(target.Layers))
	for _, layer := range target.Layers {
		targetLayers[layer.Definition.Name] = layer
	}

	names := make(map[string]struct{}, len(baseLayers)+len(targetLayers))
	for name := range baseLayers {
		names[name] = struct{}{}
	}
	for name := range targetLayers {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	slices.Sort(ordered)

	for _, name := range ordered {
		baseLayer, inBase := baseLayers[name]
		targetLayer, inTarget := targetLayers[name]
		switch {
		case inTarget && !inBase:
			ops := make([]Op, 0, len(targetLayer.Features))
			for _, feature := range targetLayer.Features {
				feature := feature
				ops = append(ops, Op{ID: feature.ID, Mode: "insert", Value: &feature})
			}
			definition := targetLayer.Definition
			patch.Layers = append(patch.Layers, LayerPatch{Name: name, Definition: &definition, Ops: ops})
		case inBase && !inTarget:
			patch.Layers = append(patch.Layers, LayerPatch{Name: name, Drop: true, Ops: []Op{}})
		default:
			entry, err := diffLayer(name, baseLayer, targetLayer)
			if err != nil {
				return Patch{}, err
			}
			if entry != nil {
				patch.Layers = append(patch.Layers, *entry)
			}
		}
	}
	return patch, nil
}

func diffLayer(name string, base, target catalog.LayerImage) (*LayerPatch, error) {
	if base.Definition.Kind != target.Definition.Kind {
		return nil, fmt.Errorf("layer %q changes geometry kind, which an incremental package cannot carry", name)
	}
	entry := &LayerPatch{Name: name, Ops: []Op{}}
	if base.Definition != target.Definition {
		definition := target.Definition
		entry.Definition = &definition
	}

	baseRows := make(map[string]catalog.Feature, len(base.Features))
	for _, feature := range base.Features {
		baseRows[feature.ID] = feature
	}
	targetRows := make(map[string]catalog.Feature, len(target.Features))
	for _, feature := range target.Features {
		targetRows[feature.ID] = feature
	}
	ids := make(map[string]struct{}, len(baseRows)+len(targetRows))
	for id := range baseRows {
		ids[id] = struct{}{}
	}
	for id := range targetRows {
		ids[id] = struct{}{}
	}
	orderedIDs := make([]string, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	slices.Sort(orderedIDs)

	for _, id := range orderedIDs {
		oldFeature, inBase := baseRows[id]
		newFeature, inTarget := targetRows[id]
		switch {
		case inTarget && !inBase:
			feature := newFeature
			entry.Ops = append(entry.Ops, Op{ID: id, Mode: "insert", Value: &feature})
		case inBase && !inTarget:
			entry.Ops = append(entry.Ops, Op{ID: id, Mode: "remove"})
		case !featuresEqual(oldFeature, newFeature):
			feature := newFeature
			entry.Ops = append(entry.Ops, Op{ID: id, Mode: "replace", Value: &feature})
		}
	}
	if entry.Definition == nil && len(entry.Ops) == 0 {
		return nil, nil
	}
	return entry, nil
}

func featuresEqual(a, b catalog.Feature) bool {
	if a.ID != b.ID || a.Class != b.Class {
		return false
	}
	return shapeEqual(a.Shape, b.Shape)
}

// shapeEqual compares shape contents. Plain struct equality would compare the
// embedded point and box pointers by address instead of value.
func shapeEqual(a, b plane.Shape) bool {
	if (a.Mark == nil) != (b.Mark == nil) || (a.Extent == nil) != (b.Extent == nil) {
		return false
	}
	if a.Mark != nil && *a.Mark != *b.Mark {
		return false
	}
	if a.Extent != nil && *a.Extent != *b.Extent {
		return false
	}
	return true
}

// Apply transforms base according to patch and returns the reconstructed target
// image together with the layers whose content or definition changed. The
// baseline digest must match the package's recorded baseline, and the resulting
// catalog content must hash to the recorded target digest. Neither check mutates
// the input image. Revision numbers in the returned image are those carried by
// the package; the caller reassigns durable revisions as needed.
func Apply(base catalog.CatalogImage, patch Patch) (catalog.CatalogImage, []string, error) {
	if err := validate(patch); err != nil {
		return catalog.CatalogImage{}, nil, err
	}
	currentDigest, err := ContentDigest(base)
	if err != nil {
		return catalog.CatalogImage{}, nil, err
	}
	if currentDigest != patch.BaselineDigest {
		return catalog.CatalogImage{}, nil, &Error{
			Code: "baseline_mismatch",
			Message: fmt.Sprintf("patch baseline %s does not match current content %s",
				patch.BaselineDigest, currentDigest),
		}
	}

	baseLayers := make(map[string]catalog.LayerImage, len(base.Layers))
	for _, layer := range base.Layers {
		baseLayers[layer.Definition.Name] = layer
	}
	changed := make([]string, 0, len(patch.Layers))
	resultLayers := make(map[string]catalog.LayerImage)
	for name, layer := range baseLayers {
		resultLayers[name] = layer
	}

	for _, layerPatch := range patch.Layers {
		name := layerPatch.Name
		baseLayer, exists := baseLayers[name]
		changed = append(changed, name)
		if layerPatch.Drop {
			if !exists {
				return catalog.CatalogImage{}, nil, malformed("drop targets missing layer %q", name)
			}
			if layerPatch.Definition != nil || len(layerPatch.Ops) != 0 {
				return catalog.CatalogImage{}, nil, malformed("dropped layer %q cannot carry a definition or ops", name)
			}
			delete(resultLayers, name)
			continue
		}
		if layerPatch.Definition == nil {
			if !exists {
				return catalog.CatalogImage{}, nil, malformed("new layer %q must include a definition", name)
			}
		} else {
			if err := layerPatch.Definition.Validate(); err != nil {
				return catalog.CatalogImage{}, nil, malformed("layer %q: %v", name, err)
			}
			if exists && baseLayer.Definition.Kind != layerPatch.Definition.Kind {
				return catalog.CatalogImage{}, nil, malformed("layer %q cannot change geometry kind", name)
			}
		}

		definition := baseLayer.Definition
		if layerPatch.Definition != nil {
			definition = *layerPatch.Definition
		}
		rows := make(map[string]catalog.Feature)
		for _, feature := range baseLayer.Features {
			rows[feature.ID] = feature
		}
		for index, op := range layerPatch.Ops {
			if err := applyOp(definition, rows, op); err != nil {
				return catalog.CatalogImage{}, nil, malformed("layer %q op %d: %v", name, index, err)
			}
		}

		target := catalog.LayerImage{
			Definition: definition,
			Features:   sortedFeatures(rows),
		}
		resultLayers[name] = target
	}

	// Revision numbers identify revisions on the originating database, not
	// catalog content, so they are not part of any digest. Reconstructing an
	// image from a package therefore starts every layer and the image itself at
	// revision zero; the importing database (or a later restore) reassigns real
	// revisions before the image is stored or validated against revision rules.
	result := catalog.CatalogImage{Revision: 0, Layers: make([]catalog.LayerImage, 0, len(resultLayers))}
	names := make([]string, 0, len(resultLayers))
	for name := range resultLayers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		layer := resultLayers[name]
		layer.Revision = 0
		result.Layers = append(result.Layers, layer)
	}
	slices.Sort(changed)

	outcomeDigest, err := ContentDigest(result)
	if err != nil {
		return catalog.CatalogImage{}, nil, err
	}
	if outcomeDigest != patch.TargetDigest {
		return catalog.CatalogImage{}, nil, &Error{
			Code:    "content_mismatch",
			Message: fmt.Sprintf("applied package produced %s, expected target %s", outcomeDigest, patch.TargetDigest),
		}
	}
	if err := validateContent(result); err != nil {
		return catalog.CatalogImage{}, nil, err
	}
	return result, changed, nil
}

func applyOp(definition catalog.Definition, rows map[string]catalog.Feature, op Op) error {
	if !catalog.ValidName(op.ID) {
		return fmt.Errorf("invalid feature id %q", op.ID)
	}
	_, present := rows[op.ID]
	switch op.Mode {
	case "insert":
		if present {
			return fmt.Errorf("feature %q already exists", op.ID)
		}
		if op.Value == nil || op.Value.ID != op.ID {
			return fmt.Errorf("insert requires a value with matching id")
		}
		if err := op.Value.Validate(definition); err != nil {
			return err
		}
		rows[op.ID] = op.Value.Copy()
	case "replace":
		if !present {
			return fmt.Errorf("feature %q does not exist", op.ID)
		}
		if op.Value == nil || op.Value.ID != op.ID {
			return fmt.Errorf("replace requires a value with matching id")
		}
		if err := op.Value.Validate(definition); err != nil {
			return err
		}
		rows[op.ID] = op.Value.Copy()
	case "remove":
		if !present {
			return fmt.Errorf("feature %q does not exist", op.ID)
		}
		if op.Value != nil {
			return fmt.Errorf("remove does not accept a value")
		}
		delete(rows, op.ID)
	default:
		return fmt.Errorf("unknown mode %q", op.Mode)
	}
	return nil
}

func sortedFeatures(rows map[string]catalog.Feature) []catalog.Feature {
	features := make([]catalog.Feature, 0, len(rows))
	for _, feature := range rows {
		features = append(features, feature)
	}
	slices.SortFunc(features, func(a, b catalog.Feature) int { return cmp.Compare(a.ID, b.ID) })
	return features
}

func validate(patch Patch) error {
	if patch.Format != 1 {
		return malformed("unsupported patch format %d", patch.Format)
	}
	if !validDigest(patch.BaselineDigest) || !validDigest(patch.TargetDigest) {
		return malformed("baseline and target digests must be %d hex characters", DigestLength)
	}
	if patch.Layers == nil {
		return malformed("patch must carry a layers array")
	}
	seen := make(map[string]bool)
	previous := ""
	for _, layer := range patch.Layers {
		if !catalog.ValidName(layer.Name) {
			return malformed("invalid layer name %q", layer.Name)
		}
		if layer.Name <= previous {
			return malformed("layer entries must be unique and sorted by name")
		}
		previous = layer.Name
		if seen[layer.Name] {
			return malformed("duplicate layer entry %q", layer.Name)
		}
		seen[layer.Name] = true
		if layer.Ops == nil {
			return malformed("layer %q must carry an ops array", layer.Name)
		}
		for _, op := range layer.Ops {
			if op.Mode != "insert" && op.Mode != "replace" && op.Mode != "remove" {
				return malformed("layer %q op %q has unknown mode", layer.Name, op.ID)
			}
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != DigestLength {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

// Encode wraps a patch in the fixed MPC v1 binary container.
func Encode(patch Patch) ([]byte, error) {
	payload, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxPatch-headerSize {
		return nil, errors.New("patch exceeds maximum size")
	}
	sum := sha256.Sum256(payload)
	raw := make([]byte, headerSize+len(payload))
	copy(raw, signature[:])
	binary.BigEndian.PutUint32(raw[8:12], uint32(len(payload)))
	copy(raw[12:], sum[:])
	copy(raw[headerSize:], payload)
	return raw, nil
}

// Decode parses and canonicalizes an MPC v1 container.
func Decode(raw []byte) (Patch, error) {
	var patch Patch
	if len(raw) < headerSize || len(raw) > MaxPatch || !bytes.Equal(raw[:8], signature[:]) {
		return patch, malformed("invalid patch size or version")
	}
	payload := raw[headerSize:]
	if int(binary.BigEndian.Uint32(raw[8:12])) != len(payload) {
		return patch, malformed("patch payload length mismatch")
	}
	sum := sha256.Sum256(payload)
	if !bytes.Equal(raw[12:headerSize], sum[:]) {
		return patch, malformed("patch checksum mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return patch, malformed("decode patch: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return patch, malformed("patch contains trailing JSON")
	}
	canonical, err := json.Marshal(patch)
	if err != nil || !bytes.Equal(payload, canonical) || patch.Layers == nil {
		return patch, malformed("noncanonical patch payload")
	}
	return patch, validate(patch)
}
