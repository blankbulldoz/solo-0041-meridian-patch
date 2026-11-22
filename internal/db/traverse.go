package db

import (
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// MaxPolylinePoints bounds a single traversal request. Long diagonal lines
// stay cheap because only the endpoints matter, while short repeated walks are
// bounded by the number of vertices.
const MaxPolylinePoints = 4096

type TraverseQuery struct {
	Points []plane.Point `json:"points"`
	Class  string        `json:"class,omitempty"`
}

type TraverseResponse struct {
	ReadStamp
	spatial.Traversal
}

func (d *Database) Traverse(name string, q TraverseQuery) (TraverseResponse, error) {
	if len(q.Points) < 2 {
		return TraverseResponse{}, invalid("traversal requires at least two points")
	}
	if len(q.Points) > MaxPolylinePoints {
		return TraverseResponse{}, invalid("traversal accepts at most %d points", MaxPolylinePoints)
	}
	for _, p := range q.Points {
		if err := p.Validate(); err != nil {
			return TraverseResponse{}, invalid("%v", err)
		}
	}
	for i := 1; i < len(q.Points); i++ {
		if q.Points[i] == q.Points[i-1] {
			return TraverseResponse{}, invalid("traversal points must be distinct when consecutive")
		}
	}
	if q.Class != "" && !catalog.ValidName(q.Class) {
		return TraverseResponse{}, invalid("invalid class %q", q.Class)
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return TraverseResponse{}, err
	}
	if l.definition.Kind != plane.Extent {
		return TraverseResponse{}, invalid("traversal requires an extent layer")
	}
	result, err := spatial.Traverse(l.tree, q.Points, q.Class)
	if err == spatial.ErrTraversalBudget {
		return TraverseResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return TraverseResponse{}, err
	}
	return TraverseResponse{ReadStamp: stamp(s, l), Traversal: result}, nil
}
