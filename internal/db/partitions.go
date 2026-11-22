package db

import (
	"errors"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

type PartitionQuery struct {
	Bounds plane.Box `json:"bounds"`
}

type PartitionResponse struct {
	ReadStamp
	spatial.PartitionResult
}

// Partitions returns the edge-sharing connected components of an extent layer
// inside the query window, together with the clipped seams proving each
// component's connectivity. The graph is recomputed from the immutable layer
// snapshot, so identical window and layer revision always yield identical
// partitions.
func (d *Database) Partitions(name string, q PartitionQuery) (PartitionResponse, error) {
	if err := validateWindow(q.Bounds, ""); err != nil {
		return PartitionResponse{}, err
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return PartitionResponse{}, err
	}
	if l.definition.Kind != plane.Extent {
		return PartitionResponse{}, invalid("partitions require an extent layer")
	}
	result, err := spatial.SeamPartitions(l.tree, q.Bounds)
	if errors.Is(err, spatial.ErrSeamBudget) {
		return PartitionResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return PartitionResponse{}, err
	}
	return PartitionResponse{ReadStamp: stamp(s, l), PartitionResult: result}, nil
}
