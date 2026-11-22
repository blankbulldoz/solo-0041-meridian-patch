package spatial

import (
	"cmp"
	"errors"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

var ErrSeamBudget = errors.New("seam query exceeds seam or candidate budget")

type SharedSeam struct {
	First  string     `json:"first"`
	Second string     `json:"second"`
	Seam   plane.Seam `json:"seam"`
}

// SeamPair identifies one rectangle pair contributing to a merged segment.
type SeamPair struct {
	First  string `json:"first"`
	Second string `json:"second"`
}

// MergedSeam is a maximal run on one supporting line whose set of
// contributing rectangle pairs is constant. Runs that meet only at an
// endpoint stay separate so their contributor sets are never mixed.
type MergedSeam struct {
	Seam  plane.Seam `json:"seam"`
	Pairs []SeamPair `json:"pairs"`
}

type SeamResult struct {
	Seams    []SharedSeam `json:"seams"`
	Segments []MergedSeam `json:"segments"`
	Visit    packed.Visit `json:"visit"`
}

func Seams(tree packed.Tree, window plane.Box, limit int) (SeamResult, error) {
	result := SeamResult{Seams: make([]SharedSeam, 0), Segments: make([]MergedSeam, 0)}
	// Search the closed envelope to keep features that lie entirely outside the
	// window but share an edge on one of its included sides.
	outer := make([]catalog.Feature, 0)
	result.Visit = tree.Search(window, func(feature catalog.Feature) bool {
		outer = append(outer, feature)
		return true
	})
	for _, row := range outer {
		remaining := maxJoinCandidates - result.Visit.Candidates
		var exhausted bool
		visit := tree.Search(row.Shape.Envelope(), func(other catalog.Feature) bool {
			remaining--
			if remaining < 0 {
				exhausted = true
				return false
			}
			if other.ID <= row.ID {
				return true
			}
			seam, shares := plane.SharedBoundary(*row.Shape.Extent, *other.Shape.Extent, window)
			if !shares {
				return true
			}
			if len(result.Seams) == limit {
				exhausted = true
				return false
			}
			result.Seams = append(result.Seams, SharedSeam{First: row.ID, Second: other.ID, Seam: seam})
			return true
		})
		if exhausted {
			return SeamResult{}, ErrSeamBudget
		}
		result.Visit.Nodes += visit.Nodes
		result.Visit.Candidates += visit.Candidates
	}
	slices.SortFunc(result.Seams, func(a, b SharedSeam) int {
		if order := cmp.Compare(a.First, b.First); order != 0 {
			return order
		}
		return cmp.Compare(a.Second, b.Second)
	})
	result.Segments = mergeSeams(result.Seams)
	return result, nil
}

// mergeSeams groups shared seams by supporting line and sweeps each line
// into maximal runs with a constant contributor set.
func mergeSeams(seams []SharedSeam) []MergedSeam {
	type line struct {
		axis  string
		fixed int64
	}
	groups := make(map[line][]SharedSeam)
	var lines []line
	for _, seam := range seams {
		key := line{seam.Seam.Axis, seam.Seam.Fixed}
		if _, ok := groups[key]; !ok {
			lines = append(lines, key)
		}
		groups[key] = append(groups[key], seam)
	}
	slices.SortFunc(lines, func(a, b line) int {
		if order := cmp.Compare(a.axis, b.axis); order != 0 {
			return order
		}
		return cmp.Compare(a.fixed, b.fixed)
	})
	segments := make([]MergedSeam, 0, len(seams))
	for _, key := range lines {
		segments = mergeLine(segments, key.axis, key.fixed, groups[key])
	}
	return segments
}

type seamEvent struct {
	pos   int64
	start bool
	pair  SeamPair
}

func mergeLine(segments []MergedSeam, axis string, fixed int64, seams []SharedSeam) []MergedSeam {
	events := make([]seamEvent, 0, 2*len(seams))
	for _, seam := range seams {
		pair := SeamPair{First: seam.First, Second: seam.Second}
		events = append(events,
			seamEvent{pos: seam.Seam.From, start: true, pair: pair},
			seamEvent{pos: seam.Seam.To, pair: pair})
	}
	slices.SortFunc(events, func(a, b seamEvent) int {
		return cmp.Compare(a.pos, b.pos)
	})
	active := make(map[SeamPair]struct{})
	from := events[0].pos
	for i := 0; i < len(events); {
		pos := events[i].pos
		if from < pos && len(active) > 0 {
			segments = appendSegment(segments, axis, fixed, from, pos, active)
		}
		for ; i < len(events) && events[i].pos == pos; i++ {
			if events[i].start {
				active[events[i].pair] = struct{}{}
			} else {
				delete(active, events[i].pair)
			}
		}
		from = pos
	}
	return segments
}

// appendSegment emits one elementary interval, extending the previous
// segment instead when it is adjacent on the same line with an identical
// contributor set. Adjacent runs with different sets stay separate.
func appendSegment(segments []MergedSeam, axis string, fixed, from, to int64, active map[SeamPair]struct{}) []MergedSeam {
	pairs := make([]SeamPair, 0, len(active))
	for pair := range active {
		pairs = append(pairs, pair)
	}
	slices.SortFunc(pairs, func(a, b SeamPair) int {
		if order := cmp.Compare(a.First, b.First); order != 0 {
			return order
		}
		return cmp.Compare(a.Second, b.Second)
	})
	if n := len(segments); n > 0 {
		last := &segments[n-1]
		if last.Seam.Axis == axis && last.Seam.Fixed == fixed && last.Seam.To == from && slices.Equal(last.Pairs, pairs) {
			last.Seam.To = to
			return segments
		}
	}
	return append(segments, MergedSeam{
		Seam:  plane.Seam{Axis: axis, Fixed: fixed, From: from, To: to},
		Pairs: pairs,
	})
}
