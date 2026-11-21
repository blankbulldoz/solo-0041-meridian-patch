package spatial

import (
	"cmp"
	"errors"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

var ErrSeamBudget = errors.New("seam query exceeds seam or candidate budget")

type SharedSeam struct {
	First  string     `json:"first"`
	Second string     `json:"second"`
	Seam   plane.Seam `json:"seam"`
}

type SeamResult struct {
	Seams []SharedSeam `json:"seams"`
	Visit packed.Visit `json:"visit"`
}

func Seams(tree packed.Tree, window plane.Box, limit int) (SeamResult, error) {
	result := SeamResult{Seams: make([]SharedSeam, 0)}
	// Search the closed envelope to keep features that lie entirely outside the
	// window but share an edge on one of its included sides.
	outer := make([]catalog.Feature, 0)
	result.Visit = tree.Search(window, func(feature catalog.Feature) bool {
		outer = append(outer, feature)
		return true
	})
	for _, row := range outer {
		remaining := maxJoinCandidates - result.Visit.Candidates
		var exhausted bool
		visit := tree.Search(row.Shape.Envelope(), func(other catalog.Feature) bool {
			remaining--
			if remaining < 0 {
				exhausted = true
				return false
			}
			if other.ID <= row.ID {
				return true
			}
			seam, shares := plane.SharedBoundary(*row.Shape.Extent, *other.Shape.Extent, window)
			if !shares {
				return true
			}
			if len(result.Seams) == limit {
				exhausted = true
				return false
			}
			result.Seams = append(result.Seams, SharedSeam{First: row.ID, Second: other.ID, Seam: seam})
			return true
		})
		if exhausted {
			return SeamResult{}, ErrSeamBudget
		}
		result.Visit.Nodes += visit.Nodes
		result.Visit.Candidates += visit.Candidates
	}
	slices.SortFunc(result.Seams, func(a, b SharedSeam) int {
		if order := cmp.Compare(a.First, b.First); order != 0 {
			return order
		}
		return cmp.Compare(a.Second, b.Second)
	})
	return result, nil
}
