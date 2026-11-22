package catalog

import (
	"fmt"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// ImportItem is one source feature waiting to be mapped into a layer.
// Its shape is expressed in the source grid coordinates; the batch's
// shared plane.Transform moves it onto the target plane.
type ImportItem struct {
	Layer string   `json:"layer"`
	Value *Feature `json:"value"`
}

// ImportBatch submits several layers in one transaction. A single transform
// applies to every item, matching a grid imported as a whole. The write is
// all-or-nothing: any invalid item rejects the entire batch.
type ImportBatch struct {
	IfRevision *uint64         `json:"if_revision,omitempty"`
	Transform  plane.Transform `json:"transform"`
	Items      []ImportItem    `json:"items"`
}

// Validate checks batch structure before touching the database. Individual
// feature geometry is validated again against the target layer after the
// transform is applied.
func (b ImportBatch) Validate() error {
	if err := b.Transform.Validate(); err != nil {
		return err
	}
	if len(b.Items) == 0 || len(b.Items) > MaxEdits {
		return fmt.Errorf("import batch must contain 1..%d items", MaxEdits)
	}
	for i, item := range b.Items {
		if !ValidName(item.Layer) {
			return fmt.Errorf("import item %d: invalid layer name", i)
		}
		if item.Value == nil {
			return fmt.Errorf("import item %d: value is required", i)
		}
		if !ValidName(item.Value.ID) || !ValidName(item.Value.Class) {
			return fmt.Errorf("import item %d: invalid feature id or class", i)
		}
	}
	return nil
}

// ApplyTransform maps a feature's shape from the source grid to the target
// plane. Marks move as points; extents use the half-open box rule described
// on plane.Transform.ApplyBox, so area and lattice-point ownership survive
// sign reversal and axis exchange.
func (f Feature) ApplyTransform(t plane.Transform) (Feature, error) {
	out := Feature{ID: f.ID, Class: f.Class}
	shape := f.Shape
	switch {
	case shape.Mark != nil && shape.Extent == nil:
		p, err := t.Apply(*shape.Mark)
		if err != nil {
			return Feature{}, fmt.Errorf("feature %q: %w", f.ID, err)
		}
		if !p.WithinLimit() {
			return Feature{}, fmt.Errorf("feature %q: transformed point %v exceeds coordinate limit", f.ID, p)
		}
		out.Shape = plane.Shape{Mark: &p}
	case shape.Extent != nil && shape.Mark == nil:
		b, err := t.ApplyBox(*shape.Extent)
		if err != nil {
			return Feature{}, fmt.Errorf("feature %q: %w", f.ID, err)
		}
		if err := b.Validate(); err != nil {
			return Feature{}, fmt.Errorf("feature %q: transformed extent: %w", f.ID, err)
		}
		out.Shape = plane.Shape{Extent: &b}
	default:
		return Feature{}, fmt.Errorf("feature %q: shape must contain exactly one mark or extent", f.ID)
	}
	return out, nil
}
