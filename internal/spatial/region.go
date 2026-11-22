package spatial

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// RegionSkips counts index candidates rejected by the exact region rule.
type RegionSkips struct {
	Marks   int `json:"marks"`
	Extents int `json:"extents"`
}

// RegionSelection is the ordered set of translated source features plus the
// candidates the exact rule rejected.
type RegionSelection struct {
	Features []catalog.Feature `json:"features"`
	Excluded RegionSkips       `json:"excluded"`
	Visit    packed.Visit      `json:"visit"`
}

// Select applies the transplant region rule to one source layer. A mark is
// selected only when the half-open window owns it. An extent is clipped to the
// window first and dropped when the clip has no positive area; the selected
// shape is the translated clip, never the untranslated source extent.
func Select(tree packed.Tree, window plane.Box, offset plane.Offset) RegionSelection {
	selection := RegionSelection{Features: make([]catalog.Feature, 0)}
	selection.Visit = tree.Search(window, func(row catalog.Feature) bool {
		var shape plane.Shape
		if row.Shape.Mark != nil {
			if !window.Owns(*row.Shape.Mark) {
				selection.Excluded.Marks++
				return true
			}
			shape = offset.Apply(row.Shape)
		} else {
			clipped, visible := window.Clip(*row.Shape.Extent)
			if !visible {
				selection.Excluded.Extents++
				return true
			}
			shape = offset.Apply(plane.Shape{Extent: &clipped})
		}
		moved := row.Copy()
		moved.Shape = shape
		selection.Features = append(selection.Features, moved)
		return true
	})
	slices.SortFunc(selection.Features, func(a, b catalog.Feature) int { return cmp.Compare(a.ID, b.ID) })
	return selection
}
