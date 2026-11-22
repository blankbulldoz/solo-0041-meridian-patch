package catalog

import "fmt"

type Edit struct {
	Layer string   `json:"layer"`
	ID    string   `json:"id"`
	Mode  string   `json:"mode"`
	Value *Feature `json:"value,omitempty"`
}

type EditBatch struct {
	IfRevision *uint64 `json:"if_revision,omitempty"`
	Edits      []Edit  `json:"edits"`
}

// FeatureEdit is an edit scoped to the layer named by the enclosing request,
// so a layout change can carry replacements and removals for the features
// affected by the new bounds in one atomic change.
type FeatureEdit struct {
	ID    string   `json:"id"`
	Mode  string   `json:"mode"`
	Value *Feature `json:"value,omitempty"`
}

func (e FeatureEdit) Validate() error {
	if !ValidName(e.ID) {
		return fmt.Errorf("invalid edit id")
	}
	switch e.Mode {
	case "insert", "replace":
		if e.Value == nil || e.ID != e.Value.ID {
			return fmt.Errorf("%s requires value with matching id", e.Mode)
		}
	case "remove":
		if e.Value != nil {
			return fmt.Errorf("remove does not accept a value")
		}
	default:
		return fmt.Errorf("unknown edit mode %q", e.Mode)
	}
	return nil
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
	case "remove":
		if e.Value != nil {
			return fmt.Errorf("remove does not accept a value")
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
