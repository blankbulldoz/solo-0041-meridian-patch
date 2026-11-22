package db

import (
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// Change kinds reported by DiffImages.
const (
	DiffEntered   = "entered"
	DiffExited    = "exited"
	DiffReshaped  = "reshaped"
	DiffReclassed = "reclassed"
)

type DiffQuery struct {
	Bounds plane.Box `json:"bounds"`
	Before []byte    `json:"before"`
	After  []byte    `json:"after"`
}

type DiffFeature struct {
	Class string      `json:"class"`
	Shape plane.Shape `json:"shape"`
}

type DiffEntry struct {
	Layer  string       `json:"layer"`
	ID     string       `json:"id"`
	Change string       `json:"change"`
	Before *DiffFeature `json:"before,omitempty"`
	After  *DiffFeature `json:"after,omitempty"`
}

type DiffResult struct {
	BeforeRevision uint64      `json:"before_revision"`
	AfterRevision  uint64      `json:"after_revision"`
	Changes        []DiffEntry `json:"changes"`
}

// DiffImages compares two portable images inside one window and reports what
// changed, ordered by layer name and feature ID. Both images are decoded and
// validated with the same rules as a restore, but the comparison is a pure
// function of the request: it builds no index and never reads, restores, or
// modifies the open database. Shapes are compared as stored, never clipped to
// the window, so a feature that slides along the window boundary with an
// unchanged clipped silhouette is still reported as reshaped.
func DiffImages(q DiffQuery) (DiffResult, error) {
	if err := q.Bounds.Validate(); err != nil {
		return DiffResult{}, invalid("%v", err)
	}
	if len(q.Before) == 0 || len(q.After) == 0 {
		return DiffResult{}, invalid("diff requires before and after images")
	}
	before, err := readImage(q.Before)
	if err != nil {
		return DiffResult{}, invalid("before image: %v", err)
	}
	after, err := readImage(q.After)
	if err != nil {
		return DiffResult{}, invalid("after image: %v", err)
	}
	result := DiffResult{
		BeforeRevision: before.revision,
		AfterRevision:  after.revision,
		Changes:        []DiffEntry{},
	}
	for _, name := range unionKeys(before.layers, after.layers) {
		var beforeRows, afterRows map[string]catalog.Feature
		if l := before.layers[name]; l != nil {
			beforeRows = l.rows
		}
		if l := after.layers[name]; l != nil {
			afterRows = l.rows
		}
		for _, id := range unionKeys(beforeRows, afterRows) {
			beforeFeature, inBefore := beforeRows[id]
			inBefore = inBefore && beforeFeature.Shape.InWindow(q.Bounds)
			afterFeature, inAfter := afterRows[id]
			inAfter = inAfter && afterFeature.Shape.InWindow(q.Bounds)
			switch {
			case !inBefore && !inAfter:
			case !inBefore:
				result.Changes = append(result.Changes, DiffEntry{
					Layer: name, ID: id, Change: DiffEntered, After: diffFeature(afterFeature),
				})
			case !inAfter:
				result.Changes = append(result.Changes, DiffEntry{
					Layer: name, ID: id, Change: DiffExited, Before: diffFeature(beforeFeature),
				})
			default:
				if !beforeFeature.Shape.Equal(afterFeature.Shape) {
					result.Changes = append(result.Changes, DiffEntry{
						Layer: name, ID: id, Change: DiffReshaped,
						Before: diffFeature(beforeFeature), After: diffFeature(afterFeature),
					})
				}
				if beforeFeature.Class != afterFeature.Class {
					result.Changes = append(result.Changes, DiffEntry{
						Layer: name, ID: id, Change: DiffReclassed,
						Before: diffFeature(beforeFeature), After: diffFeature(afterFeature),
					})
				}
			}
		}
	}
	return result, nil
}

// diffFeature keeps the stored shape. The decoded states are private to the
// call, so the entries can safely alias their rows.
func diffFeature(f catalog.Feature) *DiffFeature {
	return &DiffFeature{Class: f.Class, Shape: f.Shape}
}

func unionKeys[V any](left, right map[string]V) []string {
	keys := make([]string, 0, len(left)+len(right))
	for key := range left {
		keys = append(keys, key)
	}
	for key := range right {
		if _, ok := left[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}
