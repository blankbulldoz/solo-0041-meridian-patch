package striping

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func TestContinuousSlabReport(t *testing.T) {
	req := Request{
		Range: box(0, 0, 10, 12),
		Bands: 4,
		Features: []Feature{
			feature("slab-a", box(0, 0, 10, 8)),
			feature("slab-b", box(4, 0, 10, 11)),
			feature("overlap-cap", box(2, 8, 6, 10)),
		},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Total.Covered != 102 || report.Total.Repeated != 52 {
		t.Fatalf("totals = covered %d repeated %d, want 102/52", report.Total.Covered, report.Total.Repeated)
	}
	if got := report.Quality.TargetCovered; got.Numerator != 51 || got.Denominator != 2 {
		t.Fatalf("target = %s, want 51/2", got)
	}
	var coveredSum, repeatedSum int64
	for _, b := range report.Stripes {
		coveredSum += b.Covered
		repeatedSum += b.Repeated
		if b.Width <= 0 {
			t.Fatalf("band %d has non-positive width", b.Index)
		}
		if b.Repeated > b.Covered {
			t.Fatalf("band %d repeated %d > covered %d", b.Index, b.Repeated, b.Covered)
		}
	}
	if coveredSum != 102 || repeatedSum != 52 {
		t.Fatalf("band sums %d/%d do not match totals", coveredSum, repeatedSum)
	}
	for _, c := range report.Checks {
		if !c.Passed {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
	if report.Trace.FeatureCount != 3 || len(report.Trace.Features) != 3 {
		t.Fatalf("trace lineage lost: %+v", report.Trace)
	}
}

func TestWideGapDoesNotInflateCoverage(t *testing.T) {
	req := Request{
		Range: box(0, 0, 20, 10),
		Bands: 4,
		Features: []Feature{
			feature("west-field", box(1, 1, 6, 9)),
			feature("west-overlay", box(3, 2, 6, 7)),
			feature("east-field", box(10, 1, 17, 9)),
			feature("east-overlay", box(14, 2, 17, 7)),
		},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// The 4-column gap (x in [6,10)) and side margins must show zero coverage.
	for _, s := range report.Trace.Segments {
		if s.FromX >= 6 && s.ToX <= 10 && s.CoveredYLen != 0 {
			t.Fatalf("gap segment %+v carries coverage", s)
		}
	}
	if report.Total.Covered != 96 || report.Total.Repeated != 30 {
		t.Fatalf("totals = %d/%d, want 96/30", report.Total.Covered, report.Total.Repeated)
	}
	for _, b := range report.Stripes {
		if b.Covered != 24 {
			t.Fatalf("band %d covered %d, want perfectly even 24", b.Index, b.Covered)
		}
		if b.Deviation.Numerator != 0 {
			t.Fatalf("band %d deviation %s, want zero", b.Index, b.Deviation)
		}
	}
	// Some band must span the gap and report the gap as uncovered area.
	var spanned bool
	for _, b := range report.Stripes {
		if b.FromX <= 6 && b.ToX >= 10 {
			spanned = true
			if b.Uncovered <= 0 {
				t.Fatalf("gap-spanning band reports no uncovered area: %+v", b)
			}
		}
	}
	if !spanned {
		t.Fatalf("no band spans the empty gap; cuts %v", report.Cuts)
	}
}

func TestRepeatedAreaIsUnionNotMultiplicity(t *testing.T) {
	// Three stacked rectangles over 2 columns: covered 6, repeated 6 (not 12).
	req := Request{
		Range: box(0, 0, 4, 3),
		Bands: 2,
		Features: []Feature{
			feature("r1", box(0, 0, 2, 3)),
			feature("r2", box(0, 0, 2, 3)),
			feature("r3", box(0, 0, 2, 3)),
		},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Total.Covered != 6 || report.Total.Repeated != 6 {
		t.Fatalf("covered/repeated = %d/%d, want 6/6", report.Total.Covered, report.Total.Repeated)
	}
}

func TestUnitWidthBands(t *testing.T) {
	req := Request{
		Range:    box(0, 0, 5, 2),
		Bands:    5,
		Features: []Feature{feature("r", box(1, 0, 4, 2))},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Quality.SolverPath != "unit-width" {
		t.Fatalf("solver path = %q, want unit-width", report.Quality.SolverPath)
	}
	for _, b := range report.Stripes {
		if b.Width != 1 {
			t.Fatalf("band width %d, want 1", b.Width)
		}
	}
}

func TestRejectsMoreBandsThanColumns(t *testing.T) {
	req := Request{Range: box(0, 0, 3, 2), Bands: 4}
	_, err := Plan(req)
	if err == nil || !strings.Contains(err.Error(), "positive-width") {
		t.Fatalf("err = %v, want positive-width rejection", err)
	}
}

func TestRejectsInvalidRangeAndDuplicateIDs(t *testing.T) {
	if _, err := Plan(Request{Range: plane.Box{MinX: 5, MinY: 0, MaxX: 5, MaxY: 2}, Bands: 1}); err == nil {
		t.Fatal("accepted zero-width range")
	}
	req := Request{
		Range:    box(0, 0, 4, 4),
		Bands:    2,
		Features: []Feature{feature("dup", box(0, 0, 1, 1)), feature("dup", box(1, 1, 2, 2))},
	}
	if _, err := Plan(req); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want duplicate rejection", err)
	}
}

func TestFeaturesOutsideRangeAreIgnoredAndTraced(t *testing.T) {
	req := Request{
		Range: box(0, 0, 5, 5),
		Bands: 2,
		Features: []Feature{
			feature("inside", box(0, 0, 2, 5)),
			feature("outside", box(6, 0, 8, 5)),
			feature("edge-touch", box(5, 0, 7, 5)), // only touches the open right edge
		},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Trace.IgnoredCount != 2 {
		t.Fatalf("ignored = %d, want 2", report.Trace.IgnoredCount)
	}
	if report.Total.Covered != 10 {
		t.Fatalf("covered = %d, want 10", report.Total.Covered)
	}
}

func TestSingleBandCoversWholeRange(t *testing.T) {
	req := Request{
		Range:    box(0, 0, 6, 4),
		Bands:    1,
		Features: []Feature{feature("r", box(1, 1, 5, 3))},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(report.Cuts) != 0 || len(report.Stripes) != 1 {
		t.Fatalf("single band produced cuts %v", report.Cuts)
	}
	b := report.Stripes[0]
	if b.FromX != 0 || b.ToX != 6 || b.Covered != 8 || b.Repeated != 0 {
		t.Fatalf("unexpected single band: %+v", b)
	}
	if b.Deviation.Numerator != 0 {
		t.Fatalf("single band deviation = %s, want 0", b.Deviation)
	}
}

func TestEmptyRangePartitionsByWidth(t *testing.T) {
	req := Request{Range: box(0, 0, 8, 3), Bands: 4}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Total.Covered != 0 {
		t.Fatalf("covered = %d, want 0", report.Total.Covered)
	}
	for _, b := range report.Stripes {
		if b.Covered != 0 || b.Deviation.Numerator != 0 || b.Width <= 0 {
			t.Fatalf("empty-range band invalid: %+v", b)
		}
	}
}

func TestLargeWidthUsesReachabilitySolver(t *testing.T) {
	// 6,000 columns exceed the exact-DP budget and force the reachability path.
	features := make([]Feature, 0, 6)
	features = append(features,
		feature("left-block", box(0, 0, 1000, 10)),
		feature("right-block", box(5000, 0, 6000, 10)),
	)
	req := Request{
		Range:    box(0, 0, 6000, 10),
		Bands:    6,
		Features: features,
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if report.Quality.SolverPath != "minimax-reachability" {
		t.Fatalf("solver path = %q, want minimax-reachability", report.Quality.SolverPath)
	}
	var sum int64
	for _, b := range report.Stripes {
		if b.Width <= 0 {
			t.Fatalf("non-positive band width: %+v", b)
		}
		sum += b.Covered
	}
	if sum != report.Total.Covered {
		t.Fatalf("covered sum %d != total %d", sum, report.Total.Covered)
	}
	for _, c := range report.Checks {
		if !c.Passed {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

func TestBothSolverPathsAgreeOnOptimum(t *testing.T) {
	rng := rand.New(rand.NewSource(424242))
	for trial := 0; trial < 40; trial++ {
		width := int64(30 + rng.Intn(60))
		bands := 1 + rng.Intn(min(6, int(width)))
		prefix := make([]int64, width+1)
		var maxCol int64
		for i := 1; i <= int(width); i++ {
			step := int64(rng.Intn(5)) // includes empty columns
			prefix[i] = prefix[i-1] + step
			maxCol = max(maxCol, step)
		}
		total := prefix[width]
		dpCuts := exactDynamicCuts(width, bands, total, prefix)
		mmCuts, err := minimaxCuts(width, bands, total, prefix, maxCol)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		dpScore := scoreOf(width, bands, total, prefix, dpCuts)
		mmScore := scoreOf(width, bands, total, prefix, mmCuts)
		if dpScore.maxAbs != mmScore.maxAbs {
			t.Fatalf("trial %d: exact maxAbs %d != reachability maxAbs %d", trial, dpScore.maxAbs, mmScore.maxAbs)
		}
	}
}

// TestSlidingWindowMatchesPlainRecurrence checks the linear sliding-window
// reachability layer by layer against an independent O(n·width²) recurrence on
// prefixes with many flat plateaus and empty columns, where pointer handling
// is most error-prone.
func TestSlidingWindowMatchesPlainRecurrence(t *testing.T) {
	rng := rand.New(rand.NewSource(777))
	for trial := 0; trial < 80; trial++ {
		width := int64(8 + rng.Intn(40))
		bands := 1 + rng.Intn(min(5, int(width)))
		n := int64(bands)
		prefix := make([]int64, width+1)
		for i := 1; i <= int(width); i++ {
			var step int64
			switch rng.Intn(3) {
			case 0:
				step = 0 // frequent empty columns / plateaus
			case 1:
				step = int64(1 + rng.Intn(2))
			default:
				step = int64(3 + rng.Intn(4))
			}
			prefix[i] = prefix[i-1] + step
		}
		total := prefix[width]
		for _, limit := range []int64{0, total / 3, total / 2, total, n*5 + total} {
			lo, hi := boundedWindow(total, limit, n)
			ok, parents := reachabilityLayers(width, n, lo, hi, prefix, true, total)

			plain := make([][]bool, n+1)
			for k := range plain {
				plain[k] = make([]bool, width+1)
			}
			plain[0][0] = true
			for k := int64(1); k <= n; k++ {
				for x := k; x <= width; x++ {
					for j := k - 1; j < x; j++ {
						if plain[k-1][j] {
							sum := prefix[x] - prefix[j]
							if sum >= lo && sum <= hi {
								plain[k][x] = true
								break
							}
						}
					}
				}
			}
			if ok != plain[n][width] {
				t.Fatalf("trial %d limit %d: feasibility %v != plain %v", trial, limit, ok, plain[n][width])
			}
			for k := int64(1); k <= n; k++ {
				if parents[k-1] == nil {
					// Production stops at the first empty layer; nothing later
					// can be reachable, which the plain recurrence must confirm.
					for x := k; x <= width; x++ {
						if plain[k][x] {
							t.Fatalf("trial %d limit %d layer %d: stopped early but plain reaches x=%d", trial, limit, k, x)
						}
					}
					continue
				}
				for x := k; x <= width; x++ {
					reached := parents[k-1][x] >= 0
					if reached != plain[k][x] {
						t.Fatalf("trial %d limit %d layer %d x %d: sliding reached=%v plain=%v",
							trial, limit, k, x, reached, plain[k][x])
					}
					if reached {
						j := parents[k-1][x]
						sum := prefix[x] - prefix[j]
						if j >= x || sum < lo || sum > hi || !plain[k-1][j] {
							t.Fatalf("trial %d: invalid parent j=%d for layer %d x=%d", trial, j, k, x)
						}
					}
				}
			}
		}
	}
}

func TestNegativeCoordinatesUseAbsoluteCuts(t *testing.T) {
	req := Request{
		Range: box(-100, -50, -90, -40),
		Bands: 3,
		Features: []Feature{
			feature("neg-a", box(-100, -50, -95, -40)),
			feature("neg-b", box(-97, -50, -90, -45)),
			feature("outside", box(0, 0, 4, 4)),
		},
	}
	report, err := Plan(req)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	previous := req.Range.MinX
	for _, c := range report.Cuts {
		if c <= previous || c >= req.Range.MaxX {
			t.Fatalf("cut %d not strictly inside range", c)
		}
		previous = c
	}
	for _, b := range report.Stripes {
		if b.FromX < req.Range.MinX || b.ToX > req.Range.MaxX || b.Width <= 0 {
			t.Fatalf("band outside range or non-positive: %+v", b)
		}
	}
	if report.Total.Covered != 75 || report.Total.Repeated != 10 {
		t.Fatalf("totals %d/%d, want 75/10", report.Total.Covered, report.Total.Repeated)
	}
	if report.Trace.IgnoredCount != 1 {
		t.Fatalf("ignored %d, want 1", report.Trace.IgnoredCount)
	}
	var sum int64
	for _, b := range report.Stripes {
		sum += b.Covered
	}
	if sum != 75 {
		t.Fatalf("band covered sum %d != 75", sum)
	}
}
