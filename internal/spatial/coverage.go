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
	Depth      *DepthResult `json:"depth,omitempty"`
}

// DepthResult partitions the window into mutually exclusive exact coverage
// depths. Exact[c] is the area owned by precisely c clipped rectangles and
// Over is the area owned by more than len(Exact)-1 rectangles.
type DepthResult struct {
	Exact []int64 `json:"exact"`
	Over  int64   `json:"over"`
}

type boundary struct {
	x      int64
	y0, y1 int64
	delta  int
}

// Coverage measures the window area owned by clipped rectangles. A nil tiers
// keeps the legacy aggregates only; a non-nil tiers additionally fills Depth
// with exact areas for depths 0..tiers and the area beyond tiers.
func Coverage(tree packed.Tree, window plane.Box, class string, tiers *int) AreaResult {
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
	if tiers != nil {
		result.Depth = &DepthResult{Exact: make([]int64, *tiers+1)}
	}
	if len(events) > 0 {
		slices.Sort(y)
		y = slices.Compact(y)
		slices.SortFunc(events, func(a, b boundary) int { return cmp.Compare(a.x, b.x) })
		if tiers == nil {
			sweepLegacy(&result, events, y)
		} else {
			sweepDepth(&result, events, y, *tiers)
		}
	}
	result.Uncovered -= result.Covered
	if result.Depth != nil {
		result.Depth.Exact[0] = result.Uncovered
	}
	return result
}

// sweepLegacy accumulates the at-least-once and at-least-twice areas with the
// segment tree from lengths.go.
func sweepLegacy(result *AreaResult, events []boundary, y []int64) {
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
}

// sweepDepth accumulates the area at every exact coverage depth up to tiers
// and the area beyond it, deriving the legacy aggregates from the same
// totals. Depths above the rectangle count are impossible, so the counter
// never tracks more slots than rectangles.
func sweepDepth(result *AreaResult, events []boundary, y []int64, tiers int) {
	track := max(tiers, 2)
	if rectangles := len(events) / 2; track > rectangles {
		track = rectangles
	}
	counter := newDepthCounter(y, track)
	totals := make([]int64, track+2)
	previous := events[0].x
	for _, e := range events {
		if width := e.x - previous; width > 0 {
			for depth, length := range counter.lengths {
				totals[depth] += width * length
			}
		}
		lo, _ := slices.BinarySearch(y, e.y0)
		hi, _ := slices.BinarySearch(y, e.y1)
		counter.update(lo, hi, e.delta)
		previous = e.x
	}
	for depth := 1; depth < len(totals); depth++ {
		result.Covered += totals[depth]
	}
	for depth := 2; depth < len(totals); depth++ {
		result.Repeated += totals[depth]
	}
	for depth := 1; depth <= tiers && depth <= track; depth++ {
		result.Depth.Exact[depth] = totals[depth]
	}
	for depth := tiers + 1; depth < len(totals); depth++ {
		result.Depth.Over += totals[depth]
	}
}
