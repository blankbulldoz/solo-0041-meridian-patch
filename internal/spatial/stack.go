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
	// MaxStackShards bounds the verifiable shards returned for any single layer.
	// maxStackWork bounds the per-query sweep-line traversal.
	MaxStackShards = 2000
	maxStackWork   = 200_000
)

// ErrStackBudget is reported when a stack query produces more shards or more
// elementary traversal work than the fixed budgets allow.
var ErrStackBudget = errors.New("stack query exceeds shard or traversal budget")

// Shard is a verifiable visible fragment. Shards of one layer are pairwise
// interior-disjoint axis-aligned rectangles inside the query window, and their
// total area equals the layer's visible area. Sources lists the clipped
// features whose union covers the shard, so a caller can re-derive the fragment.
type Shard struct {
	plane.Box
	Area    int64    `json:"area"`
	Sources []string `json:"sources"`
}

// LayerExposure describes one requested layer. Layers are reported in the
// requested top-to-bottom order so equal areas from different layers never
// collapse into a single aggregate.
type LayerExposure struct {
	Layer       string  `json:"layer"`
	GrossArea   int64   `json:"gross_area"`
	VisibleArea int64   `json:"visible_area"`
	CoveredArea int64   `json:"covered_area"`
	Rectangles  int     `json:"rectangles"`
	Shards      []Shard `json:"shards"`
}

// StackResult is the full exposure report together with index traversal costs.
type StackResult struct {
	Layers []LayerExposure `json:"layers"`
	Visit  packed.Visit    `json:"visit"`
}

// StackEntry pairs a layer identity with its immutable index for a stack query.
type StackEntry struct {
	Name  string
	Class string
	Tree  packed.Tree
}

type stackLayer struct {
	name  string
	index int
	rows  []stackRect
	gross int64
	visit packed.Visit
}

type stackRect struct {
	box     plane.Box
	feature string
}

type stackEvent struct {
	x     int64
	lo    int
	hi    int
	delta int
	layer int
}

// Stack computes the visible exposure of the given extent layers, treating the
// slice order as top-to-bottom stacking. Upper layers occlude lower ones and
// overlapping rectangles inside one layer contribute area only once.
func Stack(entries []StackEntry, window plane.Box) (StackResult, error) {
	if len(entries) > 32 {
		// Leaf masks are uint32 bit sets indexed by stacking position.
		return StackResult{}, errors.New("stack supports at most 32 ordered layers")
	}
	levels := make([]*stackLayer, len(entries))
	ycoords := make([]int64, 0)
	result := StackResult{Layers: make([]LayerExposure, len(entries))}
	for i, entry := range entries {
		level := &stackLayer{name: entry.Name, index: i}
		levels[i] = level
		level.visit = entry.Tree.Search(window, func(row catalog.Feature) bool {
			if entry.Class != "" && row.Class != entry.Class {
				return true
			}
			clipped, present := window.Clip(*row.Shape.Extent)
			if !present {
				return true
			}
			level.rows = append(level.rows, stackRect{box: clipped, feature: row.ID})
			ycoords = append(ycoords, clipped.MinY, clipped.MaxY)
			return true
		})
		result.Visit.Nodes += level.visit.Nodes
		result.Visit.Candidates += level.visit.Candidates
		level.gross = unionArea(level.rows)
		result.Layers[i] = LayerExposure{
			Layer: level.name, GrossArea: level.gross,
			Rectangles: len(level.rows), Shards: make([]Shard, 0),
		}
	}
	if len(ycoords) == 0 {
		return result, nil
	}
	slices.Sort(ycoords)
	ycoords = slices.Compact(ycoords)
	var rectCount int
	for _, level := range levels {
		rectCount += len(level.rows)
	}
	events := make([]stackEvent, 0, rectCount*2)
	for _, level := range levels {
		for _, row := range level.rows {
			lo, _ := slices.BinarySearch(ycoords, row.box.MinY)
			hi, _ := slices.BinarySearch(ycoords, row.box.MaxY)
			events = append(events,
				stackEvent{x: row.box.MinX, lo: lo, hi: hi, delta: 1, layer: level.index},
				stackEvent{x: row.box.MaxX, lo: lo, hi: hi, delta: -1, layer: level.index},
			)
		}
	}
	slices.SortFunc(events, func(a, b stackEvent) int {
		if order := cmp.Compare(a.x, b.x); order != 0 {
			return order
		}
		if order := cmp.Compare(a.layer, b.layer); order != 0 {
			return order
		}
		if order := cmp.Compare(a.lo, b.lo); order != 0 {
			return order
		}
		return cmp.Compare(a.hi, b.hi)
	})
	segments := newOwnerTree(ycoords, len(levels))
	// openShard indices stay valid across slice growth: append never moves an
	// existing element, so the recorded index keeps pointing at the same shard.
	open := make(map[openKey]int)
	work := 0
	previous := events[0].x
	for _, event := range events {
		if event.x > previous {
			runs, spent, err := segments.runs(previous, event.x, work, maxStackWork)
			if err != nil {
				return StackResult{}, err
			}
			work = spent
			for _, run := range runs {
				target := &result.Layers[run.owner]
				key := openKey{owner: run.owner, minY: run.box.MinY}
				merged := false
				if index, ok := open[key]; ok {
					shard := &target.Shards[index]
					if shard.MaxX == run.box.MinX && shard.MaxY == run.box.MaxY {
						shard.MaxX = run.box.MaxX
						shard.Area = shard.Box.Area()
						merged = true
					} else {
						delete(open, key)
					}
				}
				if !merged {
					if len(target.Shards) == MaxStackShards {
						return StackResult{}, ErrStackBudget
					}
					target.Shards = append(target.Shards, Shard{Box: run.box, Area: run.box.Area()})
					open[key] = len(target.Shards) - 1
				}
				target.VisibleArea += run.box.Area()
			}
		}
		segments.update(1, 0, len(ycoords)-1, event.lo, event.hi, event.layer, event.delta)
		previous = event.x
	}
	for i := range result.Layers {
		level := result.Layers[i]
		level.CoveredArea = level.GrossArea - level.VisibleArea
		level.Shards = attachSources(level.Shards, levels[i].rows)
		result.Layers[i] = level
	}
	return result, nil
}

type openKey struct {
	owner int
	minY  int64
}

type ownerRun struct {
	owner int
	box   plane.Box
}

func attachSources(shards []Shard, rows []stackRect) []Shard {
	for i := range shards {
		ids := make([]string, 0)
		for _, row := range rows {
			if _, overlaps := row.box.Clip(shards[i].Box); overlaps {
				ids = append(ids, row.feature)
			}
		}
		slices.Sort(ids)
		shards[i].Sources = ids
	}
	return shards
}

// unionArea measures the at-least-once coverage of one layer's clipped
// rectangles with the same sweep-line machinery as Coverage, so same-layer
// overlaps are never counted twice.
func unionArea(rows []stackRect) int64 {
	if len(rows) == 0 {
		return 0
	}
	ycoords := make([]int64, 0, len(rows)*2)
	events := make([]boundary, 0, len(rows)*2)
	for _, row := range rows {
		ycoords = append(ycoords, row.box.MinY, row.box.MaxY)
		events = append(events,
			boundary{x: row.box.MinX, y0: row.box.MinY, y1: row.box.MaxY, delta: 1},
			boundary{x: row.box.MaxX, y0: row.box.MinY, y1: row.box.MaxY, delta: -1},
		)
	}
	slices.Sort(ycoords)
	ycoords = slices.Compact(ycoords)
	slices.SortFunc(events, func(a, b boundary) int { return cmp.Compare(a.x, b.x) })
	tree := newLengthTree(ycoords)
	var area int64
	previous := events[0].x
	for _, event := range events {
		area += (event.x - previous) * tree.covered()
		lo, _ := slices.BinarySearch(ycoords, event.y0)
		hi, _ := slices.BinarySearch(ycoords, event.y1)
		tree.update(1, 0, len(ycoords)-1, lo, hi, event.delta)
		previous = event.x
	}
	return area
}
