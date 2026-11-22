package spatial

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// Vacancy reports the lowest, then leftmost, integer anchor where a rectangle
// of the requested size fits inside the window. Edge contact is allowed; only
// positive-area overlap with a feature blocks an anchor.
type Vacancy struct {
	Found  bool         `json:"found"`
	Anchor *plane.Point `json:"anchor,omitempty"`
	Visit  packed.Visit `json:"visit"`
}

// forbidden is the set of anchors blocked by one feature, clipped to the
// anchor domain. All four bounds are inclusive integer coordinates.
type forbidden struct {
	x0, x1 int64
	y0, y1 int64
}

// FindVacancy sweeps anchor rows from the bottom of the window. Row coverage
// only changes where a forbidden y range starts or ends, so rows between
// events are skipped, which keeps huge windows cheap.
func FindVacancy(tree packed.Tree, window plane.Box, width, height int64) Vacancy {
	result := Vacancy{}
	lastX := window.MaxX - width
	lastY := window.MaxY - height
	if lastX < window.MinX || lastY < window.MinY {
		return result
	}
	blocked := make([]forbidden, 0)
	result.Visit = tree.Search(window, func(row catalog.Feature) bool {
		b, present := window.Clip(*row.Shape.Extent)
		if present {
			blocked = append(blocked, forbidden{
				x0: max(b.MinX-width+1, window.MinX), x1: min(b.MaxX-1, lastX),
				y0: max(b.MinY-height+1, window.MinY), y1: min(b.MaxY-1, lastY),
			})
		}
		return true
	})
	// Coverage is constant between consecutive forbidden x edges, so the
	// elementary intervals between sorted edges stand in for every integer x.
	xs := make([]int64, 0, 2*len(blocked)+2)
	xs = append(xs, window.MinX, lastX+1)
	for _, f := range blocked {
		xs = append(xs, f.x0, f.x1+1)
	}
	slices.Sort(xs)
	xs = slices.Compact(xs)
	type event struct {
		y      int64
		lo, hi int
		delta  int
	}
	events := make([]event, 0, 2*len(blocked))
	for _, f := range blocked {
		lo, _ := slices.BinarySearch(xs, f.x0)
		hi, _ := slices.BinarySearch(xs, f.x1+1)
		events = append(events, event{y: f.y0, lo: lo, hi: hi, delta: 1})
		if f.y1 < lastY {
			events = append(events, event{y: f.y1 + 1, lo: lo, hi: hi, delta: -1})
		}
	}
	slices.SortFunc(events, func(a, b event) int { return cmp.Compare(a.y, b.y) })
	gaps := newGapTree(len(xs) - 1)
	row := window.MinY
	for i := 0; row <= lastY; {
		for i < len(events) && events[i].y == row {
			gaps.update(1, 0, len(xs)-1, events[i].lo, events[i].hi, events[i].delta)
			i++
		}
		if at, ok := gaps.first(1, 0, len(xs)-1); ok {
			result.Found = true
			result.Anchor = &plane.Point{X: xs[at], Y: row}
			return result
		}
		if i == len(events) {
			break
		}
		row = events[i].y
	}
	return result
}

// gapTree counts how many forbidden ranges cover each elementary interval and
// locates the leftmost uncovered interval. A pending add applies to the whole
// node span and is pushed down only when the descent continues.
type gapTree struct {
	nodes []gapNode
}

type gapNode struct {
	add int
	min int
}

func newGapTree(segments int) gapTree {
	return gapTree{nodes: make([]gapNode, 4*segments+1)}
}

func (t *gapTree) update(index, left, right, lo, hi, delta int) {
	n := &t.nodes[index]
	if lo <= left && right <= hi {
		n.add += delta
		n.min += delta
		return
	}
	t.push(index)
	mid := (left + right) / 2
	if lo < mid {
		t.update(2*index, left, mid, lo, hi, delta)
	}
	if hi > mid {
		t.update(2*index+1, mid, right, lo, hi, delta)
	}
	n.min = min(t.nodes[2*index].min, t.nodes[2*index+1].min)
}

func (t *gapTree) push(index int) {
	delta := t.nodes[index].add
	if delta == 0 {
		return
	}
	for _, child := range [...]int{2 * index, 2*index + 1} {
		t.nodes[child].add += delta
		t.nodes[child].min += delta
	}
	t.nodes[index].add = 0
}

// first returns the index of the leftmost interval with zero coverage.
func (t *gapTree) first(index, left, right int) (int, bool) {
	if t.nodes[index].min > 0 {
		return 0, false
	}
	if right-left == 1 {
		return left, true
	}
	t.push(index)
	mid := (left + right) / 2
	if t.nodes[2*index].min == 0 {
		return t.first(2*index, left, mid)
	}
	return t.first(2*index+1, mid, right)
}
