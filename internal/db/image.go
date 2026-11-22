package db

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

func readImage(raw []byte) (*state, error) {
	image, err := folio.Decode(raw)
	if err != nil {
		return nil, err
	}
	return fromImage(image)
}

func (d *Database) Export() ([]byte, uint64, error) {
	s := d.head.Load()
	raw, err := folio.Encode(s.image())
	return raw, s.revision, err
}

// FeatureDelta counts identities whose content would change. A feature whose
// ID exists on both sides but whose class or geometry differs is modified,
// even when it is the only feature in a large layer.
type FeatureDelta struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Modified int `json:"modified"`
	Changed  int `json:"changed"`
}

// LayerDelta describes one layer name in the union of target and image layers.
// Status is one of added, replaced, removed or unchanged.
type LayerDelta struct {
	Layer             string       `json:"layer"`
	Status            string       `json:"status"`
	DefinitionChanged bool         `json:"definition_changed"`
	Features          FeatureDelta `json:"features"`
}

// LayerDiff groups layer names by what restoring the image would do to them.
// Replaced names are layers present in both sides whose definition or stored
// features differ; restores replace the whole target layer from the image.
type LayerDiff struct {
	Added     []string `json:"added"`
	Replaced  []string `json:"replaced"`
	Removed   []string `json:"removed"`
	Unchanged []string `json:"unchanged"`
}

// RestoreReport is the read-only preflight result for an MPB image. It never
// implies that the image has been installed; applying it still requires a
// PUT whose If-Meridian-Revision matches TargetRevision.
type RestoreReport struct {
	Mode           string       `json:"mode"`
	Readable       bool         `json:"readable"`
	Problem        string       `json:"problem,omitempty"`
	TargetRevision uint64       `json:"target_revision"`
	NextRevision   uint64       `json:"next_revision"`
	ImageRevision  uint64       `json:"image_revision"`
	ImageBytes     int          `json:"image_bytes"`
	ImageSHA256    string       `json:"image_sha256"`
	Layers         LayerDiff    `json:"layers"`
	Features       FeatureDelta `json:"features"`
	Details        []LayerDelta `json:"details"`
}

// Preflight validates an image exactly as Restore would, then reports its
// effect against one immutable target snapshot. It holds no write lock, does
// not encode or replace the state file, and does not advance the revision.
// The optional expected revision is checked against the same snapshot so a
// caller can demand that the report describes the current target.
//
// An unreadable image is itself a preflight result: the report says so with
// Readable false and a Problem, instead of failing the request. Only request
// and target-state failures (revision mismatch, exhausted revisions) return an
// error, because then no meaningful report can describe the target.
func (d *Database) Preflight(raw []byte, expected *uint64) (RestoreReport, error) {
	current := d.head.Load()
	if expected != nil && *expected != current.revision {
		return RestoreReport{}, conflict("expected database revision %d, current revision is %d", *expected, current.revision)
	}
	if current.revision == math.MaxUint64 {
		return RestoreReport{}, exhausted("database revision space is exhausted")
	}
	digest := sha256.Sum256(raw)
	report := RestoreReport{
		Mode:           "preflight",
		TargetRevision: current.revision,
		NextRevision:   current.revision + 1,
		ImageBytes:     len(raw),
		ImageSHA256:    hex.EncodeToString(digest[:]),
		Layers: LayerDiff{
			Added:     []string{},
			Replaced:  []string{},
			Removed:   []string{},
			Unchanged: []string{},
		},
		Details: []LayerDelta{},
	}
	candidate, err := readImage(raw)
	if err != nil {
		report.Problem = err.Error()
		return report, nil
	}
	report.Readable = true
	report.ImageRevision = candidate.revision
	for _, name := range unionNames(current, candidate) {
		delta := diffLayer(name, current.layers[name], candidate.layers[name])
		report.Details = append(report.Details, delta)
		switch delta.Status {
		case "added":
			report.Layers.Added = append(report.Layers.Added, name)
		case "replaced":
			report.Layers.Replaced = append(report.Layers.Replaced, name)
		case "removed":
			report.Layers.Removed = append(report.Layers.Removed, name)
		default:
			report.Layers.Unchanged = append(report.Layers.Unchanged, name)
		}
		report.Features.Added += delta.Features.Added
		report.Features.Removed += delta.Features.Removed
		report.Features.Modified += delta.Features.Modified
	}
	report.Features.Changed = report.Features.Added + report.Features.Removed + report.Features.Modified
	return report, nil
}

func unionNames(current, candidate *state) []string {
	names := make([]string, 0, len(current.layers)+len(candidate.layers))
	for name := range current.layers {
		names = append(names, name)
	}
	for name := range candidate.layers {
		if current.layers[name] == nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func diffLayer(name string, current, candidate *layer) LayerDelta {
	delta := LayerDelta{Layer: name, Status: "unchanged"}
	switch {
	case current == nil:
		delta.Status = "added"
		delta.Features.Added = len(candidate.rows)
	case candidate == nil:
		delta.Status = "removed"
		delta.Features.Removed = len(current.rows)
	default:
		delta.DefinitionChanged = current.definition != candidate.definition
		for id, row := range candidate.rows {
			previous, present := current.rows[id]
			switch {
			case !present:
				delta.Features.Added++
			case !row.Equal(previous):
				delta.Features.Modified++
			}
		}
		for id := range current.rows {
			if _, present := candidate.rows[id]; !present {
				delta.Features.Removed++
			}
		}
		if delta.DefinitionChanged || delta.Features.Added+delta.Features.Removed+delta.Features.Modified > 0 {
			delta.Status = "replaced"
		}
	}
	delta.Features.Changed = delta.Features.Added + delta.Features.Removed + delta.Features.Modified
	return delta
}

// Restore validates the image, then installs it under a target-revision CAS.
// A preflight report from an older target revision therefore cannot confirm a
// restore after any intervening write.
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
