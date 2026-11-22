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
	window bool
}

// Coverage reports covered, repeated and uncovered area over the union of the
// query windows. Only points belonging to at least one window are measured.
// Feature overlap is counted once per region, not once per window: a region
// inside two windows but under one rectangle is covered once, and a region
// under two rectangles is repeated once no matter how many windows hold it.
func Coverage(tree packed.Tree, windows []plane.Box, class string) AreaResult {
	if len(windows) == 0 {
		return AreaResult{Visit: packed.Visit{}}
	}
	envelope := windows[0]
	for _, window := range windows[1:] {
		envelope = envelope.Union(window)
	}
	events := make([]boundary, 0)
	y := make([]int64, 0)
	for _, window := range windows {
		events = append(events, boundary{window.MinX, window.MinY, window.MaxY, 1, true},
			boundary{window.MaxX, window.MinY, window.MaxY, -1, true})
		y = append(y, window.MinY, window.MaxY)
	}
	var visit packed.Visit
	rectangles := 0
	visit = tree.Search(envelope, func(row catalog.Feature) bool {
		if class != "" && row.Class != class {
			return true
		}
		clipped, present := envelope.Clip(*row.Shape.Extent)
		if !present || !anyWindow(windows, *row.Shape.Extent) {
			return true
		}
		rectangles++
		events = append(events, boundary{clipped.MinX, clipped.MinY, clipped.MaxY, 1, false},
			boundary{clipped.MaxX, clipped.MinY, clipped.MaxY, -1, false})
		y = append(y, clipped.MinY, clipped.MaxY)
		return true
	})
	result := AreaResult{Rectangles: rectangles, Visit: visit}
	slices.Sort(y)
	y = slices.Compact(y)
	slices.SortFunc(events, func(a, b boundary) int {
		if order := cmp.Compare(a.x, b.x); order != 0 {
			return order
		}
		// Close departing intervals before opening arriving ones so the y
		// depths never dip below zero between events at the same x.
		return cmp.Compare(a.delta, b.delta)
	})
	columns := newCoverageTree(y)
	previous := events[0].x
	for _, e := range events {
		width := e.x - previous
		result.Covered += width * columns.coveredLength()
		result.Repeated += width * columns.repeatedLength()
		result.Uncovered += width * columns.unionLength()
		lo, _ := slices.BinarySearch(y, e.y0)
		hi, _ := slices.BinarySearch(y, e.y1)
		columns.update(1, 0, len(y)-1, lo, hi, e.delta, e.window)
		previous = e.x
	}
	result.Uncovered -= result.Covered
	return result
}

// anyWindow reports a positive-area intersection with at least one window, so
// rectangles merely touching a window edge do not count as a contributing
// rectangle.
func anyWindow(windows []plane.Box, extent plane.Box) bool {
	for _, window := range windows {
		if _, meets := window.Clip(extent); meets {
			return true
		}
	}
	return false
}
