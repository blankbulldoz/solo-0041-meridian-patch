package catalog

import (
	"fmt"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const MaxQuotas = 64

// Quota caps the union area that extent features across the listed layers may
// cover inside Bounds. Overlapping coverage is counted once. Layers are stored
// sorted by name so images have a canonical form.
type Quota struct {
	Name    string    `json:"name"`
	Bounds  plane.Box `json:"bounds"`
	Layers  []string  `json:"layers"`
	MaxArea int64     `json:"max_area"`
}

func (q Quota) Validate() error {
	if !ValidName(q.Name) {
		return fmt.Errorf("invalid quota name %q", q.Name)
	}
	if err := q.Bounds.Validate(); err != nil {
		return err
	}
	if len(q.Layers) == 0 || len(q.Layers) > MaxLayers {
		return fmt.Errorf("quota %q must list 1..%d layers", q.Name, MaxLayers)
	}
	seen := make(map[string]bool, len(q.Layers))
	for _, name := range q.Layers {
		if !ValidName(name) {
			return fmt.Errorf("invalid quota layer name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("quota %q repeats layer %q", q.Name, name)
		}
		seen[name] = true
	}
	if q.MaxArea < 0 {
		return fmt.Errorf("quota %q max_area must be nonnegative", q.Name)
	}
	return nil
}
