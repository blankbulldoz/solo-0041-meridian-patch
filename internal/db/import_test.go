package db

import (
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func newTestDatabase(t *testing.T) *Database {
	t.Helper()
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func mark(id string, x, y int64) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture", Shape: plane.Shape{Mark: &plane.Point{X: x, Y: y}}}
}

func extent(id string, b plane.Box) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture", Shape: plane.Shape{Extent: &b}}
}

func TestImportCommitsMultipleLayersInOneRevision(t *testing.T) {
	d := newTestDatabase(t)
	bounds := plane.Box{MinX: -20, MinY: -20, MaxX: 20, MaxY: 20}
	if _, err := d.Register(catalog.Definition{Name: "grid-marks", Kind: plane.Mark, Layout: catalog.Layout{Bounds: bounds}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Register(catalog.Definition{Name: "grid-boxes", Kind: plane.Extent, Layout: catalog.Layout{Bounds: bounds, Exclusive: true}}, nil); err != nil {
		t.Fatal(err)
	}

	tr := plane.Transform{Swap: true, SignX: -1, SignY: 1} // Rotate90
	batch := catalog.ImportBatch{
		Transform: tr,
		Items: []catalog.ImportItem{
			{Layer: "grid-marks", Value: ptr(mark("brad", 2, 5))},
			{Layer: "grid-marks", Value: ptr(mark("stud", 0, 0))},
			{Layer: "grid-boxes", Value: ptr(extent("prong", plane.Box{MinX: 1, MinY: 2, MaxX: 4, MaxY: 5}))},
		},
	}
	commit, err := d.Import(batch)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(commit.Layers) != 2 || commit.Revision != 3 {
		t.Fatalf("expected one shared revision 3 over two layers, got %+v", commit)
	}

	s := d.head.Load()
	pins := s.layers["grid-marks"].rows
	if got := *pins["brad"].Shape.Mark; got != (plane.Point{X: -5, Y: 2}) {
		t.Fatalf("brad transformed to %v", got)
	}
	if got := *pins["stud"].Shape.Mark; got != (plane.Point{X: 0, Y: 0}) {
		t.Fatalf("stud transformed to %v", got)
	}
	box := s.layers["grid-boxes"].rows["prong"].Shape.Extent
	if want := (plane.Box{MinX: -4, MinY: 1, MaxX: -1, MaxY: 4}); *box != want {
		t.Fatalf("prong transformed to %v want %v", *box, want)
	}
}

func TestImportRejectsOutOfRangeAndKeepsOldView(t *testing.T) {
	d := newTestDatabase(t)
	bounds := plane.Box{MinX: -20, MinY: -20, MaxX: 20, MaxY: 20}
	if _, err := d.Register(catalog.Definition{Name: "grid-marks", Kind: plane.Mark, Layout: catalog.Layout{Bounds: bounds}}, nil); err != nil {
		t.Fatal(err)
	}
	before := d.Status().Revision
	tr := plane.Transform{SignX: 1, SignY: 1, TranslateX: plane.CoordinateLimit}
	batch := catalog.ImportBatch{
		Transform: tr,
		Items:     []catalog.ImportItem{{Layer: "grid-marks", Value: ptr(mark("brad", 1, 0))}},
	}
	if _, err := d.Import(batch); err == nil {
		t.Fatal("import beyond the coordinate limit must fail")
	}
	if d.Status().Revision != before {
		t.Fatal("failed import must not advance the revision")
	}
	if len(d.head.Load().layers["grid-marks"].rows) != 0 {
		t.Fatal("failed import must leave the layer empty")
	}
}

func TestImportRejectsDuplicateAndMissingLayerAtomically(t *testing.T) {
	d := newTestDatabase(t)
	bounds := plane.Box{MinX: -20, MinY: -20, MaxX: 20, MaxY: 20}
	if _, err := d.Register(catalog.Definition{Name: "grid-marks", Kind: plane.Mark, Layout: catalog.Layout{Bounds: bounds}}, nil); err != nil {
		t.Fatal(err)
	}
	base := catalog.ImportBatch{
		Transform: plane.Identity(),
		Items: []catalog.ImportItem{
			{Layer: "grid-marks", Value: ptr(mark("brad", 1, 1))},
			{Layer: "ghost-layer", Value: ptr(mark("stud", 2, 2))},
		},
	}
	if _, err := d.Import(base); err == nil {
		t.Fatal("import into a missing layer must be rejected")
	}
	if len(d.head.Load().layers["grid-marks"].rows) != 0 {
		t.Fatal("nothing from a rejected multi-layer batch may be visible")
	}

	// Re-importing the same ID conflicts and rolls back.
	first := catalog.ImportBatch{
		Transform: plane.Identity(),
		Items:     []catalog.ImportItem{{Layer: "grid-marks", Value: ptr(mark("brad", 1, 1))}},
	}
	if _, err := d.Import(first); err != nil {
		t.Fatal(err)
	}
	again := catalog.ImportBatch{
		Transform: plane.Identity(),
		Items:     []catalog.ImportItem{{Layer: "grid-marks", Value: ptr(mark("brad", 2, 2))}},
	}
	if _, err := d.Import(again); err == nil {
		t.Fatal("duplicate feature id must be rejected")
	}
	if got := d.head.Load().layers["grid-marks"].rows["brad"].Shape.Mark; *got != (plane.Point{X: 1, Y: 1}) {
		t.Fatalf("rolled-back duplicate changed stored feature: %v", got)
	}
}

func TestImportRejectsExclusiveOverlap(t *testing.T) {
	d := newTestDatabase(t)
	bounds := plane.Box{MinX: -20, MinY: -20, MaxX: 20, MaxY: 20}
	if _, err := d.Register(catalog.Definition{
		Name: "grid-boxes", Kind: plane.Extent, Layout: catalog.Layout{Bounds: bounds, Exclusive: true},
	}, nil); err != nil {
		t.Fatal(err)
	}
	batch := catalog.ImportBatch{
		Transform: plane.Identity(),
		Items: []catalog.ImportItem{
			{Layer: "grid-boxes", Value: ptr(extent("a", plane.Box{MinX: 0, MinY: 0, MaxX: 4, MaxY: 4}))},
			{Layer: "grid-boxes", Value: ptr(extent("b", plane.Box{MinX: 2, MinY: 2, MaxX: 6, MaxY: 6}))},
		},
	}
	if _, err := d.Import(batch); err == nil {
		t.Fatal("overlapping extents in an exclusive layer must be rejected")
	}
}

func TestImportHonorsRevisionBarrier(t *testing.T) {
	d := newTestDatabase(t)
	bounds := plane.Box{MinX: -20, MinY: -20, MaxX: 20, MaxY: 20}
	if _, err := d.Register(catalog.Definition{Name: "grid-marks", Kind: plane.Mark, Layout: catalog.Layout{Bounds: bounds}}, nil); err != nil {
		t.Fatal(err)
	}
	stale := uint64(0) // registration already advanced the database to revision 1
	batch := catalog.ImportBatch{
		IfRevision: &stale,
		Transform:  plane.Identity(),
		Items:      []catalog.ImportItem{{Layer: "grid-marks", Value: ptr(mark("brad", 1, 1))}},
	}
	if _, err := d.Import(batch); err == nil {
		t.Fatal("stale if_revision must be rejected")
	}
}

func ptr(f catalog.Feature) *catalog.Feature { return &f }
