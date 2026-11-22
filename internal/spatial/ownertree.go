package spatial

import (
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// ownerTree spans the elementary y intervals between compressed coordinates.
// Leaf k corresponds to the interval [y[k],y[k+1]) and carries a bit mask of
// the layers currently covering it; a layer may have several overlapping
// rectangles on one interval, so cover counts per leaf and layer decide when a
// bit flips. Internal nodes only aggregate the leaf masks for pruning. Layer
// indices follow the requested top-to-bottom stacking order, so the smallest
// set bit of a mask is the highest layer covering that span.
type ownerTree struct {
	y      []int64
	levels int
	counts []uint32
	mask   []uint32
}

func newOwnerTree(y []int64, levels int) ownerTree {
	return ownerTree{
		y:      y,
		levels: levels,
		counts: make([]uint32, 4*len(y)*levels),
		mask:   make([]uint32, 4*len(y)),
	}
}

func (t ownerTree) offset(node, layer int) int { return (node-1)*t.levels + layer }

// update adjusts the coverage count of one layer over elementary intervals
// [lo,hi), flipping the leaf bit when the count crosses zero and recomputing
// the aggregate masks on the way back up.
func (t ownerTree) update(node, left, right, lo, hi, layer, delta int) {
	if right-left == 1 {
		offset := t.offset(node, layer)
		before := t.counts[offset]
		if delta > 0 {
			t.counts[offset]++
		} else if before > 0 {
			t.counts[offset]--
		}
		switch {
		case before == 0 && t.counts[offset] > 0:
			t.mask[node] |= 1 << layer
		case before > 0 && t.counts[offset] == 0:
			t.mask[node] &^= 1 << layer
		}
		return
	}
	mid := (left + right) / 2
	if lo < mid {
		t.update(2*node, left, mid, lo, hi, layer, delta)
	}
	if hi > mid {
		t.update(2*node+1, mid, right, lo, hi, layer, delta)
	}
	t.mask[node] = t.mask[2*node] | t.mask[2*node+1]
}

// runs enumerates the visible y intervals of one vertical strip [x0,x1).
// Subtrees with no covering layer are pruned; leaf intervals are emitted with
// their highest covering layer. Adjacent intervals with the same owner are
// merged into one run, so same-layer overlaps stay fused.
func (t ownerTree) runs(x0, x1 int64, work, budget int) ([]ownerRun, int, error) {
	runs := make([]ownerRun, 0)
	var walk func(node, left, right int) error
	walk = func(node, left, right int) error {
		work++
		if work > budget {
			return ErrStackBudget
		}
		if t.mask[node] == 0 {
			return nil
		}
		if right-left == 1 {
			owner := trailingZeros(t.mask[node])
			runs = appendRun(runs, owner, plane.Box{
				MinX: x0, MaxX: x1, MinY: t.y[left], MaxY: t.y[right],
			})
			return nil
		}
		mid := (left + right) / 2
		if err := walk(2*node, left, mid); err != nil {
			return err
		}
		return walk(2*node+1, mid, right)
	}
	if err := walk(1, 0, len(t.y)-1); err != nil {
		return nil, work, err
	}
	return runs, work, nil
}

func trailingZeros(mask uint32) int {
	for bit := 0; bit < 32; bit++ {
		if mask&(1<<bit) != 0 {
			return bit
		}
	}
	return 32
}

func appendRun(runs []ownerRun, owner int, box plane.Box) []ownerRun {
	if last := len(runs) - 1; last >= 0 && runs[last].owner == owner &&
		runs[last].box.MinX == box.MinX && runs[last].box.MaxX == box.MaxX &&
		runs[last].box.MaxY == box.MinY {
		runs[last].box.MaxY = box.MaxY
		return runs
	}
	return append(runs, ownerRun{owner: owner, box: box})
}
