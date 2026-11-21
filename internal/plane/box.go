// Package plane defines the integer coordinate system used throughout the database.
package plane

import "fmt"

// CoordinateLimit ensures that any squared point distance and any area fit in int64.
const CoordinateLimit int64 = 1_000_000_000

type Point struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

func (p Point) Validate() error {
	if !bounded(p.X) || !bounded(p.Y) {
		return fmt.Errorf("point coordinates must be within [%d,%d]", -CoordinateLimit, CoordinateLimit)
	}
	return nil
}

func bounded(n int64) bool {
	return n >= -CoordinateLimit && n <= CoordinateLimit
}

// Box denotes [MinX,MaxX) × [MinY,MaxY) when used as a rectangle. An
// index envelope can also have zero width or height, for a stored point.
type Box struct {
	MinX int64 `json:"min_x"`
	MinY int64 `json:"min_y"`
	MaxX int64 `json:"max_x"`
	MaxY int64 `json:"max_y"`
}

func (b Box) Validate() error {
	if !bounded(b.MinX) || !bounded(b.MaxX) || !bounded(b.MinY) || !bounded(b.MaxY) {
		return fmt.Errorf("rectangle coordinates must be within [%d,%d]", -CoordinateLimit, CoordinateLimit)
	}
	if b.MinX >= b.MaxX || b.MinY >= b.MaxY {
		return fmt.Errorf("rectangle must have positive width and height")
	}
	return nil
}

func (p Point) Envelope() Box {
	return Box{MinX: p.X, MinY: p.Y, MaxX: p.X, MaxY: p.Y}
}

func (b Box) Owns(p Point) bool {
	return b.MinX <= p.X && p.X < b.MaxX && b.MinY <= p.Y && p.Y < b.MaxY
}

func (b Box) ContainsBox(other Box) bool {
	return b.MinX <= other.MinX && b.MaxX >= other.MaxX &&
		b.MinY <= other.MinY && b.MaxY >= other.MaxY
}

// MayMeet is inclusive. It is safe for pruning both points and rectangles.
func (b Box) MayMeet(other Box) bool {
	return b.MinX <= other.MaxX && other.MinX <= b.MaxX &&
		b.MinY <= other.MaxY && other.MinY <= b.MaxY
}

func (b Box) Union(other Box) Box {
	return Box{
		MinX: min(b.MinX, other.MinX), MinY: min(b.MinY, other.MinY),
		MaxX: max(b.MaxX, other.MaxX), MaxY: max(b.MaxY, other.MaxY),
	}
}

func (b Box) Clip(other Box) (Box, bool) {
	clip := Box{
		MinX: max(b.MinX, other.MinX), MinY: max(b.MinY, other.MinY),
		MaxX: min(b.MaxX, other.MaxX), MaxY: min(b.MaxY, other.MaxY),
	}
	return clip, clip.MaxX > clip.MinX && clip.MaxY > clip.MinY
}

func (b Box) Area() int64 {
	return (b.MaxX - b.MinX) * (b.MaxY - b.MinY)
}

func (b Box) DistanceSquared(p Point) int64 {
	dx := max(b.MinX-p.X, p.X-b.MaxX, 0)
	dy := max(b.MinY-p.Y, p.Y-b.MaxY, 0)
	return dx*dx + dy*dy
}
