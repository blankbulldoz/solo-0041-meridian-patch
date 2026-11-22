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
	_, seams, visit, err := collectSeams(tree, window, limit)
	if err != nil {
		return SeamResult{}, err
	}
	return SeamResult{Seams: seams, Visit: visit}, nil
}

// collectSeams enumerates every pair of extent features sharing a
// positive-length boundary inside the window, clipped to the window. It also
// returns the features whose envelopes meet the closed window, which is the
// full population that may contribute a seam (including features on the
// included left/bottom sides that lie entirely outside the open window).
// seamLimit <= 0 means every seam is collected. The same candidate budget used
// by the other spatial queries applies; exhaustion rejects the whole query.
func collectSeams(tree packed.Tree, window plane.Box, seamLimit int) ([]catalog.Feature, []SharedSeam, packed.Visit, error) {
	seams := make([]SharedSeam, 0)
	// Search the closed envelope to keep features that lie entirely outside the
	// window but share an edge on one of its included sides.
	outer := make([]catalog.Feature, 0)
	visit := tree.Search(window, func(feature catalog.Feature) bool {
		outer = append(outer, feature)
		return true
	})
	for _, row := range outer {
		remaining := maxJoinCandidates - visit.Candidates
		var exhausted bool
		step := tree.Search(row.Shape.Envelope(), func(other catalog.Feature) bool {
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
			if seamLimit > 0 && len(seams) == seamLimit {
				exhausted = true
				return false
			}
			seams = append(seams, SharedSeam{First: row.ID, Second: other.ID, Seam: seam})
			return true
		})
		if exhausted {
			return nil, nil, packed.Visit{}, ErrSeamBudget
		}
		visit.Nodes += step.Nodes
		visit.Candidates += step.Candidates
	}
	slices.SortFunc(seams, func(a, b SharedSeam) int {
		if order := cmp.Compare(a.First, b.First); order != 0 {
			return order
		}
		return cmp.Compare(a.Second, b.Second)
	})
	return outer, seams, visit, nil
}
