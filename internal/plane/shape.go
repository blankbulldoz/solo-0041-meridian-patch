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

// Clip restricts a shape to the half-open window b. A mark is returned
// unchanged when b owns it; an extent is returned as its positive-area
// intersection with b. Shapes that only touch an open edge are not visible.
func (s Shape) Clip(b Box) (Shape, bool) {
	if s.Mark != nil {
		if !b.Owns(*s.Mark) {
			return Shape{}, false
		}
		return s, true
	}
	clipped, visible := s.Extent.Clip(b)
	if !visible {
		return Shape{}, false
	}
	return Shape{Extent: &clipped}, true
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
