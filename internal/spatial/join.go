package spatial

import (
	"cmp"
	"errors"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const (
	MaxPairs          = 2000
	maxJoinCandidates = 200_000
)

var ErrJoinBudget = errors.New("spatial join exceeds pair or candidate budget")

type Pair struct {
	Left         string     `json:"left"`
	Right        string     `json:"right"`
	Intersection *plane.Box `json:"intersection,omitempty"`
}

type JoinResult struct {
	Pairs []Pair       `json:"pairs"`
	Visit packed.Visit `json:"visit"`
}

// Join returns every intersecting pair up to pairLimit and rejects the
// request when more pairs or more candidates than the budget would be needed.
func Join(left, right packed.Tree, bounds plane.Box, pairLimit int) (JoinResult, error) {
	result, more, err := JoinPage(left, right, bounds, pairLimit, "", "")
	if err != nil {
		return JoinResult{}, err
	}
	if more {
		return JoinResult{}, ErrJoinBudget
	}
	return result, nil
}

// JoinPage returns at most pairLimit intersecting pairs in (left, right) ID
// order, resuming strictly after the pair (afterLeft, afterRight); an empty
// afterLeft starts from the beginning. It reports whether further pairs
// remain beyond the page. The candidate budget applies to each call: a page
// that must inspect more candidates than the budget is rejected, so a dense
// window can require a smaller page even though earlier pages succeeded.
func JoinPage(left, right packed.Tree, bounds plane.Box, pairLimit int, afterLeft, afterRight string) (JoinResult, bool, error) {
	var result JoinResult
	leftRows := Window(left, bounds, "", "")
	result.Visit = leftRows.Visit
	result.Pairs = make([]Pair, 0)
	more := false
rows:
	for _, row := range leftRows.Features {
		if row.ID < afterLeft {
			continue
		}
		clipped, _ := restrict(row.Shape, bounds)
		remaining := maxJoinCandidates - result.Visit.Candidates
		var exhausted bool
		matched := make([]catalog.Feature, 0)
		visit := right.Search(clipped.Envelope(), func(other catalog.Feature) bool {
			remaining--
			if remaining < 0 {
				exhausted = true
				return false
			}
			if row.ID == afterLeft && other.ID <= afterRight {
				return true
			}
			rightShape, visible := restrict(other.Shape, bounds)
			if !visible || !clipped.Meets(rightShape) {
				return true
			}
			matched = append(matched, other)
			return true
		})
		if exhausted {
			return JoinResult{}, false, ErrJoinBudget
		}
		result.Visit.Nodes += visit.Nodes
		result.Visit.Candidates += visit.Candidates
		slices.SortFunc(matched, func(a, b catalog.Feature) int { return cmp.Compare(a.ID, b.ID) })
		for _, other := range matched {
			pair := Pair{Left: row.ID, Right: other.ID}
			rightShape, _ := restrict(other.Shape, bounds)
			if clipped.Extent != nil && rightShape.Extent != nil {
				intersection, _ := clipped.Extent.Clip(*rightShape.Extent)
				pair.Intersection = &intersection
			}
			result.Pairs = append(result.Pairs, pair)
			if len(result.Pairs) > pairLimit {
				more = true
				result.Pairs = result.Pairs[:pairLimit]
				break rows
			}
		}
	}
	return result, more, nil
}

func restrict(shape plane.Shape, bounds plane.Box) (plane.Shape, bool) {
	if shape.Mark != nil {
		return shape, bounds.Owns(*shape.Mark)
	}
	clipped, visible := shape.Extent.Clip(bounds)
	return plane.Shape{Extent: &clipped}, visible
}
