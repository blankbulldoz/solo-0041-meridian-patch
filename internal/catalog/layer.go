// Package catalog owns the public layer and feature model and its local constraints.
package catalog

import (
	"fmt"
	"regexp"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const (
	MaxLayers          = 24
	MaxLayerRows       = 4096
	MaxRows            = 16384
	MaxEdits           = 512
	MaxRehearsalProbes = 8
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

type Layout struct {
	Bounds    plane.Box `json:"bounds"`
	Exclusive bool      `json:"exclusive"`
}

func (l Layout) Validate() error {
	return l.Bounds.Validate()
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
