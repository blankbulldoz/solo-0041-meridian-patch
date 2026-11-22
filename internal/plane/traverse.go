package plane

// Rat is an exact rational number rendered as an explicit JSON fraction. The
// denominator is always positive and the pair is kept in lowest terms, so the
// same geometric position always serializes identically.
type Rat struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NewRat builds a reduced fraction with a positive denominator. Inputs are
// bounded by the coordinate limit and polyline length, so their products fit
// in int64 throughout the traversal arithmetic.
func NewRat(num, den int64) Rat {
	if den == 0 {
		panic("plane: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd64(abs64(num), den)
	return Rat{Num: num / g, Den: den / g}
}

func gcd64(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Cmp returns -1, 0 or 1 like cmp.Compare, without materializing a float.
func (r Rat) Cmp(other Rat) int {
	return cmp64(r.Num*other.Den, other.Num*r.Den)
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// RatPoint is a point whose coordinates may lie on fractional positions.
type RatPoint struct {
	X Rat `json:"x"`
	Y Rat `json:"y"`
}

// openInterval is a positive-length slice of a segment parameter space [lo, hi).
type openInterval struct {
	lo Rat
	hi Rat
}

func (i openInterval) Lo() Rat { return i.lo }
func (i openInterval) Hi() Rat { return i.hi }

// SegmentInterior reports the parameter interval t in [0,1] on the segment
// from a to b where every point lies strictly inside box. A positive-length
// interval is required: a segment that only rides an edge, passes through a
// corner or merely touches the boundary does not cross the interior and
// returns false. This mirrors the half-open [min,max) ownership of rectangles:
// traveling exactly along an excluded side can never be confused with a
// crossing.
func SegmentInterior(a, b Point, box Box) (openInterval, bool) {
	span := openInterval{lo: NewRat(0, 1), hi: NewRat(1, 1)}
	for _, axis := range []struct {
		start, delta, min, max int64
	}{
		{a.X, b.X - a.X, box.MinX, box.MaxX},
		{a.Y, b.Y - a.Y, box.MinY, box.MaxY},
	} {
		switch {
		case axis.delta == 0:
			// A stationary coordinate on an excluded boundary is never interior.
			if axis.start <= axis.min || axis.start >= axis.max {
				return openInterval{}, false
			}
		case axis.delta > 0:
			span = widen(span, NewRat(axis.min-axis.start, axis.delta), NewRat(axis.max-axis.start, axis.delta))
		default:
			// With a negative delta the max-side ratio is encountered first.
			span = widen(span, NewRat(axis.start-axis.max, -axis.delta), NewRat(axis.start-axis.min, -axis.delta))
		}
		if span.lo.Cmp(span.hi) >= 0 {
			return openInterval{}, false
		}
	}
	return span, true
}

func widen(span openInterval, lo, hi Rat) openInterval {
	if lo.Cmp(span.lo) > 0 {
		span.lo = lo
	}
	if hi.Cmp(span.hi) < 0 {
		span.hi = hi
	}
	return span
}

// PointAt evaluates a polyline at a global parameter t in [0, len(points)-1].
// The integer part selects a segment and the fractional part positions along
// it, so every recorded entry or exit position is exact.
func PointAt(points []Point, t Rat) RatPoint {
	whole := t.Num / t.Den
	if int(whole) >= len(points)-1 {
		last := points[len(points)-1]
		return RatPoint{X: NewRat(last.X, 1), Y: NewRat(last.Y, 1)}
	}
	local := NewRat(t.Num-whole*t.Den, t.Den)
	a, b := points[whole], points[whole+1]
	return RatPoint{
		X: NewRat(a.X*local.Den+(b.X-a.X)*local.Num, local.Den),
		Y: NewRat(a.Y*local.Den+(b.Y-a.Y)*local.Num, local.Den),
	}
}
