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
	// Search the closed envelope to keep features that lie entirely outside the
	// window but share an edge on one of its included sides.
	var outer []catalog.Feature
	seed := tree.Search(window, func(feature catalog.Feature) bool {
		outer = append(outer, feature)
		return true
	})
	seams, visit, err := scanCandidates(
		seed,
		outer,
		tree,
		func(row catalog.Feature) plane.Box { return row.Shape.Envelope() },
		limit,
		ErrSeamBudget,
		func(row, candidate catalog.Feature) (SharedSeam, bool) {
			// Each unordered pair is handled once, from its lower-ID side.
			if candidate.ID <= row.ID {
				return SharedSeam{}, false
			}
			// SharedBoundary applies the half-open window clipping: left and
			// bottom sides count, right and top sides do not.
			seam, shares := plane.SharedBoundary(*row.Shape.Extent, *candidate.Shape.Extent, window)
			if !shares {
				return SharedSeam{}, false
			}
			return SharedSeam{First: row.ID, Second: candidate.ID, Seam: seam}, true
		},
	)
	if err != nil {
		return SeamResult{}, err
	}
	slices.SortFunc(seams, func(a, b SharedSeam) int {
		if order := cmp.Compare(a.First, b.First); order != 0 {
			return order
		}
		return cmp.Compare(a.Second, b.Second)
	})
	return SeamResult{Seams: seams, Visit: visit}, nil
}
