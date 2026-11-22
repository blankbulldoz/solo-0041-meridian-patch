package plane

import (
	"errors"
	"fmt"
)

// Transform is an integer grid isometry followed by an integer translation.
// It moves coordinates from the source grid onto the target plane.
//
// Every component is one of the eight signed permutation matrices of Z^2
// (the dihedral group of the square), so the transform supports any
// combination of axis exchange, per-axis sign reversal and the quarter
// turns needed to align two grids whose axes differ by a quarter rotation.
//
// Evaluation order is fixed and written out explicitly:
//
//	(u, v) = swap ? (y, x) : (x, y)
//	x' = sx*u + tx
//	y' = sy*v + ty
//
// i.e. the signed linear part (exchange axes when Swap is true, then apply
// the per-axis signs) runs first, and the translation is added last.
// Composition of transforms must apply this same order.
type Transform struct {
	// Swap exchanges the two coordinates before signs are applied. When
	// true, the linear matrix is anti-diagonal (reflection or odd quarter
	// turn); when false it is diagonal (identity or point/axis reflection).
	Swap bool `json:"swap"`
	// SignX and SignY must each be either +1 or -1.
	SignX int64 `json:"sign_x"`
	SignY int64 `json:"sign_y"`
	// TranslateX and TranslateY are added after the signed linear part.
	TranslateX int64 `json:"translate_x"`
	TranslateY int64 `json:"translate_y"`
}

// Identity is the transform that leaves every coordinate untouched.
func Identity() Transform {
	return Transform{SignX: 1, SignY: 1}
}

// Validate rejects anything that is not a signed axis permutation. The
// translation is range-checked here so that no later arithmetic can wrap.
func (t Transform) Validate() error {
	if t.SignX != 1 && t.SignX != -1 {
		return fmt.Errorf("transform sign_x must be 1 or -1, got %d", t.SignX)
	}
	if t.SignY != 1 && t.SignY != -1 {
		return fmt.Errorf("transform sign_y must be 1 or -1, got %d", t.SignY)
	}
	if !bounded(t.TranslateX) || !bounded(t.TranslateY) {
		return fmt.Errorf("transform translation must be within [%d,%d]", -CoordinateLimit, CoordinateLimit)
	}
	return nil
}

var errOverflow = errors.New("integer overflow while applying transform")

// Apply maps a single point through the transform. It returns an error
// instead of wrapping when the result cannot be represented in int64.
func (t Transform) Apply(p Point) (Point, error) {
	if err := t.Validate(); err != nil {
		return Point{}, err
	}
	sourceX, sourceY := p.X, p.Y
	if t.Swap {
		sourceX, sourceY = p.Y, p.X
	}
	// Signs are restricted to +1/-1 by Validate, so the linear step is a
	// conditional negation that cannot wrap; only the translation adds.
	x, y := t.SignX*sourceX, t.SignY*sourceY
	x, ok1 := signedAdd(x, t.TranslateX)
	y, ok2 := signedAdd(y, t.TranslateY)
	if !ok1 || !ok2 {
		return Point{}, errOverflow
	}
	return Point{X: x, Y: y}, nil
}

// ApplyBox maps the half-open rectangle [MinX,MaxX) x [MinY,MaxY) through
// the transform.
//
// Written rule, one axis at a time. A sign-preserving axis moves the
// integer half-open interval [lo,hi) by translation only:
//
//	[lo,hi) -> [t+lo, t+hi)
//
// A sign-reversing axis reflects the interval, which moves the open end
// from the high side to the low side. To keep the rectangle half-open with
// the lower corner included in target coordinates, and to keep owning
// exactly the images of the lattice points it owned before, the interval
// is re-closed one unit inward on each end:
//
//	[lo,hi) -> [t-hi+1, t-lo+1)
//
// Consequences that can be checked on paper:
//   - width and height are preserved, so the area never changes;
//   - an owned integer point p (box.Owns(p) == true) maps to an integer
//     point the result owns, and the mapping stays bijective on lattice
//     points, including under point reflection (both signs reversed);
//   - two rectangles sharing an edge still share an edge after the import,
//     with no unit-wide gap or overlap;
//   - when Swap is set, the two source intervals exchange axes before the
//     per-axis sign and translation rules run.
func (t Transform) ApplyBox(b Box) (Box, error) {
	if err := b.Validate(); err != nil {
		return Box{}, err
	}
	if err := t.Validate(); err != nil {
		return Box{}, err
	}
	fromX := interval{lo: b.MinX, hi: b.MaxX}
	fromY := interval{lo: b.MinY, hi: b.MaxY}
	if t.Swap {
		fromX, fromY = fromY, fromX
	}
	x, err := mapInterval(fromX, t.SignX, t.TranslateX)
	if err != nil {
		return Box{}, err
	}
	y, err := mapInterval(fromY, t.SignY, t.TranslateY)
	if err != nil {
		return Box{}, err
	}
	return Box{MinX: x.lo, MinY: y.lo, MaxX: x.hi, MaxY: y.hi}, nil
}

type interval struct{ lo, hi int64 }

func mapInterval(in interval, sign, translate int64) (interval, error) {
	if sign == 1 {
		lo, ok1 := signedAdd(translate, in.lo)
		hi, ok2 := signedAdd(translate, in.hi)
		if !ok1 || !ok2 {
			return interval{}, errOverflow
		}
		return interval{lo: lo, hi: hi}, nil
	}
	lo, ok1 := signedAdd(translate, -in.hi)
	hi, ok2 := signedAdd(translate, -in.lo)
	if !ok1 || !ok2 {
		return interval{}, errOverflow
	}
	lo, ok1 = signedAdd(lo, 1)
	hi, ok2 = signedAdd(hi, 1)
	if !ok1 || !ok2 {
		return interval{}, errOverflow
	}
	return interval{lo: lo, hi: hi}, nil
}

// PointReflection returns the half-turn (point reflection) about center c.
//
// Rule: reflection of p through c is 2*c - p, evaluated independently on
// each axis. It is the same transform as sign reversal on both axes with a
// translation of twice the center, so points on the center stay fixed and
// every other point swaps places with its opposite. An error is returned
// when 2*c does not fit in int64 or inside the coordinate limit.
func PointReflection(c Point) (Transform, error) {
	cx, okX := signedAdd(c.X, c.X)
	cy, okY := signedAdd(c.Y, c.Y)
	if !okX || !okY || !bounded(cx) || !bounded(cy) {
		return Transform{}, fmt.Errorf("point reflection center %v doubles outside the coordinate range", c)
	}
	t := Transform{SignX: -1, SignY: -1, TranslateX: cx, TranslateY: cy}
	return t, t.Validate()
}

// QuarterTurn describes the quarter rotations used to reconcile two grids.
type QuarterTurn int

const (
	// Rotate0 is the identity.
	Rotate0 QuarterTurn = 0
	// Rotate90 is the counter-clockwise quarter turn: (x,y) -> (-y,x).
	Rotate90 QuarterTurn = 1
	// Rotate180 is the half turn: (x,y) -> (-x,-y).
	Rotate180 QuarterTurn = 2
	// Rotate270 is the clockwise quarter turn: (x,y) -> (y,-x).
	Rotate270 QuarterTurn = 3
)

// QuarterTurnTransform returns the signed permutation for the given
// counter-clockwise quarter turn, without any translation.
func QuarterTurnTransform(turn QuarterTurn) (Transform, error) {
	switch turn {
	case Rotate0:
		return Identity(), nil
	case Rotate90:
		return Transform{Swap: true, SignX: -1, SignY: 1}, nil
	case Rotate180:
		return Transform{SignX: -1, SignY: -1}, nil
	case Rotate270:
		return Transform{Swap: true, SignX: 1, SignY: -1}, nil
	default:
		return Transform{}, fmt.Errorf("quarter turn must be 0..3, got %d", turn)
	}
}

// WithinLimit reports whether the point lies inside the system coordinate
// range [-CoordinateLimit, CoordinateLimit]. Callers use it after Apply to
// reject imports that land outside the database plane.
func (p Point) WithinLimit() bool {
	return bounded(p.X) && bounded(p.Y)
}

func signedAdd(a, b int64) (int64, bool) {
	sum := a + b
	// Overflow happens precisely when both operands have one sign and the
	// result has the opposite sign.
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum > 0) {
		return 0, false
	}
	return sum, true
}
