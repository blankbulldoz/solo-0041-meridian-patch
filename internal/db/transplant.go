package db

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

const (
	TransplantInsert  = "insert"
	TransplantReplace = "replace"

	transplantPlanVersion = 1
	// MaxTransplant bounds one region transplant the way MaxLayerRows bounds
	// any single layer, so a signed plan cannot grow without limit.
	MaxTransplant = catalog.MaxLayerRows
)

// TransplantRequest asks to move a region of one source image layer into a
// live target layer. Source holds a complete MPB v1 image.
type TransplantRequest struct {
	Source      []byte
	SourceLayer string
	Target      string
	Bounds      plane.Box
	Offset      plane.Offset
	Mode        string
	IfRevision  *uint64
}

func (r TransplantRequest) validate() error {
	if len(r.Source) == 0 {
		return invalid("transplant requires a source image")
	}
	if len(r.Source) > folio.MaxImage {
		return invalid("source image exceeds the %d byte image limit", folio.MaxImage)
	}
	if !catalog.ValidName(r.SourceLayer) || !catalog.ValidName(r.Target) {
		return invalid("transplant requires valid source_layer and target names")
	}
	if err := r.Bounds.Validate(); err != nil {
		return invalid("%v", err)
	}
	if err := r.Offset.Validate(); err != nil {
		return invalid("%v", err)
	}
	if r.Mode != TransplantInsert && r.Mode != TransplantReplace {
		return invalid("transplant mode must be insert or replace")
	}
	return nil
}

// TransplantConflict names one reason a checked batch cannot be applied. It
// always carries a stable kind so a caller can branch without parsing prose.
type TransplantConflict struct {
	Kind    string   `json:"kind"`
	IDs     []string `json:"ids,omitempty"`
	Message string   `json:"message"`
}

// TransplantPreview describes the translated batch and every reason it cannot
// yet be committed. Plan is present only when Conflicts is empty; it is the
// signed, unmodifiable input to CommitTransplant.
type TransplantPreview struct {
	Revision       uint64               `json:"revision"`
	TargetRevision uint64               `json:"target_revision"`
	TargetCount    int                  `json:"target_count"`
	SourceRevision uint64               `json:"source_revision"`
	SourceDigest   string               `json:"source_digest"`
	SourceLayer    string               `json:"source_layer"`
	Target         string               `json:"target"`
	Mode           string               `json:"mode"`
	Bounds         plane.Box            `json:"bounds"`
	Offset         plane.Offset         `json:"offset"`
	Features       []catalog.Feature    `json:"features"`
	Excluded       spatial.RegionSkips  `json:"excluded"`
	Conflicts      []TransplantConflict `json:"conflicts"`
	Visit          packed.Visit         `json:"visit"`
	Plan           string               `json:"plan,omitempty"`
}

// transplantPlan is the signed payload that carries a checked batch between
// preview and execute. TargetRevision pins the target layer, not the database,
// so an unrelated layer's commit does not invalidate the plan.
type transplantPlan struct {
	Version        int               `json:"version"`
	SourceDigest   string            `json:"source_digest"`
	SourceRevision uint64            `json:"source_revision"`
	SourceLayer    string            `json:"source_layer"`
	Target         string            `json:"target"`
	TargetRevision uint64            `json:"target_revision"`
	Mode           string            `json:"mode"`
	Bounds         plane.Box         `json:"bounds"`
	Offset         plane.Offset      `json:"offset"`
	Features       []catalog.Feature `json:"features"`
}

func (p transplantPlan) validate() error {
	if p.Version != transplantPlanVersion {
		return invalid("unsupported transplant plan version %d", p.Version)
	}
	if !catalog.ValidName(p.SourceLayer) || !catalog.ValidName(p.Target) {
		return invalid("transplant plan requires valid layer names")
	}
	if p.TargetRevision == 0 {
		return invalid("transplant plan has no target revision")
	}
	if p.Mode != TransplantInsert && p.Mode != TransplantReplace {
		return invalid("transplant plan has an unknown mode")
	}
	if err := p.Bounds.Validate(); err != nil {
		return invalid("transplant plan: %v", err)
	}
	if err := p.Offset.Validate(); err != nil {
		return invalid("transplant plan: %v", err)
	}
	if len(p.SourceDigest) != 2*sha256.Size {
		return invalid("transplant plan has a malformed source digest")
	}
	if len(p.Features) == 0 || len(p.Features) > MaxTransplant {
		return invalid("transplant plan must carry 1..%d features", MaxTransplant)
	}
	previous := ""
	for _, feature := range p.Features {
		if !catalog.ValidName(feature.ID) || !catalog.ValidName(feature.Class) {
			return invalid("transplant plan has an invalid feature id or class")
		}
		if feature.ID <= previous {
			return invalid("transplant plan features are not strictly ordered by id")
		}
		previous = feature.ID
		if (feature.Shape.Mark == nil) == (feature.Shape.Extent == nil) {
			return invalid("transplant plan feature %q must carry exactly one shape", feature.ID)
		}
		if feature.Shape.Mark != nil {
			if err := feature.Shape.Mark.Validate(); err != nil {
				return invalid("transplant plan feature %q: %v", feature.ID, err)
			}
			continue
		}
		if err := feature.Shape.Extent.Validate(); err != nil {
			return invalid("transplant plan feature %q: %v", feature.ID, err)
		}
	}
	return nil
}

// PreviewTransplant validates the source image, selects the region under the
// half-open window rule, clips each rectangle before translating it, and checks
// the resulting batch against the current target layer. It never writes.
func (d *Database) PreviewTransplant(request TransplantRequest) (TransplantPreview, error) {
	if err := request.validate(); err != nil {
		return TransplantPreview{}, err
	}
	source, err := readImage(request.Source)
	if err != nil {
		return TransplantPreview{}, invalid("transplant source image: %v", err)
	}
	sourceLayer, err := source.getLayer(request.SourceLayer)
	if err != nil {
		return TransplantPreview{}, err
	}
	s := d.head.Load()
	if request.IfRevision != nil && *request.IfRevision != s.revision {
		return TransplantPreview{}, conflict("expected database revision %d, current revision is %d", *request.IfRevision, s.revision)
	}
	target, err := s.getLayer(request.Target)
	if err != nil {
		return TransplantPreview{}, err
	}
	if sourceLayer.definition.Kind != target.definition.Kind {
		return TransplantPreview{}, invalid("source layer kind %q does not match target layer kind %q",
			sourceLayer.definition.Kind, target.definition.Kind)
	}
	selection := spatial.Select(sourceLayer.tree, request.Bounds, request.Offset)
	sum := sha256.Sum256(request.Source)
	preview := TransplantPreview{
		Revision:       s.revision,
		TargetRevision: target.revision,
		TargetCount:    len(target.rows),
		SourceRevision: source.revision,
		SourceDigest:   hex.EncodeToString(sum[:]),
		SourceLayer:    request.SourceLayer,
		Target:         request.Target,
		Mode:           request.Mode,
		Bounds:         request.Bounds,
		Offset:         request.Offset,
		Features:       selection.Features,
		Excluded:       selection.Excluded,
		Conflicts:      make([]TransplantConflict, 0),
		Visit:          selection.Visit,
	}
	if len(selection.Features) == 0 {
		preview.Conflicts = append(preview.Conflicts, TransplantConflict{Kind: "empty",
			Message: "source window selects no features under the half-open rule"})
		return preview, nil
	}
	preview.Conflicts = transplantConflicts(target.definition, target.rows, selection.Features, request.Mode,
		s.otherRowCount(target.definition.Name))
	if len(preview.Conflicts) != 0 {
		return preview, nil
	}
	token, err := d.signer.SealValue(transplantPlan{
		Version:        transplantPlanVersion,
		SourceDigest:   preview.SourceDigest,
		SourceRevision: source.revision,
		SourceLayer:    request.SourceLayer,
		Target:         request.Target,
		TargetRevision: target.revision,
		Mode:           request.Mode,
		Bounds:         request.Bounds,
		Offset:         request.Offset,
		Features:       selection.Features,
	})
	if err != nil {
		return TransplantPreview{}, exhausted("transplant plan is too large to sign; select a smaller region")
	}
	preview.Plan = token
	return preview, nil
}

// TransplantCommit is the result of applying one previewed plan.
type TransplantCommit struct {
	Revision     uint64            `json:"revision"`
	Target       string            `json:"target"`
	Mode         string            `json:"mode"`
	Transplanted int               `json:"transplanted"`
	SourceDigest string            `json:"source_digest"`
	Layers       []catalog.Summary `json:"layers"`
}

// CommitTransplant applies a signed preview plan as one atomic edit. The target
// layer revision must still match the preview and the final state must still
// pass every target rule; otherwise nothing is written.
func (d *Database) CommitTransplant(token string, expected *uint64) (TransplantCommit, error) {
	if token == "" {
		return TransplantCommit{}, invalid("transplant requires a preview plan")
	}
	var plan transplantPlan
	if err := d.signer.OpenValue(token, &plan); err != nil {
		return TransplantCommit{}, invalid("transplant plan is not valid for this process; preview again")
	}
	if err := plan.validate(); err != nil {
		return TransplantCommit{}, err
	}
	transplanted := 0
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		target, err := current.getLayer(plan.Target)
		if err != nil {
			return nil, err
		}
		if target.revision != plan.TargetRevision {
			return nil, conflict("target layer %q changed between preview and execute (revision %d, plan expects %d); nothing was applied, preview again",
				plan.Target, target.revision, plan.TargetRevision)
		}
		conflicts := transplantConflicts(target.definition, target.rows, plan.Features, plan.Mode,
			current.otherRowCount(plan.Target))
		if len(conflicts) != 0 {
			return nil, transplantRefusal(conflicts[0])
		}
		rows := mergedRows(target.rows, plan.Features)
		l, err := buildLayer(target.definition, revision, rows)
		if err != nil {
			return nil, err
		}
		next := &state{revision: revision, layers: maps.Clone(current.layers)}
		next.layers[plan.Target] = l
		var total int
		for _, entry := range next.layers {
			total += len(entry.rows)
		}
		if total > catalog.MaxRows {
			return nil, exhausted("database feature capacity reached")
		}
		transplanted = len(plan.Features)
		return next, nil
	})
	if err != nil {
		return TransplantCommit{}, err
	}
	return TransplantCommit{
		Revision:     s.revision,
		Target:       plan.Target,
		Mode:         plan.Mode,
		Transplanted: transplanted,
		SourceDigest: plan.SourceDigest,
		Layers:       summaries(s, []string{plan.Target}).Layers,
	}, nil
}

// transplantRefusal keeps the HTTP class of a refusal aligned with the kind the
// preview reported: a capacity shortfall stays a capacity error, everything else
// is a conflict of the target's final state.
func transplantRefusal(reason TransplantConflict) error {
	if reason.Kind == "capacity" {
		return exhausted("transplant target: %s", reason.Message)
	}
	return conflict("transplant target: %s", reason.Message)
}

// transplantConflicts checks the exact final state of the target layer after
// the batch, not the batch alone. That is what lets a replace batch swap two
// overlapping rectangles: only the end state has to obey exclusivity. Every
// distinct reason is reported so one preview shows the whole picture.
func transplantConflicts(def catalog.Definition, base map[string]catalog.Feature, incoming []catalog.Feature, mode string, otherRows int) []TransplantConflict {
	var kindIDs, geometryIDs, boundsIDs, duplicateIDs, missingIDs []string
	for _, feature := range incoming {
		if !matchesKind(feature.Shape, def.Kind) {
			kindIDs = append(kindIDs, feature.ID)
			continue
		}
		// Kind already matches, so a validation failure here means the
		// translation pushed a coordinate outside the global range or produced
		// an empty rectangle; report it as geometry, not as a wrong kind.
		if err := feature.Shape.Validate(def.Kind); err != nil {
			geometryIDs = append(geometryIDs, feature.ID)
			continue
		}
		if !feature.Shape.Within(def.Bounds) {
			boundsIDs = append(boundsIDs, feature.ID)
		}
		_, present := base[feature.ID]
		if mode == TransplantInsert && present {
			duplicateIDs = append(duplicateIDs, feature.ID)
		}
		if mode == TransplantReplace && !present {
			missingIDs = append(missingIDs, feature.ID)
		}
	}
	conflicts := make([]TransplantConflict, 0, 8)
	if len(kindIDs) != 0 {
		conflicts = append(conflicts, TransplantConflict{Kind: "kind", IDs: kindIDs,
			Message: "translated features do not match the target layer shape kind"})
	}
	if len(geometryIDs) != 0 {
		conflicts = append(conflicts, TransplantConflict{Kind: "geometry", IDs: geometryIDs,
			Message: "translated features leave the global coordinate range or have no area"})
	}
	if len(boundsIDs) != 0 {
		conflicts = append(conflicts, TransplantConflict{Kind: "bounds", IDs: boundsIDs,
			Message: "translated features fall outside the target layer bounds"})
	}
	if len(duplicateIDs) != 0 {
		conflicts = append(conflicts, TransplantConflict{Kind: "duplicate", IDs: duplicateIDs,
			Message: "insert mode found existing feature ids in the target layer"})
	}
	if len(missingIDs) != 0 {
		conflicts = append(conflicts, TransplantConflict{Kind: "missing", IDs: missingIDs,
			Message: "replace mode requires every feature id to exist in the target layer"})
	}
	final := mergedRows(base, incoming)
	if len(final) > catalog.MaxLayerRows {
		conflicts = append(conflicts, TransplantConflict{Kind: "capacity",
			Message: "target layer would exceed its feature capacity"})
	}
	if otherRows+len(final) > catalog.MaxRows {
		conflicts = append(conflicts, TransplantConflict{Kind: "capacity",
			Message: "database would exceed its feature capacity"})
	}
	if def.Exclusive {
		rows := sortedRows(final)
		if left, right, overlap := packed.Build(rows).Conflict(rows); overlap {
			conflicts = append(conflicts, TransplantConflict{Kind: "exclusive", IDs: []string{left, right},
				Message: "exclusive target layer would contain the overlapping pair " + left + " / " + right})
		}
	}
	return conflicts
}

func mergedRows(base map[string]catalog.Feature, incoming []catalog.Feature) map[string]catalog.Feature {
	rows := maps.Clone(base)
	for _, feature := range incoming {
		rows[feature.ID] = feature.Copy()
	}
	return rows
}

func matchesKind(shape plane.Shape, kind plane.Kind) bool {
	return (kind == plane.Mark) == (shape.Mark != nil)
}

func (s *state) otherRowCount(except string) int {
	total := 0
	for name, l := range s.layers {
		if name != except {
			total += len(l.rows)
		}
	}
	return total
}
