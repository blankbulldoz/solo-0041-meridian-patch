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
	events := make([]boundary, 0)
	y := make([]int64, 0)
	visit := tree.Search(window, func(row catalog.Feature) bool {
		if class != "" && row.Class != class {
			return true
		}
		b, present := window.Clip(*row.Shape.Extent)
		if present {
			events = append(events, boundary{b.MinX, b.MinY, b.MaxY, 1}, boundary{b.MaxX, b.MinY, b.MaxY, -1})
			y = append(y, b.MinY, b.MaxY)
		}
		return true
	})
	result := AreaResult{Uncovered: window.Area(), Rectangles: len(events) / 2, Visit: visit}
	if len(events) == 0 {
		return result
	}
	slices.Sort(y)
	y = slices.Compact(y)
	slices.SortFunc(events, func(a, b boundary) int { return cmp.Compare(a.x, b.x) })
	strips := newLengthTree(y)
	previous := events[0].x
	for _, e := range events {
		width := e.x - previous
		result.Covered += width * strips.covered()
		result.Repeated += width * strips.repeated()
		lo, _ := slices.BinarySearch(y, e.y0)
		hi, _ := slices.BinarySearch(y, e.y1)
		strips.update(1, 0, len(y)-1, lo, hi, e.delta)
		previous = e.x
	}
	result.Uncovered -= result.Covered
	return result
}
