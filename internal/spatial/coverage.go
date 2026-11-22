package spatial

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

type AreaResult struct {
	Covered    int64        `json:"covered"`
	Repeated   int64        `json:"repeated"`
	Uncovered  int64        `json:"uncovered"`
	Rectangles int          `json:"rectangles"`
	Visit      packed.Visit `json:"visit"`
}

type boundary struct {
	x      int64
	y0, y1 int64
	delta  int
}

func Coverage(tree packed.Tree, window plane.Box, class string) AreaResult {
	rectangles := make([]plane.Box, 0)
	visit := tree.Search(window, func(row catalog.Feature) bool {
		if class != "" && row.Class != class {
			return true
		}
		if b, present := window.Clip(*row.Shape.Extent); present {
			rectangles = append(rectangles, b)
		}
		return true
	})
	covered, repeated := sweepArea(rectangles)
	return AreaResult{
		Covered:    covered,
		Repeated:   repeated,
		Uncovered:  window.Area() - covered,
		Rectangles: len(rectangles),
		Visit:      visit,
	}
}

// UnionArea measures the area covered by at least one rectangle. Overlapping
// coverage is counted once, so the result never exceeds the sum of the parts.
func UnionArea(rectangles []plane.Box) int64 {
	covered, _ := sweepArea(rectangles)
	return covered
}

// sweepArea accumulates the singly and repeatedly covered area of rectangles
// with an x-ordered boundary sweep over elementary y intervals.
func sweepArea(rectangles []plane.Box) (covered, repeated int64) {
	if len(rectangles) == 0 {
		return 0, 0
	}
	events := make([]boundary, 0, 2*len(rectangles))
	y := make([]int64, 0, 2*len(rectangles))
	for _, b := range rectangles {
		events = append(events, boundary{b.MinX, b.MinY, b.MaxY, 1}, boundary{b.MaxX, b.MinY, b.MaxY, -1})
		y = append(y, b.MinY, b.MaxY)
	}
	slices.Sort(y)
	y = slices.Compact(y)
	slices.SortFunc(events, func(a, b boundary) int { return cmp.Compare(a.x, b.x) })
	strips := newLengthTree(y)
	previous := events[0].x
	for _, e := range events {
		width := e.x - previous
		covered += width * strips.covered()
		repeated += width * strips.repeated()
		lo, _ := slices.BinarySearch(y, e.y0)
		hi, _ := slices.BinarySearch(y, e.y1)
		strips.update(1, 0, len(y)-1, lo, hi, e.delta)
		previous = e.x
	}
	return covered, repeated
}
