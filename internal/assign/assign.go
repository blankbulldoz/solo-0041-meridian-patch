// Package assign solves the construction marker placement problem: choose at
// most one candidate coordinate per marker (and vice versa) while respecting
// occupied coordinates and each marker's feasible Euclidean radius.
package assign

import (
	"container/heap"
	"math/bits"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// MaxMarkers and MaxCandidates bound one placement request.
const (
	MaxMarkers    = 256
	MaxCandidates = 256
)

// Marker is a construction marker with its intended integer position and the
// inclusive Euclidean Radius around it in which a candidate is feasible.
type Marker struct {
	ID     string      `json:"id"`
	Point  plane.Point `json:"point"`
	Radius int64       `json:"radius"`
}

// Candidate is an integer coordinate that may host at most one marker.
type Candidate struct {
	ID    string      `json:"id"`
	Point plane.Point `json:"point"`
}

// Placement reports one realized marker-to-candidate assignment.
type Placement struct {
	MarkerID        string      `json:"marker_id"`
	CandidateID     string      `json:"candidate_id"`
	Point           plane.Point `json:"point"`
	DistanceSquared int64       `json:"distance_squared"`
}

// Solution is the deterministically chosen optimum.
type Solution struct {
	Placements []Placement `json:"placements"`
	Unassigned []string    `json:"unassigned"`
	Assigned   int         `json:"assigned"`
}

// Solve finds a lexicographically optimum matching:
//  1. maximize the number of placed markers, then
//  2. minimize the sum of squared Euclidean displacements, then
//  3. break remaining ties by a fixed edge rank (markers and candidates are
//     identified by caller-provided indices, so identical inputs always
//     produce the same matching).
//
// It is a minimum-cost maximum flow on the bipartite graph source -> markers
// -> candidates -> sink, where feasible marker/candidate pairs are edges
// priced by their squared distance. Primary costs are 128-bit signed
// integers so no accumulation of valid squared distances can overflow.
func Solve(markers []Marker, candidates []Candidate, occupied map[plane.Point]struct{}) Solution {
	markerCount, candidateCount := len(markers), len(candidates)
	source := 0
	firstMarker := 1
	firstCandidate := firstMarker + markerCount
	sink := firstCandidate + candidateCount
	graph := flowGraph{nodeCount: sink + 1}

	for i := range markers {
		graph.addEdge(source, firstMarker+i, 1, arcCost{})
	}
	for j := range candidates {
		graph.addEdge(firstCandidate+j, sink, 1, arcCost{})
	}

	// Ranks distinguish feasible edges; a smaller (marker, candidate) index
	// pair gets a smaller rank, which gives a reproducible tie-breaking
	// preference once cardinality and displacement are already optimal.
	for i := range markers {
		limit := markers[i].Radius * markers[i].Radius
		for j := range candidates {
			if _, blocked := occupied[candidates[j].Point]; blocked {
				continue
			}
			d2 := squaredDistance(markers[i].Point, candidates[j].Point)
			if d2 > limit {
				continue
			}
			rank := int64(i)*int64(candidateCount) + int64(j)
			graph.addEdge(firstMarker+i, firstCandidate+j, 1,
				arcCost{primary: wideFromInt64(d2), secondary: rank})
		}
	}

	assigned := graph.minCostMaxFlow(source, sink)

	placements := make([]Placement, 0, assigned)
	used := make([]bool, markerCount)
	for i := range markers {
		for _, arc := range graph.adjacency[firstMarker+i] {
			// A saturated forward marker->candidate edge is a chosen pair.
			if arc.to < firstCandidate || arc.to >= sink || arc.capacity != 0 {
				continue
			}
			j := arc.to - firstCandidate
			used[i] = true
			placements = append(placements, Placement{
				MarkerID:        markers[i].ID,
				CandidateID:     candidates[j].ID,
				Point:           candidates[j].Point,
				DistanceSquared: wideToInt64(arc.cost.primary),
			})
		}
	}

	unassigned := make([]string, 0, markerCount-assigned)
	for i := range markers {
		if !used[i] {
			unassigned = append(unassigned, markers[i].ID)
		}
	}
	return Solution{Placements: placements, Unassigned: unassigned, Assigned: assigned}
}

func squaredDistance(a, b plane.Point) int64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return dx*dx + dy*dy
}

// wide128 is a signed 128-bit integer in two's complement pieces.
type wide128 struct {
	hi int64
	lo uint64
}

func wideFromInt64(n int64) wide128 {
	if n < 0 {
		return wide128{hi: -1, lo: uint64(n)}
	}
	return wide128{hi: 0, lo: uint64(n)}
}

func wideAdd(a, b wide128) wide128 {
	lo, carry := bits.Add64(a.lo, b.lo, 0)
	return wide128{hi: a.hi + b.hi + int64(carry), lo: lo}
}

func wideSub(a, b wide128) wide128 {
	lo, borrow := bits.Sub64(a.lo, b.lo, 0)
	return wide128{hi: a.hi - b.hi - int64(borrow), lo: lo}
}

func wideLess(a, b wide128) bool {
	if a.hi != b.hi {
		return a.hi < b.hi
	}
	return a.lo < b.lo
}

func wideToInt64(a wide128) int64 { return int64(a.lo) }

// arcCost is lexicographic: primary is squared displacement, secondary is a
// deterministic edge rank used only to make every tie reproducible.
type arcCost struct {
	primary   wide128
	secondary int64
}

func (c arcCost) plus(other arcCost) arcCost {
	return arcCost{primary: wideAdd(c.primary, other.primary), secondary: c.secondary + other.secondary}
}

func (c arcCost) minus(other arcCost) arcCost {
	return arcCost{primary: wideSub(c.primary, other.primary), secondary: c.secondary - other.secondary}
}

func (c arcCost) less(other arcCost) bool {
	if wideLess(c.primary, other.primary) {
		return true
	}
	if wideLess(other.primary, c.primary) {
		return false
	}
	return c.secondary < other.secondary
}

type flowEdge struct {
	from, to int
	// reverse indexes the residual counterpart in adjacency[to].
	reverse  int
	capacity int64
	cost     arcCost
}

type flowGraph struct {
	nodeCount int
	adjacency [][]flowEdge
}

func (g *flowGraph) addEdge(from, to int, capacity int64, cost arcCost) {
	for len(g.adjacency) < g.nodeCount {
		g.adjacency = append(g.adjacency, nil)
	}
	backward := arcCost{primary: wideSub(wide128{}, cost.primary), secondary: -cost.secondary}
	forward := flowEdge{from: from, to: to, reverse: len(g.adjacency[to]), capacity: capacity, cost: cost}
	reverse := flowEdge{from: to, to: from, reverse: len(g.adjacency[from]), capacity: 0, cost: backward}
	g.adjacency[from] = append(g.adjacency[from], forward)
	g.adjacency[to] = append(g.adjacency[to], reverse)
}

// minCostMaxFlow augments one unit along the cheapest residual path
// (successive shortest paths with non-negative reduced costs maintained by
// Johnson potentials) until no augmenting path remains. Unit capacities make
// every augmentation one unit, so the final flow is maximum cardinality and
// the accumulated lexicographic cost is minimum among maximum flows.
func (g *flowGraph) minCostMaxFlow(source, sink int) int {
	potential := make([]arcCost, g.nodeCount)
	distance := make([]arcCost, g.nodeCount)
	reached := make([]bool, g.nodeCount)
	fromNode := make([]int, g.nodeCount)
	fromEdge := make([]int, g.nodeCount)
	flow := 0

	for {
		for i := range reached {
			reached[i] = false
		}
		queue := flowHeap{}
		heap.Push(&queue, flowEntry{node: source, distance: arcCost{}})
		reached[source] = true
		distance[source] = arcCost{}

		for queue.Len() > 0 {
			current := heap.Pop(&queue).(flowEntry)
			// Labels only improve, so a heap entry different from the current
			// label is stale and gets skipped.
			if distance[current.node].less(current.distance) {
				continue
			}
			for edgeIndex, arc := range g.adjacency[current.node] {
				if arc.capacity == 0 {
					continue
				}
				reduced := arc.cost.plus(potential[current.node]).minus(potential[arc.to])
				candidate := distance[current.node].plus(reduced)
				if !reached[arc.to] || candidate.less(distance[arc.to]) {
					reached[arc.to] = true
					distance[arc.to] = candidate
					fromNode[arc.to] = current.node
					fromEdge[arc.to] = edgeIndex
					heap.Push(&queue, flowEntry{node: arc.to, distance: candidate})
				}
			}
		}

		if !reached[sink] {
			return flow
		}
		for node := 0; node < g.nodeCount; node++ {
			if reached[node] {
				potential[node] = potential[node].plus(distance[node])
			}
		}

		for node := sink; node != source; node = fromNode[node] {
			arc := &g.adjacency[fromNode[node]][fromEdge[node]]
			arc.capacity--
			g.adjacency[node][arc.reverse].capacity++
		}
		flow++
	}
}

type flowEntry struct {
	node     int
	distance arcCost
}

type flowHeap []flowEntry

func (h flowHeap) Len() int { return len(h) }

func (h flowHeap) Less(i, j int) bool {
	if h[i].distance.less(h[j].distance) {
		return true
	}
	if h[j].distance.less(h[i].distance) {
		return false
	}
	return h[i].node < h[j].node
}

func (h flowHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *flowHeap) Push(value any) { *h = append(*h, value.(flowEntry)) }

func (h *flowHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}
