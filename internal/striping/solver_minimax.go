package striping

import (
	"fmt"
	"slices"
)

// minimaxFeasibilityBudget bounds the O(bands·width·log total) reachability
// work (binary search over the deviation limit, each layer linear in width)
// used for ranges too wide for the exact lexicographic dynamic program.
const minimaxFeasibilityBudget = 300_000_000

// minimaxCuts handles ranges too wide for the quadratic lexicographic dynamic
// program. It finds the exact minimum of the maximum scaled deviation
// |N·covered − total|.
//
// For a trial limit d every band must carry coverage in
//
//	L = ceil((total−d)/N) ≤ c ≤ floor((total+d)/N) = U.
//
// Feasibility is a layered reachability DP: layer k holds the end columns
// reachable after exactly k positive-width bands. A column x is reachable when
// some reachable predecessor j (j < x) satisfies P[x]−U ≤ P[j] ≤ P[x]−L.
// Because the predecessor layer and the prefix P are both sorted, the valid
// predecessor window only moves right as x grows, so each layer costs O(width)
// with two sliding pointers. Binary search then yields the smallest feasible
// limit, and a final pass records quantile-close parents for reconstruction.
func minimaxCuts(width int64, bands int, total int64, prefix []int64, maxCol int64) ([]int64, error) {
	n := int64(bands)
	if n*width*64 > minimaxFeasibilityBudget {
		return nil, fmt.Errorf("striping range width %d with %d bands exceeds the solver work budget; coarsen the integer range", width, bands)
	}

	feasible := func(limit int64) bool {
		lo, hi := boundedWindow(total, limit, n)
		if hi < maxCol {
			return false
		}
		ok, _ := reachabilityLayers(width, n, lo, hi, prefix, false, total)
		return ok
	}

	// A single-column partition always exists; its largest deviation is
	// N·maxCol − total when the busiest column exceeds the mean. The feasible
	// limit can therefore exceed total, so the upper bound covers both sides.
	high := total
	if upper := n * maxCol; upper > high {
		high = upper
	}
	low := int64(0)
	for low < high {
		mid := low + (high-low)/2
		if feasible(mid) {
			high = mid
		} else {
			low = mid + 1
		}
	}

	lo, hi := boundedWindow(total, low, n)
	ok, parents := reachabilityLayers(width, n, lo, hi, prefix, true, total)
	if !ok {
		return nil, fmt.Errorf("striping %d bands cannot split the coverage into that many positive-width pieces; reduce the band count", bands)
	}

	cuts := make([]int64, 0, n-1)
	x := width
	for k := int(n); k >= 2; k-- {
		j := parents[k-1][x]
		if j < 0 {
			return nil, fmt.Errorf("striping reconstruction failed at band %d", k)
		}
		cuts = append(cuts, j)
		x = j
	}
	slices.Reverse(cuts)
	return cuts, nil
}

func boundedWindow(total, limit, n int64) (lo int64, hi int64) {
	lo = ceilDiv(total-limit, n)
	if lo < 0 {
		lo = 0
	}
	return lo, (total + limit) / n
}

// reachabilityLayers computes the reachable end columns for every band layer.
// When recordParents is set it also stores one feasible predecessor per
// reachable column, biased toward band coverage closest to the ideal total/n.
//
// parents[layer-1][column] is -1 when the column is not reachable in that
// layer. The predecessor lists and prefix coverage are both sorted, which is
// what makes the sliding window valid.
func reachabilityLayers(width, n, lo, hi int64, prefix []int64, recordParents bool, total int64) (bool, [][]int64) {
	prev := []int64{0}
	parents := make([][]int64, n)
	targetFloor := total / n
	targetCeil := targetFloor
	if total%n != 0 {
		targetCeil = targetFloor + 1
	}

	for k := int64(1); k <= n; k++ {
		parent := make([]int64, width+1)
		for i := range parent {
			parent[i] = -1
		}
		// Compute reachability across the full width; the positive-width
		// requirement (each band consumes at least one column) is already
		// enforced by starting x at k and by the predecessor structure.
		var l, r int
		for x := k; x <= width; x++ {
			needLow := prefix[x] - hi
			needHigh := prefix[x] - lo
			for l < len(prev) && prefix[prev[l]] < needLow {
				l++
			}
			for r < len(prev) && prefix[prev[r]] <= needHigh {
				r++
			}
			if l >= r {
				continue
			}
			if recordParents {
				parent[x] = closestPredecessor(prev[l:r], prefix, x, targetFloor, targetCeil)
			} else {
				parent[x] = prev[l]
			}
		}
		parents[k-1] = parent
		next := make([]int64, 0, len(prev)*2)
		for x := k; x <= width; x++ {
			if parent[x] >= 0 {
				next = append(next, x)
			}
		}
		prev = next
		if len(prev) == 0 {
			return false, parents
		}
	}
	return slices.Contains(prev, width), parents
}

// closestPredecessor picks the reachable predecessor whose resulting band sum
// is nearest the ideal per-band coverage total/n. Positions are sorted and the
// band sum P[x]-P[j] decreases as j grows, so the optimum sits next to the
// crossing of the floor target; ties resolve to the smaller predecessor.
func closestPredecessor(window []int64, prefix []int64, x, targetFloor, targetCeil int64) int64 {
	threshold := prefix[x] - targetFloor
	cross, _ := slices.BinarySearchFunc(window, threshold, func(j, want int64) int {
		if prefix[j] < want {
			return -1
		}
		return 1
	})
	distance := func(j int64) int64 {
		sum := prefix[x] - prefix[j]
		d := absInt64(sum - targetFloor)
		if d2 := absInt64(sum - targetCeil); d2 < d {
			d = d2
		}
		return d
	}
	best := window[0]
	if cross > 0 {
		best = window[cross-1]
	}
	if cross < len(window) && distance(window[cross]) < distance(best) {
		best = window[cross]
	}
	return best
}

func ceilDiv(a, b int64) int64 {
	q := a / b
	r := a % b
	if r != 0 && ((a < 0) != (b < 0)) {
		q--
	}
	if r != 0 && ((a < 0) == (b < 0)) {
		q++
	}
	return q
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
