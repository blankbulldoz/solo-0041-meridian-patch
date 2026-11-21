package packed

import "github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"

type Stats struct {
	Depth      int        `json:"depth"`
	Nodes      int        `json:"nodes"`
	Leaves     int        `json:"leaves"`
	Items      int        `json:"items"`
	MaxLeaf    int        `json:"max_leaf"`
	RootBounds *plane.Box `json:"root_bounds,omitempty"`
}

func (t Tree) Stats() Stats {
	var stats Stats
	if t.root != nil {
		bounds := t.root.bounds
		stats.RootBounds = &bounds
		accumulate(t.root, 1, &stats)
	}
	return stats
}

func accumulate(n *node, depth int, stats *Stats) {
	stats.Nodes++
	stats.Depth = max(depth, stats.Depth)
	if n.left == nil {
		stats.Leaves++
		stats.Items += len(n.rows)
		stats.MaxLeaf = max(stats.MaxLeaf, len(n.rows))
		return
	}
	accumulate(n.left, depth+1, stats)
	accumulate(n.right, depth+1, stats)
}
