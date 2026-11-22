// Package catalog owns the public layer and feature model and its local constraints.
package catalog

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const (
	MaxLayers    = 24
	MaxLayerRows = 4096
	MaxRows      = 16384
	MaxEdits     = 512
	// MaxCuts bounds the cut lines per axis so the hotspot grid stays cheap
	// to build and audit: at most (MaxCuts+1)² cells per layer.
	MaxCuts = 16
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// Cuts partitions a layer into hotspot cells along custom vertical (X) and
// horizontal (Y) cut lines. Cut lines are strictly increasing and lie inside
// the layer bounds. The index built from cuts is a process-local structure;
// the cuts themselves persist with the layer definition in database images.
type Cuts struct {
	X []int64 `json:"x,omitempty"`
	Y []int64 `json:"y,omitempty"`
}

func (c Cuts) Validate(bounds plane.Box) error {
	if len(c.X) == 0 && len(c.Y) == 0 {
		return fmt.Errorf("cuts must contain at least one cut line")
	}
	if err := validateCutLines(c.X, bounds.MinX, bounds.MaxX, "x"); err != nil {
		return err
	}
	return validateCutLines(c.Y, bounds.MinY, bounds.MaxY, "y")
}

func validateCutLines(cuts []int64, lo, hi int64, axis string) error {
	if len(cuts) > MaxCuts {
		return fmt.Errorf("%s cuts exceed the maximum of %d", axis, MaxCuts)
	}
	previous := lo
	for _, cut := range cuts {
		if cut <= lo || cut >= hi {
			return fmt.Errorf("%s cut %d is outside the layer bounds", axis, cut)
		}
		if cut <= previous {
			return fmt.Errorf("%s cuts must be strictly increasing", axis)
		}
		previous = cut
	}
	return nil
}

func cutsEqual(a, b *Cuts) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(a.X, b.X) && slices.Equal(a.Y, b.Y)
}

type Layout struct {
	Bounds    plane.Box `json:"bounds"`
	Exclusive bool      `json:"exclusive"`
	Cuts      *Cuts     `json:"cuts,omitempty"`
}

func (l Layout) Validate() error {
	if err := l.Bounds.Validate(); err != nil {
		return err
	}
	if l.Cuts != nil {
		return l.Cuts.Validate(l.Bounds)
	}
	return nil
}

// Equal reports whether two layouts describe the same definition, so a
// reframe that changes nothing keeps the current revision.
func (l Layout) Equal(o Layout) bool {
	return l.Bounds == o.Bounds && l.Exclusive == o.Exclusive && cutsEqual(l.Cuts, o.Cuts)
}

type Definition struct {
	Name string     `json:"name"`
	Kind plane.Kind `json:"kind"`
	Layout
}

func (d Definition) Validate() error {
	if !ValidName(d.Name) {
		return fmt.Errorf("invalid layer name %q", d.Name)
	}
	if d.Kind != plane.Mark && d.Kind != plane.Extent {
		return fmt.Errorf("kind must be mark or extent")
	}
	return d.Layout.Validate()
}

type Summary struct {
	Definition
	Revision uint64 `json:"revision"`
	Count    int    `json:"count"`
}

type Feature struct {
	ID    string      `json:"id"`
	Class string      `json:"class"`
	Shape plane.Shape `json:"shape"`
}

func (f Feature) Validate(d Definition) error {
	if !ValidName(f.ID) || !ValidName(f.Class) {
		return fmt.Errorf("invalid feature id or class for %q", f.ID)
	}
	if err := f.Shape.Validate(d.Kind); err != nil {
		return fmt.Errorf("feature %q: %w", f.ID, err)
	}
	if !f.Shape.Within(d.Bounds) {
		return fmt.Errorf("feature %q is outside %s bounds", f.ID, d.Name)
	}
	return nil
}

func (f Feature) Copy() Feature {
	f.Shape = f.Shape.Copy()
	return f
}
