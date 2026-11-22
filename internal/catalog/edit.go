package catalog

import "fmt"

// Shift carries a group translation: every listed feature moves by the same
// (Dx, Dy), preserving its shape and the relative positions inside the group.
type Shift struct {
	IDs []string `json:"ids"`
	Dx  int64    `json:"dx"`
	Dy  int64    `json:"dy"`
}

type Edit struct {
	Layer string   `json:"layer"`
	ID    string   `json:"id"`
	Mode  string   `json:"mode"`
	Value *Feature `json:"value,omitempty"`
	Shift *Shift   `json:"shift,omitempty"`
}

type EditBatch struct {
	IfRevision *uint64 `json:"if_revision,omitempty"`
	Edits      []Edit  `json:"edits"`
}

func (e Edit) Validate() error {
	if !ValidName(e.Layer) || !ValidName(e.ID) {
		return fmt.Errorf("invalid edit layer or id")
	}
	switch e.Mode {
	case "insert", "replace":
		if e.Value == nil || e.ID != e.Value.ID {
			return fmt.Errorf("%s requires value with matching id", e.Mode)
		}
		if e.Shift != nil {
			return fmt.Errorf("%s does not accept a shift group", e.Mode)
		}
	case "remove":
		if e.Value != nil {
			return fmt.Errorf("remove does not accept a value")
		}
		if e.Shift != nil {
			return fmt.Errorf("remove does not accept a shift group")
		}
	case "shift":
		if e.Value != nil {
			return fmt.Errorf("shift does not accept a value")
		}
		if e.Shift == nil {
			return fmt.Errorf("shift requires a shift group")
		}
		if len(e.Shift.IDs) == 0 {
			return fmt.Errorf("shift group must list at least one id")
		}
		seen := make(map[string]bool, len(e.Shift.IDs))
		for _, id := range e.Shift.IDs {
			if !ValidName(id) {
				return fmt.Errorf("shift group contains invalid id %q", id)
			}
			if seen[id] {
				return fmt.Errorf("shift group lists %q more than once", id)
			}
			seen[id] = true
		}
		if e.Shift.Dx == 0 && e.Shift.Dy == 0 {
			return fmt.Errorf("shift group requires a non-zero translation")
		}
	default:
		return fmt.Errorf("unknown edit mode %q", e.Mode)
	}
	return nil
}

type CatalogImage struct {
	Revision uint64       `json:"revision"`
	Layers   []LayerImage `json:"layers"`
}

type LayerImage struct {
	Definition Definition `json:"definition"`
	Revision   uint64     `json:"revision"`
	Features   []Feature  `json:"features"`
}
