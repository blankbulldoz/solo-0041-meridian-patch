package packed

import (
	"cmp"
	"container/heap"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

type Neighbor struct {
	Feature         catalog.Feature `json:"feature"`
	DistanceSquared int64           `json:"distance_squared"`
}

type nodeDistance struct {
	node       *node
	lowerBound int64
}

type frontier []nodeDistance

func (f frontier) Len() int           { return len(f) }
func (f frontier) Less(i, j int) bool { return f[i].lowerBound < f[j].lowerBound }
func (f frontier) Swap(i, j int)      { f[i], f[j] = f[j], f[i] }
func (f *frontier) Push(value any)    { *f = append(*f, value.(nodeDistance)) }
func (f *frontier) Pop() any {
	last := len(*f) - 1
	value := (*f)[last]
	(*f)[last] = nodeDistance{}
	*f = (*f)[:last]
	return value
}

func (t Tree) Nearest(p plane.Point, size int, class string) ([]Neighbor, Visit) {
	found := make([]Neighbor, 0, size)
	var visit Visit
	if t.root == nil {
		return found, visit
	}
	pending := frontier{{node: t.root, lowerBound: t.root.bounds.DistanceSquared(p)}}
	for len(pending) != 0 {
		current := heap.Pop(&pending).(nodeDistance)
		if len(found) == size && current.lowerBound > found[size-1].DistanceSquared {
			break
		}
		visit.Nodes++
		n := current.node
		if n.left != nil {
			heap.Push(&pending, nodeDistance{n.left, n.left.bounds.DistanceSquared(p)})
			heap.Push(&pending, nodeDistance{n.right, n.right.bounds.DistanceSquared(p)})
			continue
		}
		for _, row := range n.rows {
			visit.Candidates++
			if class != "" && row.Class != class {
				continue
			}
			next := Neighbor{Feature: row, DistanceSquared: row.Shape.Envelope().DistanceSquared(p)}
			pos, _ := slices.BinarySearchFunc(found, next, compareNeighbor)
			if pos < size {
				if len(found) == size {
					found = found[:size-1]
				}
				found = slices.Insert(found, pos, next)
			}
		}
	}
	return found, visit
}

func compareNeighbor(a, b Neighbor) int {
	if result := cmp.Compare(a.DistanceSquared, b.DistanceSquared); result != 0 {
		return result
	}
	return cmp.Compare(a.Feature.ID, b.Feature.ID)
}
