package plane

import "testing"

func TestTransformValidate(t *testing.T) {
	cases := []struct {
		name string
		t    Transform
		ok   bool
	}{
		{"identity", Identity(), true},
		{"point reflection", Transform{SignX: -1, SignY: -1, TranslateX: 4, TranslateY: 6}, true},
		{"quarter turn", Transform{Swap: true, SignX: -1, SignY: 1}, true},
		{"bad sign x", Transform{SignX: 0, SignY: 1}, false},
		{"bad sign y", Transform{SignX: 1, SignY: 2}, false},
		{"translation out of range", Transform{SignX: 1, SignY: 1, TranslateX: CoordinateLimit + 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.t.Validate()
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestQuarterTurnsOnPoint(t *testing.T) {
	p := Point{X: 2, Y: 5}
	want := map[QuarterTurn]Point{
		Rotate0:   {2, 5},
		Rotate90:  {-5, 2},
		Rotate180: {-2, -5},
		Rotate270: {5, -2},
	}
	for turn, expected := range want {
		tr, err := QuarterTurnTransform(turn)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		got, err := tr.Apply(p)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if got != expected {
			t.Fatalf("turn %d: got %v want %v", turn, got, expected)
		}
	}
	if _, err := QuarterTurnTransform(QuarterTurn(9)); err == nil {
		t.Fatal("expected invalid quarter turn error")
	}
}

func TestAxisSwapAndSignFlips(t *testing.T) {
	// (x,y) -> (-y, -x) + (10, 20): swap, both signs reversed, translated.
	tr := Transform{Swap: true, SignX: -1, SignY: -1, TranslateX: 10, TranslateY: 20}
	got, err := tr.Apply(Point{X: 3, Y: 7})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Point{X: 3, Y: 17}); got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPointReflection(t *testing.T) {
	// Reflection about (4, 6): (x,y) -> (8-x, 12-y).
	tr, err := PointReflection(Point{X: 4, Y: 6})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want Point }{
		{Point{4, 6}, Point{4, 6}},  // center is fixed
		{Point{0, 0}, Point{8, 12}}, // opposite corner
		{Point{5, 9}, Point{3, 3}},  // general point, twice applied returns it
		{Point{8, 12}, Point{0, 0}}, // round trip of the opposite corner
	}
	for _, tc := range cases {
		got, err := tr.Apply(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("reflect %v: got %v want %v", tc.in, got, tc.want)
		}
		back, err := tr.Apply(got)
		if err != nil || back != tc.in {
			t.Fatalf("reflection must be involutive: got %v err %v", back, err)
		}
	}
	if _, err := PointReflection(Point{X: CoordinateLimit, Y: 0}); err == nil {
		t.Fatal("expected error when doubled center overflows the limit")
	}
}

func TestApplyBoxKeepsHalfOpenAreaUnderReflection(t *testing.T) {
	// Point reflection about (4, 4): (x,y) -> (8-x, 12-y). On a reversed
	// axis [lo,hi) maps to [t-hi+1, t-lo+1), so [1,3) x [0,2) becomes
	// [6,8) x [7,9), the half-open box that owns exactly the reflected
	// lattice points of the source.
	tr, err := PointReflection(Point{X: 4, Y: 4})
	if err != nil {
		t.Fatal(err)
	}
	src := Box{MinX: 1, MinY: 0, MaxX: 3, MaxY: 2}
	got, err := tr.ApplyBox(src)
	if err != nil {
		t.Fatal(err)
	}
	want := Box{MinX: 6, MinY: 7, MaxX: 8, MaxY: 9}
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
	if got.Area() != src.Area() {
		t.Fatalf("area changed: %d vs %d", got.Area(), src.Area())
	}
	// Lattice-point ownership is carried over bijectively: every integer
	// point owned by the source maps to an integer point the image owns.
	owned := []Point{{1, 0}, {2, 0}, {1, 1}, {2, 1}}
	edge := []Point{{3, 0}, {1, 2}, {3, 2}}
	seen := make(map[Point]bool)
	for _, p := range owned {
		mapped, err := tr.Apply(p)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Owns(mapped) {
			t.Fatalf("mapped interior point %v -> %v lost ownership", p, mapped)
		}
		if seen[mapped] {
			t.Fatalf("lattice mapping must be injective, %v repeated", mapped)
		}
		seen[mapped] = true
	}
	// The reflected open-edge points land on the new open edges and stay
	// excluded, so the image owns exactly the four reflected owned points.
	for _, p := range edge {
		mapped, err := tr.Apply(p)
		if err != nil {
			t.Fatal(err)
		}
		if got.Owns(mapped) {
			t.Fatalf("open-edge point %v -> %v must stay excluded", p, mapped)
		}
	}
}

func TestApplyBoxUnderQuarterTurn(t *testing.T) {
	// Rotate90: (x,y) -> (-y,x). Box [1,4) x [2,5): after the axis swap
	// the old y interval feeds the reversed new x axis as [-4,-1), and the
	// old x interval feeds the preserved new y axis as [1,4). Width/height
	// swap and the nine lattice points stay owned.
	tr, _ := QuarterTurnTransform(Rotate90)
	got, err := tr.ApplyBox(Box{MinX: 1, MinY: 2, MaxX: 4, MaxY: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := Box{MinX: -4, MinY: 1, MaxX: -1, MaxY: 4}
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
	// Every one of the nine source lattice points maps into the image box.
	count := 0
	for x := int64(1); x < 4; x++ {
		for y := int64(2); y < 5; y++ {
			p, err := tr.Apply(Point{X: x, Y: y})
			if err != nil {
				t.Fatal(err)
			}
			if !got.Owns(p) {
				t.Fatalf("lattice point %v -> %v lost under quarter turn", Point{x, y}, p)
			}
			count++
		}
	}
	if count != 9 || got.Area() != 9 {
		t.Fatalf("expected 9 preserved lattice points and area, got %d / %d", count, got.Area())
	}
}

func TestApplyRejectsOverflow(t *testing.T) {
	// MinInt64 shifted by a valid negative translation cannot be represented.
	tr := Transform{SignX: 1, SignY: 1, TranslateX: -CoordinateLimit}
	if _, err := tr.Apply(Point{X: -1 << 63, Y: 0}); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestApplyBoxRejectsInvalidInput(t *testing.T) {
	tr := Identity()
	if _, err := tr.ApplyBox(Box{MinX: 4, MinY: 0, MaxX: 4, MaxY: 2}); err == nil {
		t.Fatal("expected positive-area validation error")
	}
}

func TestPointWithinLimit(t *testing.T) {
	if !(Point{X: CoordinateLimit, Y: -CoordinateLimit}).WithinLimit() {
		t.Fatal("boundary points must be inside the limit")
	}
	if (Point{X: CoordinateLimit + 1, Y: 0}).WithinLimit() {
		t.Fatal("point beyond the limit must be rejected")
	}
}

func TestReflectedBoxesKeepSharedEdge(t *testing.T) {
	// Two source boxes share the half-open edge x=4 over y in [0,3).
	left := Box{MinX: 0, MinY: 0, MaxX: 4, MaxY: 3}
	right := Box{MinX: 4, MinY: 0, MaxX: 8, MaxY: 3}
	tr, err := PointReflection(Point{X: 8, Y: 4})
	if err != nil {
		t.Fatal(err)
	}
	a, err := tr.ApplyBox(left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tr.ApplyBox(right)
	if err != nil {
		t.Fatal(err)
	}
	// They must still meet along a full edge: neither overlap nor a gap.
	if got, want := a.Area()+b.Area(), left.Area()+right.Area(); got != want {
		t.Fatalf("total area changed: %d vs %d", got, want)
	}
	if a.MaxX != b.MinX && b.MaxX != a.MinX {
		t.Fatalf("reflected boxes lost their shared edge: %v and %v", a, b)
	}
	if _, overlaps := a.Clip(b); overlaps {
		t.Fatalf("reflected neighbors must not overlap: %v and %v", a, b)
	}
}
