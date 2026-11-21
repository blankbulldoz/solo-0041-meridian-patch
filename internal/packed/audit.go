package packed

import (
	"fmt"
	"reflect"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

type Audit struct {
	Records      int `json:"records"`
	CheckedNodes int `json:"checked_nodes"`
}

func (t Tree) Audit(rows map[string]catalog.Feature) (Audit, error) {
	seen := make(map[string]bool, len(rows))
	var count int
	if t.root != nil {
		if _, err := auditNode(t.root, rows, seen, &count); err != nil {
			return Audit{}, err
		}
	}
	if len(seen) != len(rows) {
		return Audit{}, fmt.Errorf("tree has %d unique records, catalog has %d", len(seen), len(rows))
	}
	return Audit{Records: len(seen), CheckedNodes: count}, nil
}

func auditNode(n *node, rows map[string]catalog.Feature, seen map[string]bool, count *int) (plane.Box, error) {
	*count += 1
	var total plane.Box
	if n.left == nil && n.right == nil {
		if len(n.rows) == 0 || len(n.rows) > leafSize {
			return plane.Box{}, fmt.Errorf("invalid leaf occupancy: %d", len(n.rows))
		}
		total = n.rows[0].Shape.Envelope()
		for _, feature := range n.rows {
			canonical, present := rows[feature.ID]
			if !present || seen[feature.ID] || !reflect.DeepEqual(canonical, feature) {
				return plane.Box{}, fmt.Errorf("index entry %q inconsistent with catalog", feature.ID)
			}
			seen[feature.ID] = true
			total = total.Union(feature.Shape.Envelope())
		}
	} else {
		if n.left == nil || n.right == nil || len(n.rows) != 0 {
			return plane.Box{}, fmt.Errorf("invalid internal node shape")
		}
		left, err := auditNode(n.left, rows, seen, count)
		if err != nil {
			return plane.Box{}, err
		}
		right, err := auditNode(n.right, rows, seen, count)
		if err != nil {
			return plane.Box{}, err
		}
		total = left.Union(right)
	}
	if n.bounds != total {
		return plane.Box{}, fmt.Errorf("index envelope does not match contents")
	}
	return total, nil
}
