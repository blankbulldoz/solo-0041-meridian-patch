package spatial

// coverageTree is a segment tree over the elementary intervals between sorted y
// coordinates. Each node records, for exactly its span, how much length falls
// in each combination of "inside the query-window union" and "covered by 0, 1
// or at least 2 features". The two counts are independent: a window only makes
// its span part of the union, while a feature only raises the overlap depth.
// Consequently overlapping windows never make a single feature look repeated,
// and a region belonging to two windows is counted once.
type coverageTree struct {
	y     []int64
	nodes []coverageNode
}

const (
	columns = 6
	inside  = 1
)

type coverageNode struct {
	windows  int
	features int
	length   [columns]int64
	touched  bool
}

// cell maps a clamped window flag and overlap depth to a column.
func cell(window, features int) int { return window*3 + features }

func newCoverageTree(y []int64) coverageTree {
	return coverageTree{y: y, nodes: make([]coverageNode, 4*len(y))}
}

func (t coverageTree) unionLength() int64 {
	top := t.nodes[1].length
	return top[cell(inside, 0)] + top[cell(inside, 1)] + top[cell(inside, 2)]
}

func (t coverageTree) coveredLength() int64 {
	top := t.nodes[1].length
	return top[cell(inside, 1)] + top[cell(inside, 2)]
}

func (t coverageTree) repeatedLength() int64 {
	return t.nodes[1].length[cell(inside, 2)]
}

func (t coverageTree) update(index, left, right, lo, hi, delta int, window bool) {
	n := &t.nodes[index]
	if lo <= left && right <= hi {
		if window {
			n.windows += delta
		} else {
			n.features += delta
		}
	} else {
		mid := (left + right) / 2
		if lo < mid {
			t.update(2*index, left, mid, lo, hi, delta, window)
		}
		if hi > mid {
			t.update(2*index+1, mid, right, lo, hi, delta, window)
		}
	}
	t.pull(index, left, right)
}

func (t coverageTree) pull(index, left, right int) {
	n := &t.nodes[index]
	span := t.y[right] - t.y[left]
	windows := min(n.windows, 1)
	features := min(n.features, 2)
	n.length = [columns]int64{}
	if right-left == 1 {
		n.length[cell(windows, features)] = span
		n.touched = true
		return
	}
	// An untouched child was never entered by a sweep event, so it lies wholly
	// outside the union and carries no feature overlap. Its length still
	// belongs to the node and must be added to the node's own column.
	middle := t.y[(left+right)/2] - t.y[left]
	t.add(n, 2*index, windows, features, middle)
	t.add(n, 2*index+1, windows, features, span-middle)
	n.touched = true
}

func (t coverageTree) add(n *coverageNode, child, windows, features int, untouched int64) {
	if !t.nodes[child].touched {
		n.length[cell(windows, features)] += untouched
		return
	}
	for w := 0; w < 2; w++ {
		for f := 0; f < 3; f++ {
			length := t.nodes[child].length[cell(w, f)]
			if length != 0 {
				n.length[cell(min(w+windows, 1), min(f+features, 2))] += length
			}
		}
	}
}
