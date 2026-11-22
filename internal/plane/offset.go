package plane

import "fmt"

// OffsetLimit bounds a translation so that adding it to any valid coordinate
// stays well inside int64, leaving the translated shape for ordinary Validate.
const OffsetLimit int64 = 2 * CoordinateLimit

// Offset is an integer translation applied to a selected region.
type Offset struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

func (o Offset) Validate() error {
	if o.X < -OffsetLimit || o.X > OffsetLimit || o.Y < -OffsetLimit || o.Y > OffsetLimit {
		return fmt.Errorf("offset components must be within [%d,%d]", -OffsetLimit, OffsetLimit)
	}
	return nil
}

func (o Offset) Point(p Point) Point {
	return Point{X: p.X + o.X, Y: p.Y + o.Y}
}

func (o Offset) Box(b Box) Box {
	return Box{MinX: b.MinX + o.X, MinY: b.MinY + o.Y, MaxX: b.MaxX + o.X, MaxY: b.MaxY + o.Y}
}

// Apply returns a translated copy of a shape. The source shape is untouched.
func (o Offset) Apply(s Shape) Shape {
	if s.Mark != nil {
		moved := o.Point(*s.Mark)
		return Shape{Mark: &moved}
	}
	moved := o.Box(*s.Extent)
	return Shape{Extent: &moved}
}
