package spatial

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// SeamPartition is one connected component of the edge-sharing graph inside a
// query window. Members lists every feature in the component and Seams lists
// the window-clipped shared edges that connect those members, so the seams are
// a proof of connectivity for the partition. Singleton components carry no
// seams and preserve isolated features.
type SeamPartition struct {
	Members []string     `json:"members"`
	Seams   []SharedSeam `json:"seams"`
}

// PartitionResult holds a deterministic recomputation of the edge-sharing
// components: partitions are ordered by their smallest member ID, members are
// sorted by ID, and each proof seam list is sorted by its ID pair.
type PartitionResult struct {
	Partitions []SeamPartition `json:"partitions"`
	Visit      packed.Visit    `json:"visit"`
}

// SeamPartitions builds the connected components of the extent features that
// meet the window, where two features are adjacent only when they keep a
// positive-length shared edge after clipping to the window. Corner contacts
// never create adjacency because they yield no seam, and a connection cut away
// by the window simply disappears from the graph. Features that touch the
// window solely through its open right/top side cannot contribute an included
// seam and are not partition members.
func SeamPartitions(tree packed.Tree, window plane.Box) (PartitionResult, error) {
	// seamLimit 0 collects every seam; the same candidate budget still applies.
	outer, seams, visit, err := collectSeams(tree, window, 0)
	if err != nil {
		return PartitionResult{}, err
	}
	slices.SortFunc(outer, func(a, b catalog.Feature) int { return cmp.Compare(a.ID, b.ID) })

	index := make(map[string]int, len(outer))
	for position, feature := range outer {
		index[feature.ID] = position
	}
	dsu := newDisjointSets(len(outer))
	for _, seam := range seams {
		dsu.union(index[seam.First], index[seam.Second])
	}

	groups := make(map[int]*SeamPartition, len(outer))
	roots := make([]int, 0)
	for position, feature := range outer {
		root := dsu.find(position)
		group, ok := groups[root]
		if !ok {
			group = &SeamPartition{Members: make([]string, 0), Seams: make([]SharedSeam, 0)}
			groups[root] = group
			roots = append(roots, root)
		}
		group.Members = append(group.Members, feature.ID)
	}
	for _, seam := range seams {
		group := groups[dsu.find(index[seam.First])]
		group.Seams = append(group.Seams, seam)
	}

	partitions := make([]SeamPartition, 0, len(roots))
	for _, root := range roots {
		partitions = append(partitions, *groups[root])
	}
	// Roots appear in sorted member order, but sort explicitly so the output
	// order depends only on member IDs and not on the union-find internals.
	slices.SortFunc(partitions, func(a, b SeamPartition) int {
		return cmp.Compare(a.Members[0], b.Members[0])
	})
	return PartitionResult{Partitions: partitions, Visit: visit}, nil
}

// disjointSets is a union-find with path compression and union by size.
type disjointSets struct {
	parent []int
	size   []int
}

func newDisjointSets(n int) disjointSets {
	sets := disjointSets{parent: make([]int, n), size: make([]int, n)}
	for i := range sets.parent {
		sets.parent[i] = i
		sets.size[i] = 1
	}
	return sets
}

func (d disjointSets) find(i int) int {
	for d.parent[i] != i {
		d.parent[i] = d.parent[d.parent[i]]
		i = d.parent[i]
	}
	return i
}

func (d disjointSets) union(a, b int) {
	a, b = d.find(a), d.find(b)
	if a == b {
		return
	}
	if d.size[a] < d.size[b] {
		a, b = b, a
	}
	d.parent[b] = a
	d.size[a] += d.size[b]
}
