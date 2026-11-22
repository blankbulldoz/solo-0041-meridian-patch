package spatial

import (
	"fmt"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func extent(id string, minX, minY, maxX, maxY int64) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture",
		Shape: plane.Shape{Extent: &plane.Box{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY}}}
}

func treeOf(rows ...catalog.Feature) packed.Tree { return packed.Build(rows) }

func box(minX, minY, maxX, maxY int64) plane.Box {
	return plane.Box{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY}
}

func TestStackTwoLayers(t *testing.T) {
	window := box(0, 0, 10, 10)
	top := treeOf(extent("a1", 0, 0, 6, 6))
	bottom := treeOf(extent("b1", 4, 4, 10, 10))
	result, err := Stack([]StackEntry{
		{Name: "top", Tree: top}, {Name: "bottom", Tree: bottom},
	}, window)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Layers) != 2 {
		t.Fatalf("expected 2 layer reports, got %d", len(result.Layers))
	}
	if got := result.Layers[0]; got.Layer != "top" || got.GrossArea != 36 || got.VisibleArea != 36 || got.CoveredArea != 0 {
		t.Fatalf("top exposure wrong: %+v", got)
	}
	if got := result.Layers[1]; got.Layer != "bottom" || got.GrossArea != 36 || got.VisibleArea != 32 || got.CoveredArea != 4 {
		t.Fatalf("bottom exposure wrong: %+v", got)
	}
	if sum := shardArea(t, result.Layers[1].Shards); sum != 32 {
		t.Fatalf("bottom shard area %d != 32", sum)
	}
	if err := disjoint(result.Layers[1].Shards); err != nil {
		t.Fatal(err)
	}
}

func TestStackSameLayerOverlapCountedOnce(t *testing.T) {
	window := box(0, 0, 10, 10)
	top := treeOf(extent("a1", 0, 0, 5, 5))
	bottom := treeOf(extent("b1", 0, 0, 8, 4), extent("b2", 2, 2, 10, 8))
	result, err := Stack([]StackEntry{
		{Name: "top", Tree: top}, {Name: "bottom", Tree: bottom},
	}, window)
	if err != nil {
		t.Fatal(err)
	}
	// bottom union area: 8*4 + 8*6 - 6*2 = 32+48-12 = 68
	if result.Layers[1].GrossArea != 68 {
		t.Fatalf("gross area = %d, want 68", result.Layers[1].GrossArea)
	}
	// top covers [0,5)x[0,5); within bottom union it removes:
	// [0,5)x[0,4)=20 plus [2,5)x[4,5)=3 -> 23
	if result.Layers[1].VisibleArea != 45 {
		t.Fatalf("visible area = %d, want 45", result.Layers[1].VisibleArea)
	}
	if sum := shardArea(t, result.Layers[1].Shards); sum != 45 {
		t.Fatalf("shard area %d != 45", sum)
	}
	if err := disjoint(result.Layers[1].Shards); err != nil {
		t.Fatal(err)
	}
	if err := disjoint(result.Layers[0].Shards); err != nil {
		t.Fatal(err)
	}
}

func TestStackNearlyFullyCoveredAndFragments(t *testing.T) {
	window := box(0, 0, 100, 100)
	// Upper sheet almost entirely covers the lower one; the lower layer keeps
	// only a thin strip and a corner fragment.
	top := treeOf(extent("sheet", 0, 1, 100, 100))
	bottom := treeOf(extent("slab", 0, 0, 99, 100))
	result, err := Stack([]StackEntry{
		{Name: "top", Tree: top}, {Name: "bottom", Tree: bottom},
	}, window)
	if err != nil {
		t.Fatal(err)
	}
	want := Shard{Box: box(0, 0, 99, 1), Area: 99}
	shards := result.Layers[1].Shards
	if len(shards) != 1 || shards[0].Box != want.Box || shards[0].Area != want.Area {
		t.Fatalf("expected single strip %+v, got %+v", want, shards)
	}
	if got := shards[0].Sources; len(got) != 1 || got[0] != "slab" {
		t.Fatalf("expected shard source slab, got %v", got)
	}
	if result.Layers[0].VisibleArea != 9900 || result.Layers[1].VisibleArea != 99 {
		t.Fatalf("unexpected areas: %+v", result.Layers)
	}
	// Equal-area layers must remain separately attributable.
	if result.Layers[0].Layer == result.Layers[1].Layer {
		t.Fatal("layer identities merged")
	}
}

func TestStackClassFilterAndEmpty(t *testing.T) {
	window := box(0, 0, 10, 10)
	other := extent("a2", 0, 0, 10, 10)
	other.Class = "sleeve"
	top := treeOf(extent("a1", 0, 0, 6, 6), other)
	bottom := treeOf(extent("b1", 0, 0, 10, 10))
	result, err := Stack([]StackEntry{
		{Name: "top", Class: "fixture", Tree: top}, {Name: "bottom", Tree: bottom},
	}, window)
	if err != nil {
		t.Fatal(err)
	}
	if result.Layers[0].Rectangles != 1 || result.Layers[0].VisibleArea != 36 {
		t.Fatalf("class filter failed: %+v", result.Layers[0])
	}
	if result.Layers[1].VisibleArea != 64 {
		t.Fatalf("bottom visible = %d, want 64", result.Layers[1].VisibleArea)
	}
	empty, err := Stack([]StackEntry{
		{Name: "top", Tree: top}, {Name: "bottom", Tree: bottom},
	}, box(50, 50, 60, 60))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range empty.Layers {
		if level.GrossArea != 0 || level.VisibleArea != 0 || len(level.Shards) != 0 {
			t.Fatalf("expected empty report, got %+v", level)
		}
	}
}

func TestStackOrderChangesExposure(t *testing.T) {
	window := box(0, 0, 10, 10)
	a := treeOf(extent("a1", 0, 0, 8, 8))
	b := treeOf(extent("b1", 2, 2, 10, 10))
	forward, err := Stack([]StackEntry{{Name: "a", Tree: a}, {Name: "b", Tree: b}}, window)
	if err != nil {
		t.Fatal(err)
	}
	backward, err := Stack([]StackEntry{{Name: "b", Tree: b}, {Name: "a", Tree: a}}, window)
	if err != nil {
		t.Fatal(err)
	}
	// a on top: a keeps all 64; b loses the 6x6=36 overlap, leaving 28 in
	// two disjoint fragments (12 + 16). Reversing the order swaps the roles.
	if forward.Layers[0].VisibleArea != 64 || forward.Layers[1].VisibleArea != 28 {
		t.Fatalf("forward exposure wrong: %+v", forward.Layers)
	}
	if shardArea(t, forward.Layers[1].Shards) != 28 {
		t.Fatalf("forward shards do not sum to visible area")
	}
	if err := disjoint(forward.Layers[1].Shards); err != nil {
		t.Fatal(err)
	}
	if backward.Layers[0].VisibleArea != 64 || backward.Layers[1].VisibleArea != 28 {
		t.Fatalf("backward exposure wrong: %+v", backward.Layers)
	}
	if shardArea(t, backward.Layers[1].Shards) != 28 {
		t.Fatalf("backward shards do not sum to visible area")
	}
	if forward.Layers[0].Layer != "a" || backward.Layers[0].Layer != "b" {
		t.Fatal("report order must follow requested stacking order")
	}
}

func shardArea(t *testing.T, shards []Shard) int64 {
	t.Helper()
	var total int64
	for _, shard := range shards {
		if shard.Area != shard.Box.Area() {
			t.Fatalf("stored area %d != computed %d", shard.Area, shard.Box.Area())
		}
		total += shard.Area
	}
	return total
}

func disjoint(shards []Shard) error {
	for i := range shards {
		for j := i + 1; j < len(shards); j++ {
			if _, overlaps := shards[i].Box.Clip(shards[j].Box); overlaps {
				return fmt.Errorf("shards overlap: %+v vs %+v", shards[i].Box, shards[j].Box)
			}
		}
	}
	return nil
}
