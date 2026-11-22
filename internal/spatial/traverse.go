package spatial

import (
	"cmp"
	"errors"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const (
	// MaxTraversals bounds the number of entry/leave crossings one query may
	// report, including repeated visits to the same feature.
	MaxTraversals = 2000
)

var ErrTraversalBudget = errors.New("traversal exceeds crossing or candidate budget")

// Crossing is one continuous run of a polyline through the open interior of a
// rectangle. The same feature appears more than once when the polyline leaves
// and re-enters it.
type Crossing struct {
	ID    string         `json:"id"`
	Enter plane.RatPoint `json:"enter"`
	Leave plane.RatPoint `json:"leave"`
}

// Traversal is the ordered list of crossings over one immutable layer view.
type Traversal struct {
	Crossings []Crossing   `json:"crossings"`
	Visit     packed.Visit `json:"visit"`
}

type span struct {
	enter plane.Rat
	leave plane.Rat
}

type featureRun struct {
	id    string
	spans []span
}

type orderedCrossing struct {
	id   string
	span span
}

// Traverse walks an integer polyline in its given order and reports every
// rectangle whose open interior the line passes through, with exact entry and
// leave positions. Edge-only contact never yields a crossing; a revisit after
// the line has fully left a feature yields a second run.
func Traverse(tree packed.Tree, points []plane.Point, class string) (Traversal, error) {
	envelope := points[0].Envelope()
	for _, p := range points[1:] {
		envelope = envelope.Union(p.Envelope())
	}
	candidates := make([]catalog.Feature, 0)
	remaining := maxJoinCandidates
	var exhausted bool
	visit := tree.Search(envelope, func(row catalog.Feature) bool {
		remaining--
		if remaining < 0 {
			exhausted = true
			return false
		}
		if class == "" || row.Class == class {
			candidates = append(candidates, row)
		}
		return true
	})
	if exhausted {
		return Traversal{}, ErrTraversalBudget
	}
	runs := make([]featureRun, 0, len(candidates))
	total := 0
	for _, row := range candidates {
		spans := interiorSpans(points, *row.Shape.Extent)
		if len(spans) > 0 {
			runs = append(runs, featureRun{id: row.ID, spans: spans})
			total += len(spans)
		}
	}
	if total > MaxTraversals {
		return Traversal{}, ErrTraversalBudget
	}
	ordered := make([]orderedCrossing, 0, total)
	for _, run := range runs {
		for _, part := range run.spans {
			ordered = append(ordered, orderedCrossing{id: run.id, span: part})
		}
	}
	// Travel order first; the feature ID only resolves coincident entries,
	// which keeps responses deterministic for overlapping rectangles.
	slices.SortFunc(ordered, func(a, b orderedCrossing) int {
		if order := a.span.enter.Cmp(b.span.enter); order != 0 {
			return order
		}
		if order := cmp.Compare(a.id, b.id); order != 0 {
			return order
		}
		return a.span.leave.Cmp(b.span.leave)
	})
	crossings := make([]Crossing, 0, len(ordered))
	for _, entry := range ordered {
		crossings = append(crossings, Crossing{
			ID:    entry.id,
			Enter: plane.PointAt(points, entry.span.enter),
			Leave: plane.PointAt(points, entry.span.leave),
		})
	}
	return Traversal{Crossings: crossings, Visit: visit}, nil
}

// interiorSpans computes the open-interior runs of one rectangle over the
// whole polyline. Runs only join when the shared polyline vertex is itself
// strictly inside the rectangle; a bounce off an edge or corner closes the
// run at the boundary, so the next interior stretch starts a second one.
func interiorSpans(points []plane.Point, box plane.Box) []span {
	spans := make([]span, 0)
	for i := 0; i+1 < len(points); i++ {
		local, inside := plane.SegmentInterior(points[i], points[i+1], box)
		if !inside {
			continue
		}
		current := span{enter: globalParam(i, local.Lo()), leave: globalParam(i, local.Hi())}
		if n := len(spans); n > 0 {
			// The shared vertex joins the runs only when it lies strictly
			// inside the box; a boundary touch ends the previous run instead.
			vertex := plane.NewRat(int64(i), 1)
			joined := spans[n-1].leave.Cmp(vertex) == 0 &&
				current.enter.Cmp(vertex) == 0 && box.Owns(points[i])
			if joined {
				if current.leave.Cmp(spans[n-1].leave) > 0 {
					spans[n-1].leave = current.leave
				}
				continue
			}
		}
		spans = append(spans, current)
	}
	return spans
}

func globalParam(segment int, local plane.Rat) plane.Rat {
	return plane.NewRat(int64(segment)*local.Den+local.Num, local.Den)
}
