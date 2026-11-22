package db

import (
	"errors"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// maxGridCell keeps every reported cell edge inside int64 even when the grid
// origin and the query window sit at opposite coordinate limits.
const maxGridCell = 2 * plane.CoordinateLimit

type DistributionQuery struct {
	Bounds plane.Box   `json:"bounds"`
	Class  string      `json:"class,omitempty"`
	Origin plane.Point `json:"origin"`
	// Cell is the lattice step in integer units. An omitted cell selects the
	// unit lattice; zero and negative steps are rejected.
	Cell *int64 `json:"cell,omitempty"`
}

type DistributionResponse struct {
	ReadStamp
	spatial.DistributionResult
}

func (d *Database) Distribution(name string, q DistributionQuery) (DistributionResponse, error) {
	if err := validateWindow(q.Bounds, q.Class); err != nil {
		return DistributionResponse{}, err
	}
	if err := q.Origin.Validate(); err != nil {
		return DistributionResponse{}, invalid("%v", err)
	}
	cell := int64(1)
	if q.Cell != nil {
		cell = *q.Cell
	}
	if cell < 1 || cell > maxGridCell {
		return DistributionResponse{}, invalid("grid cell must be 1..%d", int64(maxGridCell))
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return DistributionResponse{}, err
	}
	result, err := spatial.Distribution(l.tree, q.Bounds, q.Class, spatial.DistributionGrid{
		Origin: q.Origin,
		Cell:   cell,
	})
	if errors.Is(err, spatial.ErrDistributionCapacity) {
		return DistributionResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return DistributionResponse{}, err
	}
	return DistributionResponse{ReadStamp: stamp(s, l), DistributionResult: result}, nil
}
