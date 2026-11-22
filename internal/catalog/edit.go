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
	// RequestID makes the batch retryable. Repeating the same identifier with
	// the same content returns the original commit; reusing it with different
	// content is rejected.
	RequestID string `json:"request_id,omitempty"`
	Edits     []Edit `json:"edits"`
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
	// Requests carries the bounded retry ledger. It is optional so images made
	// before the ledger existed, and images exported purely for portability,
	// remain decodable; an absent array is not the same as a present empty one
	// and decodes to nil without changing canonicality.
	Requests []RequestRecord `json:"requests,omitempty"`
}

type LayerImage struct {
	Definition Definition `json:"definition"`
	Revision   uint64     `json:"revision"`
	Features   []Feature  `json:"features"`
}
