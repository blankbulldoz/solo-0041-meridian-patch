package plane

// Seam is a positive-length shared rectangle boundary clipped to a query
// window. Fixed is X for a vertical segment and Y for a horizontal segment.
type Seam struct {
	Axis  string `json:"axis"`
	Fixed int64  `json:"fixed"`
	From  int64  `json:"from"`
	To    int64  `json:"to"`
}

// DistanceSquared is the squared Euclidean distance from p to the closed
// clipped segment. Points on the seam closure report zero, matching the
// closure rule used for nearest-feature distances.
func (s Seam) DistanceSquared(p Point) int64 {
	if s.Axis == "vertical" {
		dx := max(p.X-s.Fixed, s.Fixed-p.X)
		dy := max(s.From-p.Y, p.Y-s.To, 0)
		return dx*dx + dy*dy
	}
	dx := max(s.From-p.X, p.X-s.To, 0)
	dy := max(p.Y-s.Fixed, s.Fixed-p.Y)
	return dx*dx + dy*dy
}

func SharedBoundary(a, b, window Box) (Seam, bool) {
	if a.MaxX == b.MinX || b.MaxX == a.MinX {
		x := max(a.MinX, b.MinX)
		from := max(a.MinY, b.MinY, window.MinY)
		to := min(a.MaxY, b.MaxY, window.MaxY)
		if window.MinX <= x && x < window.MaxX && from < to {
			return Seam{Axis: "vertical", Fixed: x, From: from, To: to}, true
		}
	}
	if a.MaxY == b.MinY || b.MaxY == a.MinY {
		y := max(a.MinY, b.MinY)
		from := max(a.MinX, b.MinX, window.MinX)
		to := min(a.MaxX, b.MaxX, window.MaxX)
		if window.MinY <= y && y < window.MaxY && from < to {
			return Seam{Axis: "horizontal", Fixed: y, From: from, To: to}, true
		}
	}
	return Seam{}, false
}
