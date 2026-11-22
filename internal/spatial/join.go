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

func Join(left, right packed.Tree, bounds plane.Box, pairLimit int) (JoinResult, error) {
	var result JoinResult
	leftRows := Window(left, bounds, "", OrderID, nil)
	result.Visit = leftRows.Visit
	result.Pairs = make([]Pair, 0)
	for _, row := range leftRows.Features {
		clipped, _ := restrict(row.Shape, bounds)
		var exhausted bool
		remaining := maxJoinCandidates - result.Visit.Candidates
		visit := right.Search(clipped.Envelope(), func(other catalog.Feature) bool {
			remaining--
			if remaining < 0 {
				exhausted = true
				return false
			}
			rightShape, visible := restrict(other.Shape, bounds)
			if !visible || !clipped.Meets(rightShape) {
				return true
			}
			if len(result.Pairs) == pairLimit {
				exhausted = true
				return false
			}
			pair := Pair{Left: row.ID, Right: other.ID}
			if clipped.Extent != nil && rightShape.Extent != nil {
				intersection, _ := clipped.Extent.Clip(*rightShape.Extent)
				pair.Intersection = &intersection
			}
			result.Pairs = append(result.Pairs, pair)
			return true
		})
		if exhausted {
			return JoinResult{}, ErrJoinBudget
		}
		result.Visit.Nodes += visit.Nodes
		result.Visit.Candidates += visit.Candidates
	}
	slices.SortFunc(result.Pairs, func(a, b Pair) int {
		if order := cmp.Compare(a.Left, b.Left); order != 0 {
			return order
		}
		return cmp.Compare(a.Right, b.Right)
	})
	return result, nil
}

func restrict(shape plane.Shape, bounds plane.Box) (plane.Shape, bool) {
	if shape.Mark != nil {
		return shape, bounds.Owns(*shape.Mark)
	}
	clipped, visible := shape.Extent.Clip(bounds)
	return plane.Shape{Extent: &clipped}, visible
}
