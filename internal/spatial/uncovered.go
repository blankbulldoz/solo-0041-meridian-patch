package spatial

import (
	"cmp"
	"errors"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// MaxFragments bounds the decomposition of one window into uncovered rectangles.
const MaxFragments = 2000

var ErrFragmentBudget = errors.New("uncovered decomposition exceeds the fragment limit")

type FragmentResult struct {
	Uncovered int64        `json:"uncovered"`
	Fragments []plane.Box  `json:"fragments"`
	Visit     packed.Visit `json:"visit"`
}

// opening is a fragment still growing along the sweep: it spans minX up to the
// current sweep position over the y range minY..maxY.
type opening struct {
	minX int64
	minY int64
	maxY int64
}

func lessOpening(a, b opening) bool {
	if a.minY != b.minY {
		return a.minY < b.minY
	}
	return a.maxY < b.maxY
}

// Uncovered decomposes the uncovered part of window into disjoint rectangles
// whose union is exactly the uncovered region. The sweep is deterministic:
// fragments grow along x while their y range stays an uncovered run, and the
// output is sorted, so repeated queries over the same view return identical
// fragments. The summed fragment area always equals the Coverage uncovered
// area, because both add up the same elementary slabs.
func Uncovered(tree packed.Tree, window plane.Box, class string, limit int) (FragmentResult, error) {
	events := make([]boundary, 0)
	y := []int64{window.MinY, window.MaxY}
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
	result := FragmentResult{Fragments: make([]plane.Box, 0), Visit: visit}
	if len(events) == 0 {
		// Nothing clips the window: the whole window is one candidate.
		if limit < 1 {
			return FragmentResult{}, ErrFragmentBudget
		}
		result.Uncovered = window.Area()
		result.Fragments = append(result.Fragments, window)
		return result, nil
	}
	slices.Sort(y)
	y = slices.Compact(y)
	slices.SortFunc(events, func(a, b boundary) int { return cmp.Compare(a.x, b.x) })
	counts := make([]int, len(y)-1)
	position := func(v int64) int {
		i, _ := slices.BinarySearch(y, v)
		return i
	}
	open := make([]opening, 0)
	created := 0
	closeFragment := func(o opening, maxX int64) {
		fragment := plane.Box{MinX: o.minX, MinY: o.minY, MaxX: maxX, MaxY: o.maxY}
		result.Fragments = append(result.Fragments, fragment)
		result.Uncovered += fragment.Area()
	}
	// emit absorbs one slab whose left edge is left: maximal runs of uncovered
	// elementary intervals extend matching open fragments, open fragments with
	// no matching run close at left, and new runs open at left. open stays
	// sorted by (minY, maxY) because runs are produced in y order.
	emit := func(left int64) error {
		runs := make([]opening, 0)
		for i := 0; i < len(counts); {
			if counts[i] > 0 {
				i++
				continue
			}
			j := i + 1
			for j < len(counts) && counts[j] == 0 {
				j++
			}
			runs = append(runs, opening{minX: left, minY: y[i], maxY: y[j]})
			i = j
		}
		next := make([]opening, 0, len(runs))
		o, r := 0, 0
		for o < len(open) || r < len(runs) {
			switch {
			case o < len(open) && r < len(runs) && open[o].minY == runs[r].minY && open[o].maxY == runs[r].maxY:
				next = append(next, open[o])
				o++
				r++
			case r < len(runs) && (o == len(open) || lessOpening(runs[r], open[o])):
				created++
				if created > limit {
					return ErrFragmentBudget
				}
				next = append(next, runs[r])
				r++
			default:
				closeFragment(open[o], left)
				o++
			}
		}
		open = next
		return nil
	}
	previous := window.MinX
	for e := 0; e < len(events); {
		x := events[e].x
		if x > previous {
			if err := emit(previous); err != nil {
				return FragmentResult{}, err
			}
		}
		for ; e < len(events) && events[e].x == x; e++ {
			lo, hi := position(events[e].y0), position(events[e].y1)
			for i := lo; i < hi; i++ {
				counts[i] += events[e].delta
			}
		}
		previous = x
	}
	if previous < window.MaxX {
		if err := emit(previous); err != nil {
			return FragmentResult{}, err
		}
	}
	for _, o := range open {
		closeFragment(o, window.MaxX)
	}
	slices.SortFunc(result.Fragments, func(a, b plane.Box) int {
		if order := cmp.Compare(a.MinX, b.MinX); order != 0 {
			return order
		}
		if order := cmp.Compare(a.MinY, b.MinY); order != 0 {
			return order
		}
		if order := cmp.Compare(a.MaxX, b.MaxX); order != 0 {
			return order
		}
		return cmp.Compare(a.MaxY, b.MaxY)
	})
	return result, nil
}
