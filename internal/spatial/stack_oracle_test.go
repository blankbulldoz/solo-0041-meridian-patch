package spatial

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// TestStackGridOracle compares the sweep-line result against a brute-force
// unit-grid oracle on many random small stacks, including fully covering
// layers, fragments and same-layer overlaps.
func TestStackGridOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20261122))
	const size = 12
	window := box(0, 0, size, size)
	for trial := 0; trial < 200; trial++ {
		layerCount := 1 + rng.Intn(4)
		entries := make([]StackEntry, layerCount)
		oracle := make([][]int, size) // highest layer owning each unit cell, -1 when empty
		gridArea := make([]int64, layerCount)
		for x := range oracle {
			oracle[x] = make([]int, size)
			for y := range oracle[x] {
				oracle[x][y] = -1
			}
		}
		for level := 0; level < layerCount; level++ {
			rows := make([]catalog.Feature, 0)
			featureCount := 1 + rng.Intn(5)
			for f := 0; f < featureCount; f++ {
				x0 := rng.Intn(size - 1)
				y0 := rng.Intn(size - 1)
				x1 := x0 + 1 + rng.Intn(size-1-x0)
				y1 := y0 + 1 + rng.Intn(size-1-y0)
				rows = append(rows, extent(fmt.Sprintf("r%d-%d", level, f), int64(x0), int64(y0), int64(x1), int64(y1)))
				for x := x0; x < x1; x++ {
					for y := y0; y < y1; y++ {
						if owner := oracle[x][y]; owner < 0 || level < owner {
							if owner >= 0 {
								gridArea[owner]--
							}
							oracle[x][y] = level
							gridArea[level]++
						}
					}
				}
			}
			entries[level] = StackEntry{Name: fmt.Sprintf("layer-%d", level), Tree: treeOf(rows...)}
		}
		result, err := Stack(entries, window)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		for level := 0; level < layerCount; level++ {
			report := result.Layers[level]
			if report.VisibleArea != gridArea[level] {
				t.Fatalf("trial %d layer %d: visible %d != oracle %d", trial, level, report.VisibleArea, gridArea[level])
			}
			if got := shardArea(t, report.Shards); got != report.VisibleArea {
				t.Fatalf("trial %d layer %d: shard area %d != visible %d", trial, level, got, report.VisibleArea)
			}
			if err := disjoint(report.Shards); err != nil {
				t.Fatalf("trial %d layer %d: %v", trial, level, err)
			}
			if report.CoveredArea != report.GrossArea-report.VisibleArea {
				t.Fatalf("trial %d layer %d: covered area inconsistent", trial, level)
			}
			for _, shard := range report.Shards {
				if _, inside := window.Clip(shard.Box); !inside || shard.Box.Area() != shard.Area {
					t.Fatalf("trial %d: shard outside window or bad area: %+v", trial, shard)
				}
				for _, source := range shard.Sources {
					if source == "" {
						t.Fatalf("trial %d: shard without sources", trial)
					}
				}
			}
		}
	}
}

// TestStackBudget forces more than MaxStackShards isolated fragments for one
// layer with a checkerboard of top-layer squares over a full bottom sheet.
func TestStackBudget(t *testing.T) {
	const span = 96
	window := box(0, 0, span, span)
	topRows := make([]catalog.Feature, 0)
	bottomRows := make([]catalog.Feature, 0)
	for x := 0; x < span; x += 2 {
		for y := 0; y < span; y += 2 {
			topRows = append(topRows, extent(fmt.Sprintf("tooth-%d-%d", x, y), int64(x), int64(y), int64(x+1), int64(y+1)))
		}
	}
	bottomRows = append(bottomRows, extent("sheet", 0, 0, span, span))
	_, err := Stack([]StackEntry{
		{Name: "top", Tree: treeOf(topRows...)},
		{Name: "bottom", Tree: treeOf(bottomRows...)},
	}, window)
	if err != ErrStackBudget {
		t.Fatalf("expected ErrStackBudget, got %v", err)
	}
}

func TestStackSourcesVerifyFragments(t *testing.T) {
	window := box(0, 0, 10, 10)
	top := treeOf(extent("a1", 0, 0, 5, 10))
	bottom := treeOf(extent("b1", 0, 0, 10, 5), extent("b2", 5, 5, 10, 10))
	result, err := Stack([]StackEntry{
		{Name: "top", Tree: top}, {Name: "bottom", Tree: bottom},
	}, window)
	if err != nil {
		t.Fatal(err)
	}
	// Bottom keeps [5,10)x[0,10), fused into one shard; both bottom features
	// meet that shard (b1 below y=5, b2 above), so both are verifiable sources.
	if len(result.Layers[1].Shards) != 1 {
		t.Fatalf("expected one fused shard, got %+v", result.Layers[1].Shards)
	}
	if got := result.Layers[1].Shards[0].Sources; len(got) != 2 || got[0] != "b1" || got[1] != "b2" {
		t.Fatalf("sources not attributable: %v", got)
	}
}
