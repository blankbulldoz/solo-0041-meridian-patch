package plane

// Relation distinguishes intersection from zero closed-envelope distance.
// Containment is directional, so a pair of identical marks contains each other.
type Relation struct {
	Intersects      bool  `json:"intersects"`
	LeftContains    bool  `json:"left_contains"`
	RightContains   bool  `json:"right_contains"`
	DistanceSquared int64 `json:"distance_squared"`
	Intersection    *Box  `json:"intersection,omitempty"`
}

func Relate(left, right Shape) Relation {
	a, b := left.Envelope(), right.Envelope()
	dx := max(a.MinX-b.MaxX, b.MinX-a.MaxX, 0)
	dy := max(a.MinY-b.MaxY, b.MinY-a.MaxY, 0)
	result := Relation{
		Intersects:      left.Meets(right),
		LeftContains:    contains(left, right),
		RightContains:   contains(right, left),
		DistanceSquared: dx*dx + dy*dy,
	}
	if left.Extent != nil && right.Extent != nil {
		if clip, ok := a.Clip(b); ok {
			result.Intersection = &clip
		}
	}
	return result
}

func contains(container, item Shape) bool {
	if item.Mark != nil {
		return container.At(*item.Mark)
	}
	return container.Extent != nil && container.Extent.ContainsBox(*item.Extent)
}
