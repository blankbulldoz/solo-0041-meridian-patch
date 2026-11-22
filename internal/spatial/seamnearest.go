package spatial

import (
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// SeamHit is the positive-length shared edge closest to a click point. First
// and Second keep the feature pair that produced the edge, so two pairs
// whose seams share identical coordinates still report distinct provenance.
type SeamHit struct {
	First           string     `json:"first"`
	Second          string     `json:"second"`
	Seam            plane.Seam `json:"seam"`
	DistanceSquared int64      `json:"distance_squared"`
}

// NearestSeam finds the windowed shared edge with the smallest closure
// distance to p and returns nil when the window holds no seam. Corner
// touches never qualify because SharedBoundary requires positive length.
// Distance ties break by the (first, second) id pair, and coincident seams
// from independent pairs are never merged, so the answer is deterministic
// and always names its source pair.
//
// The walk stays bounded on both extremes. Rows surface in ascending closure
// distance and a seam lies on the closure of both its rectangles, so once
// the best distance is known, any row whose closure is farther than that
// ends the walk: dense tilings find a seam almost immediately and prune the
// rest. Rank never enters subtrees outside the window closure, so empty
// regions touch only the index path above the window. Adversarial layouts
// still fall back to the shared candidate budget and report ErrSeamBudget.
func NearestSeam(tree packed.Tree, window plane.Box, p plane.Point) (*SeamHit, packed.Visit, error) {
	var hit *SeamHit
	var best int64
	var visit packed.Visit
	var exhausted bool
	ranked := 0
	rankVisit := tree.Rank(p, window, func(row catalog.Feature, lower int64) bool {
		if hit != nil && lower > best {
			return false
		}
		if ranked+visit.Candidates >= maxJoinCandidates {
			exhausted = true
			return false
		}
		ranked++
		remaining := maxJoinCandidates - ranked - visit.Candidates
		pairVisit := tree.Search(row.Shape.Envelope(), func(other catalog.Feature) bool {
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
			distance := seam.DistanceSquared(p)
			if hit == nil || distance < best || (distance == best && closerPair(row.ID, other.ID, hit)) {
				best = distance
				hit = &SeamHit{First: row.ID, Second: other.ID, Seam: seam, DistanceSquared: distance}
			}
			return true
		})
		visit.Nodes += pairVisit.Nodes
		visit.Candidates += pairVisit.Candidates
		return !exhausted
	})
	visit.Nodes += rankVisit.Nodes
	visit.Candidates += ranked
	if exhausted {
		return nil, visit, ErrSeamBudget
	}
	return hit, visit, nil
}

func closerPair(first, second string, hit *SeamHit) bool {
	return first < hit.First || (first == hit.First && second < hit.Second)
}
