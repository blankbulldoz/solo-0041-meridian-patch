// Package striping partitions an integer x range into a requested number of
// vertical bands whose covered area is as evenly distributed as possible.
//
// Coverage follows the database convention: rectangles are half-open
// [min_x,max_x) × [min_y,max_y); "covered" counts area covered at least once
// and "repeated" counts area covered at least twice (a point covered by three
// rectangles is still one unit of repeated area, not two). Empty space inside
// the range contributes to neither measure, so a wide gap never inflates the
// coverage of a neighbouring band.
//
// All cut coordinates are integer x values and every band has a strictly
// positive width, including bands that happen to carry no coverage.
package striping

import (
	"cmp"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// MaxBands bounds solver work and keeps scaled deviation arithmetic inside
// int64 even for the coordinate limit used by the database.
const MaxBands = 10_000

// Feature is one traceable input rectangle.
type Feature struct {
	ID    string    `json:"id"`
	Class string    `json:"class,omitempty"`
	Box   plane.Box `json:"box"`
}

// Request is the striping job description.
type Request struct {
	Range       plane.Box `json:"range"`
	Bands       int       `json:"bands"`
	Features    []Feature `json:"features"`
	Description string    `json:"description,omitempty"`
}

// Segment is an x interval on which per-column covered and repeated y-length
// are constant. Segments tile the full integer range with no gaps.
type Segment struct {
	FromX        int64 `json:"from_x"`
	ToX          int64 `json:"to_x"`
	CoveredYLen  int64 `json:"covered_y_length"`
	RepeatedYLen int64 `json:"repeated_y_length"`
}

// Band is one produced vertical band.
type Band struct {
	Index      int      `json:"index"`
	FromX      int64    `json:"from_x"`
	ToX        int64    `json:"to_x"`
	Width      int64    `json:"width"`
	Covered    int64    `json:"covered"`
	Repeated   int64    `json:"repeated"`
	Uncovered  int64    `json:"uncovered"`
	Deviation  Fraction `json:"deviation"`
	FeatureIDs []string `json:"feature_ids"`
}

// Fraction is an exact rational deviation from the target area.
type Fraction struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
}

// Rat renders the fraction as an exact rational.
func (f Fraction) Rat() *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(f.Numerator), big.NewInt(f.Denominator))
}

func (f Fraction) String() string {
	if f.Numerator == 0 {
		return "0"
	}
	if f.Denominator == 1 {
		return fmt.Sprintf("%d", f.Numerator)
	}
	return fmt.Sprintf("%d/%d", f.Numerator, f.Denominator)
}

// Check records an invariant that was verified while building the report.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Trace captures how the output can be traced back to the input.
type Trace struct {
	Description    string    `json:"description,omitempty"`
	Range          plane.Box `json:"range"`
	FeatureCount   int       `json:"feature_count"`
	Features       []Feature `json:"features"`
	Segments       []Segment `json:"segments"`
	IgnoredCount   int       `json:"ignored_outside_range"`
	SegmentBuiltAt string    `json:"solver"`
}

// Quality summarises how far the partition is from the even target.
type Quality struct {
	TargetCovered    Fraction `json:"target_covered_per_band"`
	MaxAbsDeviation  Fraction `json:"max_abs_deviation"`
	SumSquaredScaled int64    `json:"sum_squared_scaled_deviation"`
	MeanAbsDeviation Fraction `json:"mean_abs_deviation"`
	SolverPath       string   `json:"solver_path"`
	Optimality       string   `json:"optimality"`
}

// Report is the full deliverable for the cartography team.
type Report struct {
	Range plane.Box `json:"range"`
	Bands int       `json:"bands"`
	Width int64     `json:"range_width"`
	Cuts  []int64   `json:"cuts"`
	Total struct {
		RangeArea int64 `json:"range_area"`
		Covered   int64 `json:"covered"`
		Repeated  int64 `json:"repeated"`
		Uncovered int64 `json:"uncovered"`
	} `json:"totals"`
	Stripes []Band  `json:"stripes"`
	Quality Quality `json:"quality"`
	Checks  []Check `json:"checks"`
	Trace   Trace   `json:"trace"`
}

type event struct {
	x      int64
	y0, y1 int64
	delta  int
}

// Plan validates the request, measures coverage and computes the partition.
func Plan(req Request) (*Report, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	r := req.Range
	width := r.MaxX - r.MinX

	events, ignored, err := buildEvents(r, req.Features)
	if err != nil {
		return nil, err
	}
	segments, ys := sweep(r, events)

	// Prefix coverage and repeated area over integer columns x in [MinX,x).
	prefixC, prefixR, maxColC, err := buildPrefixes(r, segments)
	if err != nil {
		return nil, err
	}
	totalCovered := prefixC[width]
	totalRepeated := prefixR[width]

	var cuts []int64
	var path string
	var optimality string
	switch {
	case width == int64(req.Bands):
		cuts = unitCuts(width)
		path = "unit-width"
		optimality = "exact: every integer column becomes one positive-width band"
	case exactBudget(width, req.Bands):
		cuts = exactDynamicCuts(width, req.Bands, totalCovered, prefixC)
		path = "exact-dp"
		optimality = "exact: lexicographic minimum of (max |N·covered-total|, sum squared scaled deviation)"
	default:
		var err error
		cuts, err = minimaxCuts(width, req.Bands, totalCovered, prefixC, maxColC)
		if err != nil {
			return nil, err
		}
		path = "minimax-reachability"
		optimality = "exact minimum max deviation via layered reachability; quantile-close tie breaks"
	}

	report := assemble(req, r, width, cuts, segments, prefixC, prefixR, totalCovered, totalRepeated, ignored, path, optimality, ys)
	if err := verify(report, req); err != nil {
		return nil, err
	}
	return report, nil
}

func validate(req Request) error {
	if err := req.Range.Validate(); err != nil {
		return fmt.Errorf("striping range: %w", err)
	}
	if req.Bands < 1 {
		return errors.New("bands must be at least 1")
	}
	if req.Bands > MaxBands {
		return fmt.Errorf("bands must not exceed %d", MaxBands)
	}
	width := req.Range.MaxX - req.Range.MinX
	if int64(req.Bands) > width {
		return fmt.Errorf("bands %d exceed integer range width %d: positive-width bands are mandatory", req.Bands, width)
	}
	if req.Range.MaxY <= req.Range.MinY {
		return errors.New("striping range must have positive height")
	}
	seen := make(map[string]struct{}, len(req.Features))
	for i, f := range req.Features {
		if f.ID == "" {
			return fmt.Errorf("feature at index %d has no id", i)
		}
		if _, dup := seen[f.ID]; dup {
			return fmt.Errorf("duplicate feature id %q", f.ID)
		}
		seen[f.ID] = struct{}{}
		if err := f.Box.Validate(); err != nil {
			return fmt.Errorf("feature %q: %w", f.ID, err)
		}
	}
	return nil
}

func buildEvents(r plane.Box, features []Feature) ([]event, int, error) {
	events := make([]event, 0, 2*len(features))
	ignored := 0
	for _, f := range features {
		clip, ok := r.Clip(f.Box)
		if !ok {
			ignored++
			continue
		}
		events = append(events,
			event{x: clip.MinX, y0: clip.MinY, y1: clip.MaxY, delta: 1},
			event{x: clip.MaxX, y0: clip.MinY, y1: clip.MaxY, delta: -1},
		)
	}
	return events, ignored, nil
}

// sweep returns constant-measure segments tiling the whole range and the
// sorted distinct y coordinates used by the coverage length tree.
func sweep(r plane.Box, events []event) ([]Segment, []int64) {
	ySet := make(map[int64]struct{})
	for _, e := range events {
		ySet[e.y0] = struct{}{}
		ySet[e.y1] = struct{}{}
	}
	ys := make([]int64, 0, len(ySet))
	for y := range ySet {
		ys = append(ys, y)
	}
	slices.Sort(ys)

	slices.SortFunc(events, func(a, b event) int { return cmp.Compare(a.x, b.x) })
	tree := newCoverTree(ys)
	segments := make([]Segment, 0)
	previous := r.MinX
	index := 0
	for index < len(events) {
		x := events[index].x
		if x > previous {
			segments = append(segments, Segment{
				FromX:        previous,
				ToX:          x,
				CoveredYLen:  tree.covered(),
				RepeatedYLen: tree.repeated(),
			})
		}
		for index < len(events) && events[index].x == x {
			e := events[index]
			lo, _ := slices.BinarySearch(ys, e.y0)
			hi, _ := slices.BinarySearch(ys, e.y1)
			tree.update(1, 0, len(ys)-1, lo, hi, e.delta)
			index++
		}
		previous = x
	}
	if previous < r.MaxX {
		segments = append(segments, Segment{
			FromX:        previous,
			ToX:          r.MaxX,
			CoveredYLen:  tree.covered(),
			RepeatedYLen: tree.repeated(),
		})
	}
	return segments, ys
}

func buildPrefixes(r plane.Box, segments []Segment) (covered []int64, repeated []int64, maxColCovered int64, err error) {
	width := r.MaxX - r.MinX
	covered = make([]int64, width+1)
	repeated = make([]int64, width+1)
	var c, rp int64
	column := 0
	for _, s := range segments {
		for x := s.FromX; x < s.ToX; x++ {
			c += s.CoveredYLen
			rp += s.RepeatedYLen
			column++
			covered[column] = c
			repeated[column] = rp
			if s.CoveredYLen > maxColCovered {
				maxColCovered = s.CoveredYLen
			}
		}
	}
	if c > plane.CoordinateLimit*plane.CoordinateLimit {
		return nil, nil, 0, errors.New("covered area exceeds int64 safety bound")
	}
	return covered, repeated, maxColCovered, nil
}

// unitCuts returns the relative interior cuts 1..width-1, giving every
// integer column its own positive-width band.
func unitCuts(width int64) []int64 {
	cuts := make([]int64, 0, width-1)
	for k := int64(1); k < width; k++ {
		cuts = append(cuts, k)
	}
	return cuts
}

// exactBudget keeps the O(bands · width²) dynamic program comfortably fast.
func exactBudget(width int64, bands int) bool {
	work := uint64(bands) * uint64(width) * uint64(width)
	return work <= 25_000_000
}
