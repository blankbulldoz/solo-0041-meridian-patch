package plane

import "fmt"

type Kind string

const (
	Mark   Kind = "mark"
	Extent Kind = "extent"
)

type Shape struct {
	Mark   *Point `json:"mark,omitempty"`
	Extent *Box   `json:"extent,omitempty"`
}

func (s Shape) Validate(kind Kind) error {
	switch {
	case kind == Mark && s.Mark != nil && s.Extent == nil:
		return s.Mark.Validate()
	case kind == Extent && s.Extent != nil && s.Mark == nil:
		return s.Extent.Validate()
	default:
		return fmt.Errorf("shape must contain exactly one %s", kind)
	}
}

func (s Shape) Copy() Shape {
	if s.Mark != nil {
		p := *s.Mark
		return Shape{Mark: &p}
	}
	b := *s.Extent
	return Shape{Extent: &b}
}

func (s Shape) Envelope() Box {
	if s.Mark != nil {
		return s.Mark.Envelope()
	}
	return *s.Extent
}

// Anchor returns the representative coordinate used to order window results.
// A mark represents itself. An extent represents the lower-left corner of its
// intersection with window, so a shape reaching in from outside is ordered by
// the corner visible inside the window rather than by its own lower-left
// corner. Callers only use it for shapes that match the window.
func (s Shape) Anchor(window Box) Point {
	if s.Mark != nil {
		return *s.Mark
	}
	return Point{
		X: max(s.Extent.MinX, window.MinX),
		Y: max(s.Extent.MinY, window.MinY),
	}
}

func (s Shape) Within(b Box) bool {
	if s.Mark != nil {
		return b.Owns(*s.Mark)
	}
	return b.ContainsBox(*s.Extent)
}

func (s Shape) InWindow(b Box) bool {
	if s.Mark != nil {
		return b.Owns(*s.Mark)
	}
	_, meets := b.Clip(*s.Extent)
	return meets
}

func (s Shape) At(p Point) bool {
	if s.Mark != nil {
		return *s.Mark == p
	}
	return s.Extent.Owns(p)
}

func (s Shape) Meets(other Shape) bool {
	if s.Mark != nil {
		return other.At(*s.Mark)
	}
	return other.InWindow(*s.Extent)
}
