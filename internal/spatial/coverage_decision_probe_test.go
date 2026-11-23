package spatial

// Diagnostic probe for choosing between the production coverage sweep
// (internal/spatial/coverage.go + lengths.go) and a sparse disjoint-segment
// sweep. It is additive: production code does not reference it.
//
// Run the fast differential checks and the example table:
//
//	go test ./internal/spatial -run TestCoverageDecision -v
//
// The timing scans are opt-in so the default suite stays fast:
//
//	MERIDIAN_COVERAGE_SCAN=1 go test ./internal/spatial -run 'SwitchScan|CrossoverByBreakpoints' -v
//
// Run the side-by-side timings directly:
//
//	go test ./internal/spatial -run '^$' -bench 'Coverage|ProbeSparse' -benchtime 3x

import (
	"cmp"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

var probeSink AreaResult

type probeEvent struct {
	x, y0, y1 int64
	delta     int
}

func probeFeatures(rects []plane.Box) []catalog.Feature {
	rows := make([]catalog.Feature, len(rects))
	for i := range rects {
		b := rects[i]
		rows[i] = catalog.Feature{
			ID:    fmt.Sprintf("r%04d", i),
			Class: "fixture",
			Shape: plane.Shape{Extent: &b},
		}
	}
	return rows
}

func probeEvents(rects []plane.Box, window plane.Box) ([]probeEvent, []int64) {
	events := make([]probeEvent, 0, 2*len(rects))
	ys := make([]int64, 0, 2*len(rects))
	for _, r := range rects {
		clip, ok := r.Clip(window)
		if !ok {
			continue
		}
		events = append(events,
			probeEvent{clip.MinX, clip.MinY, clip.MaxY, 1},
			probeEvent{clip.MaxX, clip.MinY, clip.MaxY, -1})
		ys = append(ys, clip.MinY, clip.MaxY)
	}
	return events, ys
}

func probeSorted(values map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(values))
	for v := range values {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// probeReference is an independent exact answer: it decomposes the window into
// elementary cells at every clipped endpoint and counts containing rectangles.
// It shares no sweep logic with either candidate.
func probeReference(rects []plane.Box, window plane.Box) (covered, repeated int64) {
	clipped := make([]plane.Box, 0, len(rects))
	xs, ys := map[int64]struct{}{}, map[int64]struct{}{}
	for _, r := range rects {
		c, ok := r.Clip(window)
		if !ok {
			continue
		}
		clipped = append(clipped, c)
		xs[c.MinX] = struct{}{}
		xs[c.MaxX] = struct{}{}
		ys[c.MinY] = struct{}{}
		ys[c.MaxY] = struct{}{}
	}
	sx, sy := probeSorted(xs), probeSorted(ys)
	for i := 0; i+1 < len(sx); i++ {
		for j := 0; j+1 < len(sy); j++ {
			x0, x1 := sx[i], sx[i+1]
			y0, y1 := sy[j], sy[j+1]
			n := 0
			for _, c := range clipped {
				if c.MinX <= x0 && x1 <= c.MaxX && c.MinY <= y0 && y1 <= c.MaxY {
					n++
				}
			}
			if n == 0 {
				continue
			}
			area := (x1 - x0) * (y1 - y0)
			covered += area
			if n >= 2 {
				repeated += area
			}
		}
	}
	return covered, repeated
}

type probeSeg struct {
	lo, hi int64
	count  int
}

// probeSparse keeps sorted disjoint y segments that each carry an exact
// multiplicity. Exactly-once, at-least-once and at-least-twice are all
// available, but only because the exact count survives end events.
func probeSparse(rects []plane.Box, window plane.Box) (covered, repeated int64, smax, ssum int) {
	events, _ := probeEvents(rects, window)
	if len(events) == 0 {
		return 0, 0, 0, 0
	}
	slices.SortFunc(events, func(a, b probeEvent) int { return cmp.Compare(a.x, b.x) })
	segs := make([]probeSeg, 0, 8)
	var once, twice int64
	previous := events[0].x
	for _, e := range events {
		width := e.x - previous
		covered += width * once
		repeated += width * twice
		segs, once, twice = probeApply(segs, e.y0, e.y1, e.delta, once, twice)
		ssum += len(segs)
		smax = max(smax, len(segs))
		previous = e.x
	}
	return covered, repeated, smax, ssum
}

func probeApply(segs []probeSeg, y0, y1 int64, delta int, once, twice int64) ([]probeSeg, int64, int64) {
	type edge struct {
		at, delta int64
	}
	edges := []edge{{y0, int64(delta)}, {y1, -int64(delta)}}
	for _, s := range segs {
		a, b := max(s.lo, y0), min(s.hi, y1)
		if a < b {
			edges = append(edges, edge{a, int64(s.count)}, edge{b, -int64(s.count)})
		}
	}
	slices.SortFunc(edges, func(a, b edge) int {
		if order := cmp.Compare(a.at, b.at); order != 0 {
			return order
		}
		return cmp.Compare(a.delta, b.delta)
	})
	// Rebuild the touched span from its local edges; keep outer pieces.
	out := make([]probeSeg, 0, len(segs)+4)
	for _, s := range segs {
		if s.hi <= y0 || s.lo >= y1 {
			out = append(out, s)
			continue
		}
		a, b := max(s.lo, y0), min(s.hi, y1)
		once -= b - a
		if s.count >= 2 {
			twice -= b - a
		}
		if s.lo < y0 {
			out = append(out, probeSeg{s.lo, y0, s.count})
		}
		if s.hi > y1 {
			out = append(out, probeSeg{y1, s.hi, s.count})
		}
	}
	count := int64(0)
	for i := 0; i < len(edges); {
		at := edges[i].at
		for i < len(edges) && edges[i].at == at {
			count += edges[i].delta
			i++
		}
		if i < len(edges) && at < y1 && count > 0 {
			hi := min(edges[i].at, y1)
			if hi > at {
				out = append(out, probeSeg{at, hi, int(count)})
				once += hi - at
				if count >= 2 {
					twice += hi - at
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b probeSeg) int {
		if order := cmp.Compare(a.lo, b.lo); order != 0 {
			return order
		}
		return cmp.Compare(a.hi, b.hi)
	})
	merged := out[:0]
	for _, s := range out {
		if s.hi <= s.lo || s.count <= 0 {
			continue
		}
		if n := len(merged); n > 0 && merged[n-1].hi == s.lo && merged[n-1].count == s.count {
			merged[n-1].hi = s.hi
			continue
		}
		merged = append(merged, s)
	}
	return merged, once, twice
}

func probeRun(rects []plane.Box, window plane.Box) (AreaResult, int) {
	res := Coverage(packed.Build(probeFeatures(rects)), window, "")
	_, ys := probeEvents(rects, window)
	slices.Sort(ys)
	ys = slices.Compact(ys)
	return res, len(ys)
}

func probeRandom(rng *rand.Rand, n int, span int64) []plane.Box {
	out := make([]plane.Box, 0, n)
	for i := 0; i < n; i++ {
		x0, y0 := rng.Int63n(span), rng.Int63n(span)
		w, h := rng.Int63n(span-x0)+1, rng.Int63n(span-y0)+1
		out = append(out, plane.Box{MinX: x0, MinY: y0, MaxX: x0 + w, MaxY: y0 + h})
	}
	return out
}

func probeRandomLarge(rng *rand.Rand, n int, limit int64) []plane.Box {
	spans := []int64{1, 2, 7, 1_000_000, 2*limit - 1, 3}
	pick := func() int64 {
		v := rng.Int63n(2*limit) - limit
		return v
	}
	out := make([]plane.Box, 0, n)
	for i := 0; i < n; i++ {
		x0, y0 := pick(), pick()
		x1, y1 := min(limit, x0+spans[rng.Intn(len(spans))]), min(limit, y0+spans[rng.Intn(len(spans))])
		if x1 <= x0 {
			x1 = x0 + 1
		}
		if y1 <= y0 {
			y1 = y0 + 1
		}
		out = append(out, plane.Box{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1})
	}
	return out
}

func ProbeExampleEdgeDense(n int) []plane.Box {
	limit := int64(1_000_000_000)
	out := make([]plane.Box, 0, n)
	for i := int64(0); i < int64(n); i++ {
		x0, y0 := -limit+2*i, -limit+i
		out = append(out, plane.Box{MinX: x0, MinY: y0, MaxX: x0 + 1, MaxY: y0 + 1})
	}
	return out
}

// ProbeExampleLongStrips staggers x ends and overlaps y bands so many strips
// are live at one sweep x. depth is the overlap band in units of the y step.
func ProbeExampleLongStrips(n int, depth int64) []plane.Box {
	limit := int64(1_000_000_000)
	out := make([]plane.Box, 0, n)
	for i := int64(0); i < int64(n); i++ {
		y0 := i * 10
		out = append(out, plane.Box{MinX: -limit + i, MinY: y0, MaxX: limit - i, MaxY: y0 + depth*10})
	}
	return out
}

func probeFullWindow() plane.Box {
	limit := int64(1_000_000_000)
	return plane.Box{MinX: -limit, MinY: -limit, MaxX: limit, MaxY: limit}
}

func TestCoverageDecisionMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	window := plane.Box{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12}
	for trial := 0; trial < 600; trial++ {
		rects := probeRandom(rng, rng.Intn(14), 12)
		res, _ := probeRun(rects, window)
		cov, rep, _, _ := probeSparse(rects, window)
		refCov, refRep := probeReference(rects, window)
		if res.Covered != cov || res.Repeated != rep ||
			res.Covered != refCov || res.Repeated != refRep {
			t.Fatalf("trial %d: tree=(%d,%d) sparse=(%d,%d) reference=(%d,%d) rects=%v",
				trial, res.Covered, res.Repeated, cov, rep, refCov, refRep, rects)
		}
		if res.Uncovered != window.Area()-res.Covered {
			t.Fatalf("trial %d: uncovered does not close the window", trial)
		}
	}
}

func TestCoverageDecisionLargeCoordinates(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	limit := int64(1_000_000_000)
	window := probeFullWindow()
	for trial := 0; trial < 300; trial++ {
		rects := probeRandomLarge(rng, 20, limit)
		res, _ := probeRun(rects, window)
		cov, rep, _, _ := probeSparse(rects, window)
		refCov, refRep := probeReference(rects, window)
		if res.Covered != cov || res.Repeated != rep ||
			res.Covered != refCov || res.Repeated != refRep {
			t.Fatalf("trial %d: tree=(%d,%d) sparse=(%d,%d) reference=(%d,%d)",
				trial, res.Covered, res.Repeated, cov, rep, refCov, refRep)
		}
		if res.Uncovered < 0 || res.Repeated > res.Covered {
			t.Fatalf("trial %d: invariants broken", trial)
		}
	}
}

func TestCoverageDecisionIntegerBoundaries(t *testing.T) {
	limit := int64(1_000_000_000)
	full := probeFullWindow()
	cases := []struct {
		name   string
		rects  []plane.Box
		window plane.Box
	}{
		{"full-window", []plane.Box{{MinX: -limit, MinY: -limit, MaxX: limit, MaxY: limit}}, full},
		{"two-unit-corners", []plane.Box{
			{MinX: -limit, MinY: -limit, MaxX: -limit + 1, MaxY: -limit + 1},
			{MinX: limit - 1, MinY: limit - 1, MaxX: limit, MaxY: limit},
		}, full},
		{"span-edge-strip", []plane.Box{
			{MinX: -limit, MinY: -limit, MaxX: limit, MaxY: limit},
			{MinX: -limit, MinY: 0, MaxX: limit, MaxY: 1},
		}, full},
		{"half-open-touch-x", []plane.Box{
			{MinX: 0, MinY: 0, MaxX: 1, MaxY: 1},
			{MinX: 1, MinY: 0, MaxX: 2, MaxY: 1},
		}, plane.Box{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2}},
		{"half-open-touch-y", []plane.Box{
			{MinX: 0, MinY: 0, MaxX: 1, MaxY: 1},
			{MinX: 0, MinY: 1, MaxX: 1, MaxY: 2},
		}, plane.Box{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2}},
		{"triple", []plane.Box{
			{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2},
			{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2},
			{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2},
		}, plane.Box{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2}},
		{"overlap-2-1", []plane.Box{
			{MinX: 0, MinY: 0, MaxX: 2, MaxY: 2},
			{MinX: 1, MinY: 1, MaxX: 3, MaxY: 3},
			{MinX: 2, MinY: 2, MaxX: 4, MaxY: 4},
		}, plane.Box{MinX: 0, MinY: 0, MaxX: 4, MaxY: 4}},
	}
	for _, tc := range cases {
		res, _ := probeRun(tc.rects, tc.window)
		cov, rep, _, _ := probeSparse(tc.rects, tc.window)
		refCov, refRep := probeReference(tc.rects, tc.window)
		if res.Covered != cov || res.Repeated != rep ||
			res.Covered != refCov || res.Repeated != refRep {
			t.Fatalf("%s: tree=(%d,%d) sparse=(%d,%d) reference=(%d,%d)",
				tc.name, res.Covered, res.Repeated, cov, rep, refCov, refRep)
		}
		if res.Uncovered != tc.window.Area()-res.Covered {
			t.Fatalf("%s: uncovered does not close the window", tc.name)
		}
	}

	// 4096 identical full-window rectangles: multiplicity must not be summed.
	rects := make([]plane.Box, 4096)
	for i := range rects {
		rects[i] = plane.Box{MinX: -limit, MinY: -limit, MaxX: limit, MaxY: limit}
	}
	res, _ := probeRun(rects, full)
	if res.Covered != full.Area() || res.Repeated != full.Area() || res.Uncovered != 0 {
		t.Fatalf("4096 overlap: covered=%d repeated=%d uncovered=%d", res.Covered, res.Repeated, res.Uncovered)
	}
}

func TestCoverageDecisionExamples(t *testing.T) {
	window := probeFullWindow()

	edge := ProbeExampleEdgeDense(4096)
	res, breakpoints := probeRun(edge, window)
	cov, rep, smax, ssum := probeSparse(edge, window)
	if res.Covered != cov || res.Repeated != rep {
		t.Fatalf("edge-dense examples disagree")
	}
	t.Logf("A edge-dense/empty: R=%d covered=%d repeated=%d exactly_once=%d uncovered=%d B=%d S_max=%d S_sum=%d",
		len(edge), res.Covered, res.Repeated, res.Covered-res.Repeated, res.Uncovered, breakpoints, smax, ssum)

	for layer := 0; layer < 3; layer++ {
		strips := ProbeExampleLongStrips(1024, 290)
		if layer > 0 {
			for i := range strips {
				strips[i].MinY += int64(layer) * 100_000_000
				strips[i].MaxY += int64(layer) * 100_000_000
			}
		}
		res, breakpoints := probeRun(strips, window)
		cov, rep, smax, ssum := probeSparse(strips, window)
		if res.Covered != cov || res.Repeated != rep {
			t.Fatalf("long-strip layer %d disagrees", layer)
		}
		t.Logf("B long-strips layer %d: R=%d covered=%d repeated=%d exactly_once=%d B=%d S_max=%d S_sum=%d",
			layer, len(strips), res.Covered, res.Repeated, res.Covered-res.Repeated, breakpoints, smax, ssum)
	}
}

func probeCollect(tree packed.Tree, window plane.Box) []plane.Box {
	out := make([]plane.Box, 0)
	tree.Search(window, func(row catalog.Feature) bool {
		if row.Shape.Extent == nil {
			return true
		}
		if clip, ok := row.Shape.Extent.Clip(window); ok {
			out = append(out, clip)
		}
		return true
	})
	return out
}

func benchmarkCoverage(b *testing.B, rects []plane.Box) {
	tree := packed.Build(probeFeatures(rects))
	window := probeFullWindow()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		probeSink = Coverage(tree, window, "")
	}
}

func benchmarkSparse(b *testing.B, rects []plane.Box) {
	tree := packed.Build(probeFeatures(rects))
	window := probeFullWindow()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collected := probeCollect(tree, window)
		probeSparse(collected, window)
	}
}

func BenchmarkCoverageEdgeDenseEmpty(b *testing.B) {
	benchmarkCoverage(b, ProbeExampleEdgeDense(4096))
}

func BenchmarkProbeSparseEdgeDenseEmpty(b *testing.B) {
	benchmarkSparse(b, ProbeExampleEdgeDense(4096))
}

func BenchmarkCoverageLongStripOverlap(b *testing.B) {
	benchmarkCoverage(b, ProbeExampleLongStrips(1024, 290))
}

func BenchmarkProbeSparseLongStripOverlap(b *testing.B) {
	benchmarkSparse(b, ProbeExampleLongStrips(1024, 290))
}

func BenchmarkCoverageCapacityCeiling(b *testing.B) {
	benchmarkCoverage(b, ProbeExampleLongStrips(4096, 4096))
}

func BenchmarkProbeSparseCapacityCeiling(b *testing.B) {
	benchmarkSparse(b, ProbeExampleLongStrips(4096, 4096))
}

// TestCoverageDecisionSwitchScan measures the observable that actually decides
// the switch. The decisive quantity is the peak number of live y segments
// S_max (or, equivalently, how many distinct y counts a strip carries), not
// the coordinate span. B and S_sum are reported alongside so the decision can
// be re-derived from one run.
func TestCoverageDecisionSwitchScan(t *testing.T) {
	if testing.Short() || os.Getenv("MERIDIAN_COVERAGE_SCAN") == "" {
		t.Skip("timing scan: set MERIDIAN_COVERAGE_SCAN=1")
	}
	t.Logf("%4s %7s %7s %9s %12s %12s %9s %9s", "c", "B", "S_max", "S_sum",
		"tree_ns/op", "sparse_ns/op", "ratio", "winner")
	for _, depth := range []int64{1, 2, 3, 4, 6, 8, 10, 20, 40, 80, 160, 320, 640} {
		rects := ProbeExampleLongStrips(1024, depth)
		window := probeFullWindow()

		_, breakpoints := probeRun(rects, window)
		_, _, smax, ssum := probeSparse(rects, window)

		tree := packed.Build(probeFeatures(rects))
		treeRes := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				probeSink = Coverage(tree, window, "")
			}
		})
		sparseRes := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				probeSparse(probeCollect(tree, window), window)
			}
		})
		treeNs := float64(treeRes.NsPerOp())
		sparseNs := float64(sparseRes.NsPerOp())
		winner := "tree"
		if sparseNs < treeNs {
			winner = "sparse"
		}
		t.Logf("%4d %7d %7d %9d %12.0f %12.0f %9.2f %9s",
			depth, breakpoints, smax, ssum, treeNs, sparseNs, sparseNs/treeNs, winner)
	}
}

// TestCoverageDecisionCrossoverByBreakpoints checks whether the switch point
// tracks log2(B) or just the strip count. If the per-event cost of the tree is
// O(log B) and that of the sparse model is O(S_max), then the crossover S_max
// should rise with B at a fixed constant factor.
func TestCoverageDecisionCrossoverByBreakpoints(t *testing.T) {
	if testing.Short() || os.Getenv("MERIDIAN_COVERAGE_SCAN") == "" {
		t.Skip("timing scan: set MERIDIAN_COVERAGE_SCAN=1")
	}
	window := probeFullWindow()
	t.Logf("%5s %7s %4s %7s %12s %12s %9s", "R", "B", "c", "S_max", "tree_ns/op", "sparse_ns/op", "ratio")
	for _, n := range []int{512, 1024, 2048, 4096} {
		for _, depth := range []int64{1, 2, 4} {
			rects := ProbeExampleLongStrips(n, depth)
			_, breakpoints := probeRun(rects, window)
			_, _, smax, _ := probeSparse(rects, window)
			tree := packed.Build(probeFeatures(rects))
			treeNs := float64(testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					probeSink = Coverage(tree, window, "")
				}
			}).NsPerOp())
			sparseNs := float64(testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					probeSparse(probeCollect(tree, window), window)
				}
			}).NsPerOp())
			t.Logf("%5d %7d %4d %7d %12.0f %12.0f %9.2f", n, breakpoints, depth, smax,
				treeNs, sparseNs, sparseNs/treeNs)
		}
	}
}
