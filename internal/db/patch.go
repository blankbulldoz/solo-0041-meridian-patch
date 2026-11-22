package db

import (
	"errors"
	"fmt"
	"math"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/delta"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

// PatchReceipt tells an operator whether an uploaded package changed anything.
// "applied" advances the revision, "already_applied" reports that the target
// content is already present and leaves the revision untouched. Both outcomes
// return the current content digest so repeat uploads can be correlated.
type PatchReceipt struct {
	Status           string   `json:"status"`
	Revision         uint64   `json:"revision"`
	BaselineDigest   string   `json:"baseline_digest"`
	TargetDigest     string   `json:"target_digest"`
	CurrentDigest    string   `json:"current_digest"`
	BaselineRevision uint64   `json:"baseline_revision"`
	TargetRevision   uint64   `json:"target_revision"`
	ChangedLayers    []string `json:"changed_layers,omitempty"`
}

func (d *Database) headImage() catalog.CatalogImage {
	return d.head.Load().image()
}

// Diff builds an incremental package from the named baseline image to the
// database's current image. The baseline is decoded and validated first, so a
// corrupt or incompatible baseline is rejected before a package is produced.
func (d *Database) Diff(baselineRaw []byte) (delta.Patch, error) {
	baseline, err := readImage(baselineRaw)
	if err != nil {
		return delta.Patch{}, invalid("diff baseline image: %v", err)
	}
	return DiffImages(baseline.image(), d.headImage())
}

// DiffImages derives a package between two fully validated images. It backs the
// offline CLI path where neither image belongs to an open database.
func DiffImages(base, target catalog.CatalogImage) (delta.Patch, error) {
	patch, err := delta.Diff(base, target)
	if err != nil {
		return delta.Patch{}, invalid("build patch: %v", err)
	}
	return patch, nil
}

// ApplyPatch applies an MPC v1 package under the write lock. The expected
// revision, when supplied, is the normal CAS guard and is only enforced when the
// package actually changes content; an idempotent replay never needs it.
func (d *Database) ApplyPatch(raw []byte, expected *uint64) (PatchReceipt, error) {
	patch, err := delta.Decode(raw)
	if err != nil {
		return PatchReceipt{}, invalid("decode patch: %v", err)
	}

	d.write.Lock()
	defer d.write.Unlock()
	if d.blocked.Load() {
		return PatchReceipt{}, &Error{Code: "unavailable", Message: "database is fenced; reopen the data directory"}
	}

	current := d.head.Load()
	currentDigest, err := delta.ContentDigest(current.image())
	if err != nil {
		return PatchReceipt{}, fmt.Errorf("digest current image: %w", err)
	}

	// Replay first: if the current content already equals the package target,
	// the package was applied before, regardless of the baseline revision.
	if currentDigest == patch.TargetDigest {
		return PatchReceipt{
			Status:           "already_applied",
			Revision:         current.revision,
			BaselineDigest:   patch.BaselineDigest,
			TargetDigest:     patch.TargetDigest,
			CurrentDigest:    currentDigest,
			BaselineRevision: patch.BaselineRevision,
			TargetRevision:   patch.TargetRevision,
			ChangedLayers:    []string{},
		}, nil
	}

	// A different content that is not the package baseline cannot accept it.
	// This rejects a target whose image merely shares the baseline revision.
	if currentDigest != patch.BaselineDigest {
		return PatchReceipt{}, &Error{
			Code: "baseline_mismatch",
			Message: fmt.Sprintf("patch expects baseline %s (revision %d) but current content is %s (revision %d)",
				patch.BaselineDigest, patch.BaselineRevision, currentDigest, current.revision),
		}
	}
	if expected != nil && *expected != current.revision {
		return PatchReceipt{}, conflict("expected database revision %d, current revision is %d", *expected, current.revision)
	}
	if current.revision == math.MaxUint64 {
		return PatchReceipt{}, exhausted("database revision space is exhausted")
	}

	resultImage, changed, err := delta.Apply(current.image(), patch)
	if err != nil {
		var problem *delta.Error
		if errors.As(err, &problem) {
			return PatchReceipt{}, &Error{Code: mapDeltaCode(problem.Code), Message: problem.Message}
		}
		return PatchReceipt{}, invalid("apply patch: %v", err)
	}

	revision := current.revision + 1
	resultImage.Revision = revision
	changedSet := make(map[string]bool, len(changed))
	for _, name := range changed {
		changedSet[name] = true
	}
	// The reconstructed image arrives with revision zero on every layer. Changed
	// layers take the new commit revision; untouched layers keep the revision
	// they had in the baseline, which stays valid and no greater than the head.
	baselineRevisions := make(map[string]uint64, len(current.layers))
	for name, layer := range current.layers {
		baselineRevisions[name] = layer.revision
	}
	for index := range resultImage.Layers {
		name := resultImage.Layers[index].Definition.Name
		if changedSet[name] {
			resultImage.Layers[index].Revision = revision
		} else {
			resultImage.Layers[index].Revision = baselineRevisions[name]
		}
	}

	next, err := fromImage(resultImage)
	if err != nil {
		return PatchReceipt{}, invalid("rebuild patched image: %v", err)
	}
	encoded, err := folio.Encode(next.image())
	if err != nil {
		return PatchReceipt{}, fmt.Errorf("encode patch commit: %w", err)
	}
	renamed, err := d.store.Replace(encoded)
	if err != nil {
		if renamed {
			d.blocked.Store(true)
			return PatchReceipt{}, errors.Join(
				&Error{Code: "commit_uncertain", Message: "replace occurred but durability was not confirmed; reopen before retrying"},
				err,
			)
		}
		return PatchReceipt{}, fmt.Errorf("persist patch commit: %w", err)
	}
	d.head.Store(next)

	return PatchReceipt{
		Status:           "applied",
		Revision:         revision,
		BaselineDigest:   patch.BaselineDigest,
		TargetDigest:     patch.TargetDigest,
		CurrentDigest:    patch.TargetDigest,
		BaselineRevision: patch.BaselineRevision,
		TargetRevision:   patch.TargetRevision,
		ChangedLayers:    changed,
	}, nil
}

func mapDeltaCode(code string) string {
	switch code {
	case "baseline_mismatch":
		return "baseline_mismatch"
	case "content_mismatch":
		return "content_mismatch"
	default:
		return "invalid"
	}
}
