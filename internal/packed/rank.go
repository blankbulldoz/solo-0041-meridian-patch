package packed

import (
	"cmp"
	"container/heap"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// rankItem keeps internal nodes and leaf rows in one best-first frontier.
// Rows sort before nodes at equal lower bounds, so the reported row sequence
// and the visit tally stay deterministic: when a caller stops the walk at a
// row, every still-queued node has a strictly larger key.
type rankItem struct {
	lower int64
	node  *node
	row   catalog.Feature
}

type rankFrontier []rankItem

func (f rankFrontier) Len() int { return len(f) }

func (f rankFrontier) Less(i, j int) bool {
	a, b := f[i], f[j]
	if a.lower != b.lower {
		return a.lower < b.lower
	}
	if (a.node == nil) != (b.node == nil) {
		return a.node == nil
	}
	if a.node == nil {
		return a.row.ID < b.row.ID
	}
	return compareBounds(a.node.bounds, b.node.bounds) < 0
}

func (f rankFrontier) Swap(i, j int) { f[i], f[j] = f[j], f[i] }

func (f *rankFrontier) Push(value any) { *f = append(*f, value.(rankItem)) }

func (f *rankFrontier) Pop() any {
	last := len(*f) - 1
	value := (*f)[last]
	(*f)[last] = rankItem{}
	*f = (*f)[:last]
	return value
}

func compareBounds(a, b plane.Box) int {
	if order := cmp.Compare(a.MinX, b.MinX); order != 0 {
		return order
	}
	if order := cmp.Compare(a.MinY, b.MinY); order != 0 {
		return order
	}
	if order := cmp.Compare(a.MaxX, b.MaxX); order != 0 {
		return order
	}
	return cmp.Compare(a.MaxY, b.MaxY)
}

// Rank reports rows whose closure meets the window closure in ascending
// closure distance to p, along with that distance. Subtrees outside the
// window closure are never entered, and the walk stops as soon as accept
// returns false, so a caller with a distance ceiling prunes everything
// farther away. Reported distances never decrease during one walk.
func (t Tree) Rank(p plane.Point, window plane.Box, accept func(catalog.Feature, int64) bool) Visit {
	var visit Visit
	if t.root == nil || !t.root.bounds.MayMeet(window) {
		return visit
	}
	pending := rankFrontier{{lower: t.root.bounds.DistanceSquared(p), node: t.root}}
	for len(pending) != 0 {
		item := heap.Pop(&pending).(rankItem)
		if item.node == nil {
			visit.Candidates++
			if !accept(item.row, item.lower) {
				break
			}
			continue
		}
		visit.Nodes++
		n := item.node
		if n.left == nil {
			for _, row := range n.rows {
				envelope := row.Shape.Envelope()
				if envelope.MayMeet(window) {
					heap.Push(&pending, rankItem{lower: envelope.DistanceSquared(p), row: row})
				}
			}
			continue
		}
		for _, child := range []*node{n.left, n.right} {
			if child.bounds.MayMeet(window) {
				heap.Push(&pending, rankItem{lower: child.bounds.DistanceSquared(p), node: child})
			}
		}
	}
	return visit
}
