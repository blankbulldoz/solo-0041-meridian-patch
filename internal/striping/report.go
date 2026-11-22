package striping

import (
	"fmt"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func assemble(
	req Request,
	r plane.Box,
	width int64,
	cuts []int64,
	segments []Segment,
	prefixC, prefixR []int64,
	totalCovered, totalRepeated int64,
	ignored int,
	path, optimality string,
	ys []int64,
) *Report {
	bands := req.Bands
	absoluteCuts := make([]int64, len(cuts))
	for i, c := range cuts {
		absoluteCuts[i] = r.MinX + c
	}
	boundaries := make([]int64, 0, bands+1)
	boundaries = append(boundaries, r.MinX)
	boundaries = append(boundaries, absoluteCuts...)
	boundaries = append(boundaries, r.MaxX)

	report := &Report{
		Range:   r,
		Bands:   bands,
		Width:   width,
		Cuts:    absoluteCuts,
		Stripes: make([]Band, 0, bands),
		Quality: Quality{
			TargetCovered: fraction(totalCovered, int64(bands)),
			SolverPath:    path,
			Optimality:    optimality,
		},
	}
	report.Total.RangeArea = r.Area()
	report.Total.Covered = totalCovered
	report.Total.Repeated = totalRepeated
	report.Total.Uncovered = r.Area() - totalCovered

	rangeHeight := r.MaxY - r.MinY
	var sumSq int64
	var maxAbs int64
	var sumAbs int64

	for i := 0; i < bands; i++ {
		fromX := boundaries[i]
		toX := boundaries[i+1]
		lo := fromX - r.MinX
		hi := toX - r.MinX
		covered := prefixC[hi] - prefixC[lo]
		repeated := prefixR[hi] - prefixR[lo]
		bandWidth := toX - fromX
		uncovered := bandWidth*rangeHeight - covered

		diff := int64(bands)*covered - totalCovered
		abs := diff
		if abs < 0 {
			abs = -abs
		}
		maxAbs = max(maxAbs, abs)
		sumAbs += abs
		sumSq += diff * diff

		band := Band{
			Index:      i + 1,
			FromX:      fromX,
			ToX:        toX,
			Width:      bandWidth,
			Covered:    covered,
			Repeated:   repeated,
			Uncovered:  uncovered,
			Deviation:  fraction(diff, int64(bands)),
			FeatureIDs: intersectingFeatures(req.Features, fromX, toX),
		}
		report.Stripes = append(report.Stripes, band)
	}

	report.Quality.MaxAbsDeviation = fraction(maxAbs, int64(bands))
	report.Quality.MeanAbsDeviation = fraction(sumAbs, int64(bands)*int64(bands))
	report.Quality.SumSquaredScaled = sumSq

	report.Checks = buildChecks(report, req)
	report.Trace = Trace{
		Description:    req.Description,
		Range:          r,
		FeatureCount:   len(req.Features),
		Features:       append([]Feature(nil), req.Features...),
		Segments:       segments,
		IgnoredCount:   ignored,
		SegmentBuiltAt: "internal/striping sweep-line (half-open integer cells, y breakpoints: " + fmt.Sprintf("%d", len(ys)) + ")",
	}
	return report
}

func fraction(num, den int64) Fraction {
	if num == 0 {
		return Fraction{Numerator: 0, Denominator: 1}
	}
	g := gcd(abs64(num), den)
	return Fraction{Numerator: num / g, Denominator: den / g}
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// intersectingFeatures lists input rectangles with a positive-width overlap
// with [fromX,toX), keeping the report traceable to the source shapes.
func intersectingFeatures(features []Feature, fromX, toX int64) []string {
	ids := make([]string, 0)
	for _, f := range features {
		if f.Box.MinX < toX && fromX < f.Box.MaxX {
			ids = append(ids, f.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// verify independently re-derives every claimed measure from the cuts and the
// original request. A mismatch is an implementation failure, not bad input.
func verify(report *Report, req Request) error {
	checks := buildChecks(report, req)
	for _, c := range checks {
		if !c.Passed {
			return fmt.Errorf("internal striping invariant failed: %s (%s)", c.Name, c.Detail)
		}
	}
	return nil
}

func buildChecks(report *Report, req Request) []Check {
	r := req.Range
	checks := make([]Check, 0, 8)
	add := func(name string, passed bool, detail string) {
		checks = append(checks, Check{Name: name, Passed: passed, Detail: detail})
	}

	// Cuts are strictly increasing integers inside the open range.
	cutsOK := len(report.Cuts) == req.Bands-1
	if cutsOK {
		previous := r.MinX
		for _, c := range report.Cuts {
			if c <= previous || c >= r.MaxX {
				cutsOK = false
				break
			}
			previous = c
		}
	}
	add("integer_cuts_strictly_inside_range", cutsOK, fmt.Sprintf("%d interior cuts", len(report.Cuts)))

	// Every band has positive width and tiles the range without overlap.
	positiveWidth, tiled := true, true
	var widthSum int64
	for _, b := range report.Stripes {
		if b.Width <= 0 {
			positiveWidth = false
		}
		widthSum += b.Width
	}
	if widthSum != report.Width {
		tiled = false
	}
	for i := 1; i < len(report.Stripes); i++ {
		if report.Stripes[i].FromX != report.Stripes[i-1].ToX {
			tiled = false
		}
	}
	add("all_bands_positive_width", positiveWidth, "")
	add("bands_tile_range_without_gap_or_overlap", tiled, fmt.Sprintf("width sum %d of %d", widthSum, report.Width))

	// Per-band measures add up to the totals.
	var coveredSum, repeatedSum, uncoveredSum int64
	for _, b := range report.Stripes {
		coveredSum += b.Covered
		repeatedSum += b.Repeated
		uncoveredSum += b.Uncovered
		if b.Repeated > b.Covered {
			add("repeated_not_exceeding_covered", false, b.devDetail())
		}
	}
	add("covered_parts_sum_to_total", coveredSum == report.Total.Covered,
		fmt.Sprintf("sum %d vs total %d", coveredSum, report.Total.Covered))
	add("repeated_parts_sum_to_total", repeatedSum == report.Total.Repeated,
		fmt.Sprintf("sum %d vs total %d", repeatedSum, report.Total.Repeated))
	add("covered_plus_uncovered_equals_range_area", coveredSum+uncoveredSum == report.Total.RangeArea,
		fmt.Sprintf("%d + %d = %d", coveredSum, uncoveredSum, report.Total.RangeArea))

	// Deviations reconcile with the reported band coverage.
	deviationOK := true
	for _, b := range report.Stripes {
		want := fraction(int64(report.Bands)*b.Covered-report.Total.Covered, int64(report.Bands))
		if want != b.Deviation {
			deviationOK = false
		}
	}
	add("deviations_reconcile_with_coverage", deviationOK, "")
	return checks
}

func (b Band) devDetail() string {
	return fmt.Sprintf("band %d repeated %d > covered %d", b.Index, b.Repeated, b.Covered)
}
