package packed

import (
	"fmt"
	"slices"
	"sort"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// Grid is a hotspot index over custom cut lines. The cuts split the layer
// bounds into a grid of cells; every feature is stored in exactly one
// subtree: the single cell that fully contains its envelope, or the spill
// tree when the envelope crosses a cut line. Because ownership is unique,
// merging results across cells never yields duplicates, and because the
// cells plus the spill tree cover the whole layer, a window query answered
// from the grid never misses a match.
type Grid struct {
	bounds plane.Box
	cutsX  []int64
	cutsY  []int64
	cells  []Tree // (len(cutsX)+1)*(len(cutsY)+1) trees, x-major order
	spill  Tree
}

// BuildGrid assigns each row to its owning cell or to the spill tree. The
// cuts must already be validated against bounds by the catalog layer.
func BuildGrid(rows []catalog.Feature, bounds plane.Box, cutsX, cutsY []int64) Grid {
	g := Grid{
		bounds: bounds,
		cutsX:  slices.Clone(cutsX),
		cutsY:  slices.Clone(cutsY),
		cells:  make([]Tree, (len(cutsX)+1)*(len(cutsY)+1)),
	}
	buckets := make([][]catalog.Feature, len(g.cells))
	var spill []catalog.Feature
	for _, row := range rows {
		if slot, ok := g.slot(row.Shape.Envelope()); ok {
			buckets[slot] = append(buckets[slot], row)
		} else {
			spill = append(spill, row)
		}
	}
	for slot, bucket := range buckets {
		g.cells[slot] = Build(bucket)
	}
	g.spill = Build(spill)
	return g
}

// intervalOf returns the index of the cut-line interval [lo,hi) holding v.
func intervalOf(cuts []int64, v int64) int {
	return sort.Search(len(cuts), func(i int) bool { return v < cuts[i] })
}

func (g *Grid) xlo(i int) int64 {
	if i == 0 {
		return g.bounds.MinX
	}
	return g.cutsX[i-1]
}

func (g *Grid) xhi(i int) int64 {
	if i == len(g.cutsX) {
		return g.bounds.MaxX
	}
	return g.cutsX[i]
}

func (g *Grid) ylo(j int) int64 {
	if j == 0 {
		return g.bounds.MinY
	}
	return g.cutsY[j-1]
}

func (g *Grid) yhi(j int) int64 {
	if j == len(g.cutsY) {
		return g.bounds.MaxY
	}
	return g.cutsY[j]
}

// slot reports which cell owns the envelope: the x interval that contains
// env.MinX must also reach env.MaxX, and likewise for y. A point on a cut
// line belongs to the interval on its upper-right side, so ownership is
// always unique. Envelopes crossing a cut line have no owning cell.
func (g *Grid) slot(env plane.Box) (int, bool) {
	i := intervalOf(g.cutsX, env.MinX)
	if env.MaxX > g.xhi(i) {
		return 0, false
	}
	j := intervalOf(g.cutsY, env.MinY)
	if env.MaxY > g.yhi(j) {
		return 0, false
	}
	return i*(len(g.cutsY)+1) + j, true
}

func (g *Grid) cellBox(i, j int) plane.Box {
	return plane.Box{MinX: g.xlo(i), MinY: g.ylo(j), MaxX: g.xhi(i), MaxY: g.yhi(j)}
}

// Search visits every cell whose closed region meets the window, then the
// spill tree, and reports the merged visit cost plus the number of cells
// touched. Cell selection uses the inclusive MayMeet rule, matching the
// pruning semantics of the packed trees, so no candidate can be lost.
func (g *Grid) Search(window plane.Box, accept func(catalog.Feature) bool) (Visit, int) {
	var total Visit
	touched := 0
	stopped := false
	wrap := func(row catalog.Feature) bool {
		if stopped {
			return false
		}
		if !accept(row) {
			stopped = true
			return false
		}
		return true
	}
	rows := len(g.cutsY) + 1
	for i := 0; i <= len(g.cutsX) && !stopped; i++ {
		for j := 0; j <= len(g.cutsY) && !stopped; j++ {
			if !g.cellBox(i, j).MayMeet(window) {
				continue
			}
			touched++
			visit := g.cells[i*rows+j].Search(window, wrap)
			total.Nodes += visit.Nodes
			total.Candidates += visit.Candidates
		}
	}
	if !stopped {
		visit := g.spill.Search(window, wrap)
		total.Nodes += visit.Nodes
		total.Candidates += visit.Candidates
	}
	return total, touched
}

// GridStats describes the hotspot grid for index inspection.
type GridStats struct {
	CutsX      []int64 `json:"cuts_x,omitempty"`
	CutsY      []int64 `json:"cuts_y,omitempty"`
	Cells      int     `json:"cells"`
	Occupied   int     `json:"occupied"`
	Items      int     `json:"items"`
	SpillItems int     `json:"spill_items"`
	Depth      int     `json:"depth"`
}

func (g *Grid) Stats() GridStats {
	stats := GridStats{
		CutsX: slices.Clone(g.cutsX),
		CutsY: slices.Clone(g.cutsY),
		Cells: len(g.cells),
	}
	for _, cell := range g.cells {
		sub := cell.Stats()
		if sub.Items > 0 {
			stats.Occupied++
		}
		stats.Items += sub.Items
		stats.Depth = max(stats.Depth, sub.Depth)
	}
	spill := g.spill.Stats()
	stats.SpillItems = spill.Items
	stats.Items += spill.Items
	stats.Depth = max(stats.Depth, spill.Depth)
	return stats
}

// GridAudit reports the hotspot grid check alongside the tree audit.
type GridAudit struct {
	Audit
	Cells      int `json:"cells"`
	SpillItems int `json:"spill_items"`
}

// Audit verifies every subtree against the catalog, then checks that each
// feature sits in exactly the subtree its envelope prescribes: the owning
// cell, or the spill tree for envelopes crossing a cut line.
func (g *Grid) Audit(rows map[string]catalog.Feature) (GridAudit, error) {
	seen := make(map[string]int, len(rows))
	var checked int
	auditTree := func(tree Tree, slot int) error {
		if tree.root == nil {
			return nil
		}
		local := make(map[string]bool)
		if _, err := auditNode(tree.root, rows, local, &checked); err != nil {
			return err
		}
		for id := range local {
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("feature %q appears in more than one grid subtree", id)
			}
			seen[id] = slot
		}
		return nil
	}
	for slot, cell := range g.cells {
		if err := auditTree(cell, slot); err != nil {
			return GridAudit{}, err
		}
	}
	if err := auditTree(g.spill, -1); err != nil {
		return GridAudit{}, err
	}
	if len(seen) != len(rows) {
		return GridAudit{}, fmt.Errorf("grid has %d unique records, catalog has %d", len(seen), len(rows))
	}
	for id, row := range rows {
		want := -1
		if slot, ok := g.slot(row.Shape.Envelope()); ok {
			want = slot
		}
		if seen[id] != want {
			return GridAudit{}, fmt.Errorf("feature %q is not in its prescribed grid subtree", id)
		}
	}
	return GridAudit{
		Audit:      Audit{Records: len(seen), CheckedNodes: checked},
		Cells:      len(g.cells),
		SpillItems: g.spill.Stats().Items,
	}, nil
}
