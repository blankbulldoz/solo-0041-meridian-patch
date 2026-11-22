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

// NearestPolicy selects the features eligible for a nearest-neighbor result
// and caps how many neighbors each class may contribute.
type NearestPolicy struct {
	// Class restricts the search to a single class when set.
	Class string
	// Quotas caps the neighbors returned for each listed class. A zero
	// quota excludes the class. An empty map disables per-class caps.
	Quotas map[string]int
	// IncludeUnknown admits classes missing from Quotas, bounded only by
	// the overall limit. It has no effect when Quotas is empty.
	IncludeUnknown bool
}

// bucket keeps the nearest accepted neighbors for one class, for every
// class when quotas are disabled, or for every unlisted class.
type bucket struct {
	limit int
	rows  []Neighbor
}

func (b *bucket) offer(next Neighbor) {
	pos, _ := slices.BinarySearchFunc(b.rows, next, compareNeighbor)
	if pos < b.limit {
		if len(b.rows) == b.limit {
			b.rows = b.rows[:b.limit-1]
		}
		b.rows = slices.Insert(b.rows, pos, next)
	}
}

// sealed reports whether no feature at distance lowerBound or beyond could
// still enter the bucket, including by winning an ID tiebreak.
func (b *bucket) sealed(lowerBound int64) bool {
	return len(b.rows) == b.limit && lowerBound > b.rows[b.limit-1].DistanceSquared
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

func (t Tree) Nearest(p plane.Point, size int, policy NearestPolicy) ([]Neighbor, Visit) {
	found := make([]Neighbor, 0, size)
	var visit Visit
	if t.root == nil || size <= 0 {
		return found, visit
	}
	byClass := make(map[string]*bucket)
	var shared *bucket
	var buckets []*bucket
	if len(policy.Quotas) == 0 {
		shared = &bucket{limit: size}
		buckets = append(buckets, shared)
	} else {
		for class, quota := range policy.Quotas {
			if quota > 0 {
				b := &bucket{limit: min(quota, size)}
				byClass[class] = b
				buckets = append(buckets, b)
			}
		}
		if policy.IncludeUnknown {
			shared = &bucket{limit: size}
			buckets = append(buckets, shared)
		}
	}
	pending := frontier{{node: t.root, lowerBound: t.root.bounds.DistanceSquared(p)}}
	for len(pending) != 0 {
		current := heap.Pop(&pending).(nodeDistance)
		sealed := true
		for _, b := range buckets {
			if !b.sealed(current.lowerBound) {
				sealed = false
				break
			}
		}
		if sealed {
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
			if policy.Class != "" && row.Class != policy.Class {
				continue
			}
			b := shared
			if len(policy.Quotas) != 0 {
				if _, listed := policy.Quotas[row.Class]; listed {
					b = byClass[row.Class] // nil when the quota is zero
				}
			}
			if b == nil {
				continue
			}
			b.offer(Neighbor{Feature: row, DistanceSquared: row.Shape.Envelope().DistanceSquared(p)})
		}
	}
	for _, b := range buckets {
		found = append(found, b.rows...)
	}
	slices.SortFunc(found, compareNeighbor)
	if len(found) > size {
		found = found[:size]
	}
	return found, visit
}

func compareNeighbor(a, b Neighbor) int {
	if result := cmp.Compare(a.DistanceSquared, b.DistanceSquared); result != 0 {
		return result
	}
	return cmp.Compare(a.Feature.ID, b.Feature.ID)
}
