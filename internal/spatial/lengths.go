package spatial

// lengthTree represents the elementary intervals between sorted y coordinates.
// A node's cover count applies to its entire span, independently of the child
// counts. Two aggregate lengths distinguish single and repeated coverage.
type lengthTree struct {
	y     []int64
	nodes []lengthNode
}

type lengthNode struct {
	count int
	once  int64
	twice int64
}

func newLengthTree(y []int64) lengthTree {
	return lengthTree{y: y, nodes: make([]lengthNode, 4*len(y))}
}

func (t lengthTree) covered() int64  { return t.nodes[1].once }
func (t lengthTree) repeated() int64 { return t.nodes[1].twice }

func (t lengthTree) update(index, left, right, lo, hi, delta int) {
	n := &t.nodes[index]
	if lo <= left && right <= hi {
		n.count += delta
	} else {
		mid := (left + right) / 2
		if lo < mid {
			t.update(2*index, left, mid, lo, hi, delta)
		}
		if hi > mid {
			t.update(2*index+1, mid, right, lo, hi, delta)
		}
	}
	var childOnce, childTwice int64
	if right-left > 1 {
		a, b := t.nodes[2*index], t.nodes[2*index+1]
		childOnce = a.once + b.once
		childTwice = a.twice + b.twice
	}
	span := t.y[right] - t.y[left]
	switch {
	case n.count >= 2:
		n.once, n.twice = span, span
	case n.count == 1:
		n.once, n.twice = span, childOnce
	default:
		n.once, n.twice = childOnce, childTwice
	}
}
