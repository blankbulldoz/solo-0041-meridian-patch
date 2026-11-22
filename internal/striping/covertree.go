package striping

// coverTree mirrors the database coverage sweep semantics: elementary y
// intervals carry a cover count; "once" is length covered at least once and
// "twice" is length covered at least twice, independent of higher multiplicities.
type coverTree struct {
	y     []int64
	nodes []coverNode
}

type coverNode struct {
	count int
	once  int64
	twice int64
}

func newCoverTree(y []int64) coverTree {
	return coverTree{y: y, nodes: make([]coverNode, 4*max(1, len(y)))}
}

func (t coverTree) covered() int64  { return t.nodes[1].once }
func (t coverTree) repeated() int64 { return t.nodes[1].twice }

func (t coverTree) update(index, left, right, lo, hi, delta int) {
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
