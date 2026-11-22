package striping

import (
	"math/rand"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func box(x0, y0, x1, y1 int64) plane.Box {
	return plane.Box{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
}

func feature(id string, b plane.Box) Feature {
	return Feature{ID: id, Box: b}
}

// bruteForce enumerates every positive-width partition and returns the best
// lexicographic score using the exact same objective as the dynamic program.
func bruteForce(t *testing.T, width int64, bands int, total int64, prefix []int64) ([]int64, score) {
	t.Helper()
	best := score{maxAbs: 1<<63 - 1, sumSq: 1<<63 - 1}
	var bestCuts []int64
	var recurse func(start int64, left int, cuts []int64, cur score)
	recurse = func(start int64, left int, cuts []int64, cur score) {
		if left == 1 {
			s, ok := bandScore(prefix[width]-prefix[start], int64(bands), total)
			if !ok {
				return
			}
			cur = score{maxAbs: max(cur.maxAbs, s.maxAbs), sumSq: cur.sumSq + s.sumSq}
			if cur.sumSq < 0 {
				return
			}
			if bestCuts == nil || better(cur, best) {
				best = cur
				bestCuts = append([]int64(nil), cuts...)
			}
			return
		}
		for end := start + 1; end <= width-(int64(left)-1); end++ {
			s, ok := bandScore(prefix[end]-prefix[start], int64(bands), total)
			if !ok {
				continue
			}
			next := score{maxAbs: max(cur.maxAbs, s.maxAbs), sumSq: cur.sumSq + s.sumSq}
			recurse(end, left-1, append(cuts, end), next)
		}
	}
	recurse(0, bands, nil, score{})
	return bestCuts, best
}

func scoreOf(width int64, bands int, total int64, prefix, cuts []int64) score {
	boundaries := append(append([]int64{0}, cuts...), width)
	var out score
	for i := 0; i < bands; i++ {
		c := prefix[boundaries[i+1]] - prefix[boundaries[i]]
		s, ok := bandScore(c, int64(bands), total)
		if !ok {
			t := score{maxAbs: 1<<63 - 1, sumSq: 1<<63 - 1}
			return t
		}
		out.maxAbs = max(out.maxAbs, s.maxAbs)
		out.sumSq += s.sumSq
	}
	return out
}

// TestExactDPMatchesBruteForce exercises random prefix shapes on widths small
// enough for exhaustive enumeration, including plateau (empty-gap) columns.
func TestExactDPMatchesBruteForce(t *testing.T) {
	seed := int64(20261122)
	rng := rand.New(rand.NewSource(seed))
	for trial := 0; trial < 300; trial++ {
		width := int64(6 + rng.Intn(9))
		bands := 1 + rng.Intn(int(width))
		prefix := make([]int64, width+1)
		for i := 1; i <= int(width); i++ {
			step := int64(rng.Intn(6)) // zero steps model empty columns/gaps
			prefix[i] = prefix[i-1] + step
		}
		total := prefix[width]
		cuts := exactDynamicCuts(width, bands, total, prefix)
		got := scoreOf(width, bands, total, prefix, cuts)
		_, want := bruteForce(t, width, bands, total, prefix)
		if got != want {
			t.Fatalf("trial %d width %d bands %d prefix %v: dp score %+v != optimum %+v cuts %v",
				trial, width, bands, prefix, got, want, cuts)
		}
	}
}

// TestMinimaxOptimum uses an independent quadratic reachability check to
// verify the sliding-window solver reaches the exact minimum maximum
// deviation, including cases with wide zero-coverage gaps.
func TestMinimaxOptimum(t *testing.T) {
	rng := rand.New(rand.NewSource(99173))
	for trial := 0; trial < 60; trial++ {
		width := int64(40 + rng.Intn(120))
		bands := 2 + rng.Intn(4)
		if int64(bands) > width {
			continue
		}
		prefix := make([]int64, width+1)
		var maxCol int64
		for i := 1; i <= int(width); i++ {
			var step int64
			switch rng.Intn(4) {
			case 0:
				step = 0 // empty columns / wide gaps
			case 1:
				step = int64(rng.Intn(4))
			default:
				step = int64(3 + rng.Intn(5))
			}
			prefix[i] = prefix[i-1] + step
			maxCol = max(maxCol, step)
		}
		total := prefix[width]
		cuts, err := minimaxCuts(width, bands, total, prefix, maxCol)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		got := scoreOf(width, bands, total, prefix, cuts)
		opt := referenceMinimaxQuadratic(width, int64(bands), total, maxCol, prefix)
		if got.maxAbs != opt {
			t.Fatalf("trial %d width %d bands %d: maxAbs %d != optimum %d cuts %v",
				trial, width, bands, got.maxAbs, opt, cuts)
		}
		// Every reported band must respect the optimal bounded window.
		lo, hi := boundedWindow(total, opt, int64(bands))
		boundaries := append(append([]int64{0}, cuts...), width)
		for i := 0; i < bands; i++ {
			c := prefix[boundaries[i+1]] - prefix[boundaries[i]]
			if c < lo || c > hi || boundaries[i+1] <= boundaries[i] {
				t.Fatalf("trial %d infeasible band %d: coverage %d outside [%d,%d]", trial, i+1, c, lo, hi)
			}
		}
		assertPositiveTiling(t, width, bands, cuts)
	}
}

// referenceMinimaxQuadratic decides each trial limit with a plain O(n·width²)
// reachability recurrence, written independently of the production sliding
// pointers, then binary searches the smallest feasible limit.
func referenceMinimaxQuadratic(width, n, total, maxCol int64, prefix []int64) int64 {
	feasible := func(limit int64) bool {
		lo := ceilDiv(total-limit, n)
		if lo < 0 {
			lo = 0
		}
		hi := (total + limit) / n
		if hi < maxCol {
			return false
		}
		reachable := make([][]bool, n+1)
		for k := range reachable {
			reachable[k] = make([]bool, width+1)
		}
		reachable[0][0] = true
		for k := int64(1); k <= n; k++ {
			for x := k; x <= width; x++ {
				for j := k - 1; j < x; j++ {
					if !reachable[k-1][j] {
						continue
					}
					sum := prefix[x] - prefix[j]
					if sum >= lo && sum <= hi {
						reachable[k][x] = true
						break
					}
				}
			}
		}
		return reachable[n][width]
	}
	lo, hi := int64(0), total
	for lo < hi {
		mid := (lo + hi) / 2
		if feasible(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

func assertPositiveTiling(t *testing.T, width int64, bands int, cuts []int64) {
	t.Helper()
	if len(cuts) != bands-1 {
		t.Fatalf("got %d cuts, want %d", len(cuts), bands-1)
	}
	prev := int64(0)
	for _, c := range cuts {
		if c <= prev {
			t.Fatalf("non-increasing cut %d after %d", c, prev)
		}
		prev = c
	}
	if prev >= width {
		t.Fatalf("last cut %d reaches width %d", prev, width)
	}
}
