package packed

import (
	"sort"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// Conflict returns the lexically first overlapping pair. Traversal order may
// change as the index is rebuilt, so the report cannot depend on that order.
func (t Tree) Conflict(rows []catalog.Feature) (string, string, bool) {
	ordered := append([]catalog.Feature(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, row := range ordered {
		var first string
		t.Search(row.Shape.Envelope(), func(other catalog.Feature) bool {
			if other.ID > row.ID && row.Shape.Meets(other.Shape) && (first == "" || other.ID < first) {
				first = other.ID
			}
			return true
		})
		if first != "" {
			return row.ID, first, true
		}
	}
	return "", "", false
}
