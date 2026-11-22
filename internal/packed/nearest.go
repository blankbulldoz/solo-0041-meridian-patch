package packed

import (
	"cmp"
	"container/heap"
	"fmt"
	"slices"
	"sort"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// MaxTieGroup is the independent hard cap on a retained boundary tie group.
// It is deliberately separate from the ordinary nearest limit: a dense layer
// may hold thousands of coincident features at the boundary distance, and
// retaining all of them must not become an unbounded response. A group larger
// than this is rejected whole rather than truncated by ID.
const MaxTieGroup = 1024

// TieGroupTooLargeError reports that the boundary tie group exceeds
// MaxTieGroup. The query returns no partial neighbor list, so a caller never
// observes a tie group cut by the hard cap.
type TieGroupTooLargeError struct {
	DistanceSquared int64
	GroupSize       int
	Limit           int
	Cap             int
}

func (e *TieGroupTooLargeError) Error() string {
	return fmt.Sprintf("nearest boundary tie group at distance_squared %d has %d features, exceeding tie group cap %d (limit %d)",
		e.DistanceSquared, e.GroupSize, e.Cap, e.Limit)
}

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

// TieRetention describes how a boundary tie group expanded the result.
type TieRetention struct {
	BoundaryDistanceSquared int64 `json:"boundary_distance_squared"`
	GroupSize               int   `json:"group_size"`
}

// NearestWithTies is the boundary-tie retention variant of Nearest. It keeps
// the ordinary K-row ranking but never splits the group whose distance equals
// the K-th distance: every class-matching feature at that distance is returned
// even when that makes the result longer than size. Ordering stays stable and
// deterministic by distance and then ID, so the tie group is contiguous and
// sorted by ID within its distance.
//
// The traversal must not prune a node whose bounding-box lower bound merely
// equals the current boundary: such a node can still hold tied rows, and a
// smaller ID there could change the group. The stop test is strictly greater.
//
// A tie group larger than MaxTieGroup is rejected as a whole through
// TieGroupTooLargeError, never truncated by ID.
func (t Tree) NearestWithTies(p plane.Point, size int, class string) ([]Neighbor, *TieRetention, Visit, error) {
	found := make([]Neighbor, 0, size)
	var visit Visit
	if t.root == nil {
		return found, nil, visit, nil
	}
	var boundary int64
	hasBoundary := false
	pending := frontier{{node: t.root, lowerBound: t.root.bounds.DistanceSquared(p)}}
	for len(pending) != 0 {
		current := heap.Pop(&pending).(nodeDistance)
		if hasBoundary && current.lowerBound > boundary {
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
			if hasBoundary && next.DistanceSquared > boundary {
				continue
			}
			pos, _ := slices.BinarySearchFunc(found, next, compareNeighbor)
			found = slices.Insert(found, pos, next)
			if len(found) >= size {
				// The K-th distance can shrink as closer rows arrive, so any
				// retained row now beyond it cannot be part of the final set.
				boundary = found[size-1].DistanceSquared
				hasBoundary = true
				keep := sort.Search(len(found), func(i int) bool { return found[i].DistanceSquared > boundary })
				found = found[:keep]
			}
		}
	}
	if !hasBoundary {
		return found, nil, visit, nil
	}
	firstTied := sort.Search(len(found), func(i int) bool { return found[i].DistanceSquared >= boundary })
	group := len(found) - firstTied
	if group > MaxTieGroup {
		return nil, nil, visit, &TieGroupTooLargeError{
			DistanceSquared: boundary,
			GroupSize:       group,
			Limit:           size,
			Cap:             MaxTieGroup,
		}
	}
	return found, &TieRetention{BoundaryDistanceSquared: boundary, GroupSize: group}, visit, nil
}

func compareNeighbor(a, b Neighbor) int {
	if result := cmp.Compare(a.DistanceSquared, b.DistanceSquared); result != 0 {
		return result
	}
	return cmp.Compare(a.Feature.ID, b.Feature.ID)
}
