package catalog

import (
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func markFeature(id string, x, y int64) Feature {
	return Feature{ID: id, Class: "fixture", Shape: plane.Shape{Mark: &plane.Point{X: x, Y: y}}}
}

func extentFeature(id string, b plane.Box) Feature {
	return Feature{ID: id, Class: "fixture", Shape: plane.Shape{Extent: &b}}
}

func TestImportBatchValidate(t *testing.T) {
	good := plane.Identity()
	base := ImportBatch{
		Transform: good,
		Items:     []ImportItem{{Layer: "grid-a", Value: ptr(markFeature("brad", 0, 0))}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}

	empty := base
	empty.Items = nil
	if err := empty.Validate(); err == nil {
		t.Fatal("empty batch must be rejected")
	}

	badTransform := base
	badTransform.Transform.SignX = 0
	if err := badTransform.Validate(); err == nil {
		t.Fatal("bad transform must be rejected")
	}

	noValue := base
	noValue.Items[0].Value = nil
	if err := noValue.Validate(); err == nil {
		t.Fatal("item without value must be rejected")
	}

	badName := ImportBatch{Transform: good, Items: []ImportItem{
		{Layer: "Bad Name", Value: ptr(markFeature("brad", 0, 0))},
	}}
	if err := badName.Validate(); err == nil {
		t.Fatal("invalid layer name must be rejected")
	}
}

func TestFeatureApplyTransformMark(t *testing.T) {
	tr, err := plane.QuarterTurnTransform(plane.Rotate90)
	if err != nil {
		t.Fatal(err)
	}
	got, err := markFeature("brad", 2, 5).ApplyTransform(tr)
	if err != nil {
		t.Fatal(err)
	}
	if want := (plane.Point{X: -5, Y: 2}); *got.Shape.Mark != want {
		t.Fatalf("got %v want %v", *got.Shape.Mark, want)
	}
}

func TestFeatureApplyTransformExtent(t *testing.T) {
	tr, err := plane.PointReflection(plane.Point{X: 4, Y: 4})
	if err != nil {
		t.Fatal(err)
	}
	got, err := extentFeature("prong", plane.Box{MinX: 1, MinY: 0, MaxX: 3, MaxY: 2}).ApplyTransform(tr)
	if err != nil {
		t.Fatal(err)
	}
	want := plane.Box{MinX: 6, MinY: 7, MaxX: 8, MaxY: 9}
	if *got.Shape.Extent != want {
		t.Fatalf("got %v want %v", *got.Shape.Extent, want)
	}
}

func TestFeatureApplyTransformRejectsOutOfRangePoint(t *testing.T) {
	tr := plane.Transform{SignX: 1, SignY: 1, TranslateX: plane.CoordinateLimit}
	if _, err := markFeature("brad", 1, 0).ApplyTransform(tr); err == nil {
		t.Fatal("transformed point outside the system range must be rejected")
	}
}

func ptr(f Feature) *Feature { return &f }
