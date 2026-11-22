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

// joinSide pairs a left-side feature with the shape it presents inside the
// query window. The join clips both sides against the window, so a feature
// whose real intersection lies entirely outside the window never matches.
type joinSide struct {
	row     catalog.Feature
	clipped plane.Shape
}

func Join(left, right packed.Tree, bounds plane.Box, pairLimit int) (JoinResult, error) {
	leftRows := Window(left, bounds, "", "")
	sides := make([]joinSide, 0, len(leftRows.Features))
	for _, row := range leftRows.Features {
		clipped, _ := restrict(row.Shape, bounds)
		sides = append(sides, joinSide{row: row, clipped: clipped})
	}
	pairs, visit, err := scanCandidates(
		leftRows.Visit,
		sides,
		right,
		func(side joinSide) plane.Box { return side.clipped.Envelope() },
		pairLimit,
		ErrJoinBudget,
		func(side joinSide, candidate catalog.Feature) (Pair, bool) {
			rightShape, visible := restrict(candidate.Shape, bounds)
			if !visible || !side.clipped.Meets(rightShape) {
				return Pair{}, false
			}
			pair := Pair{Left: side.row.ID, Right: candidate.ID}
			if side.clipped.Extent != nil && rightShape.Extent != nil {
				intersection, _ := side.clipped.Extent.Clip(*rightShape.Extent)
				pair.Intersection = &intersection
			}
			return pair, true
		},
	)
	if err != nil {
		return JoinResult{}, err
	}
	slices.SortFunc(pairs, func(a, b Pair) int {
		if order := cmp.Compare(a.Left, b.Left); order != 0 {
			return order
		}
		return cmp.Compare(a.Right, b.Right)
	})
	return JoinResult{Pairs: pairs, Visit: visit}, nil
}

func restrict(shape plane.Shape, bounds plane.Box) (plane.Shape, bool) {
	if shape.Mark != nil {
		return shape, bounds.Owns(*shape.Mark)
	}
	clipped, visible := shape.Extent.Clip(bounds)
	return plane.Shape{Extent: &clipped}, visible
}
