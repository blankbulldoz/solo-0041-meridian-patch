package catalog

import "fmt"

type Edit struct {
	Layer string   `json:"layer"`
	ID    string   `json:"id"`
	Mode  string   `json:"mode"`
	Value *Feature `json:"value,omitempty"`
}

// LayerRevision pins one layer to the revision the client last observed.
type LayerRevision struct {
	Layer    string `json:"layer"`
	Revision uint64 `json:"revision"`
}

type EditBatch struct {
	IfRevision       *uint64         `json:"if_revision,omitempty"`
	IfLayerRevisions []LayerRevision `json:"if_layer_revisions,omitempty"`
	Edits            []Edit          `json:"edits"`
}

// ValidateConditions checks the layer-scoped precondition list. A present list
// must name every layer the batch touches; layers outside the batch may be
// listed as extra guards. A nil list means layer conditions are not used.
func (b EditBatch) ValidateConditions() error {
	if b.IfLayerRevisions == nil {
		return nil
	}
	touched := make(map[string]bool)
	for _, edit := range b.Edits {
		touched[edit.Layer] = true
	}
	guarded := make(map[string]bool, len(b.IfLayerRevisions))
	for _, condition := range b.IfLayerRevisions {
		if !ValidName(condition.Layer) {
			return fmt.Errorf("invalid layer condition name %q", condition.Layer)
		}
		if condition.Revision == 0 {
			return fmt.Errorf("layer condition for %q requires a positive revision", condition.Layer)
		}
		if guarded[condition.Layer] {
			return fmt.Errorf("duplicate layer condition for %q", condition.Layer)
		}
		guarded[condition.Layer] = true
	}
	for name := range touched {
		if !guarded[name] {
			return fmt.Errorf("edit touches %q but if_layer_revisions has no condition for it", name)
		}
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
