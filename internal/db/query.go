package db

import (
	"fmt"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/bookmark"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

type ReadStamp struct {
	Revision      uint64 `json:"revision"`
	LayerRevision uint64 `json:"layer_revision"`
}

type WindowQuery struct {
	Bounds   plane.Box `json:"bounds"`
	Class    string    `json:"class,omitempty"`
	Limit    int       `json:"limit"`
	Bookmark string    `json:"bookmark,omitempty"`
}

type WindowPage struct {
	ReadStamp
	spatial.MatchSet
	Next string `json:"next,omitempty"`
}

func (d *Database) Window(name string, q WindowQuery) (WindowPage, error) {
	if err := validateWindow(q.Bounds, q.Class); err != nil {
		return WindowPage{}, err
	}
	if q.Limit < 1 || q.Limit > 256 {
		return WindowPage{}, invalid("window limit must be 1..256")
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return WindowPage{}, err
	}
	scope := bookmark.Scope{Layer: name, Bounds: q.Bounds, Class: q.Class, Revision: l.revision}
	var after string
	if q.Bookmark != "" {
		previous, from, err := d.signer.Open(q.Bookmark)
		if err != nil || previous.Layer != scope.Layer || previous.Bounds != scope.Bounds || previous.Class != scope.Class {
			return WindowPage{}, invalid("bookmark does not match the window predicate")
		}
		if previous.Revision != scope.Revision {
			return WindowPage{}, conflict("bookmark revision has expired")
		}
		after = from
	}
	page := WindowPage{ReadStamp: stamp(s, l), MatchSet: spatial.Window(l.tree, q.Bounds, q.Class, after)}
	if len(page.Features) > q.Limit {
		page.Next = d.signer.Seal(scope, page.Features[q.Limit-1].ID)
		page.Features = page.Features[:q.Limit]
	}
	copyFeatures(page.Features)
	return page, nil
}

// LocateQuery accepts either a single point through the promoted x/y members
// or a batch of sample points. Limit caps the total features returned across
// all groups; zero selects the default budget.
type LocateQuery struct {
	plane.Point
	Points []plane.Point `json:"points,omitempty"`
	Limit  int           `json:"limit,omitempty"`
}

func (q LocateQuery) Batch() bool { return q.Points != nil }

func locateBudget(limit int) (int, error) {
	switch {
	case limit < 0 || limit > spatial.MaxLocateResults:
		return 0, invalid("locate limit must be 0..%d", spatial.MaxLocateResults)
	case limit == 0:
		return spatial.MaxLocateResults, nil
	default:
		return limit, nil
	}
}

type LocateResult struct {
	ReadStamp
	spatial.MatchSet
}

func (d *Database) Locate(name string, q LocateQuery) (LocateResult, error) {
	if err := q.Point.Validate(); err != nil {
		return LocateResult{}, invalid("%v", err)
	}
	budget, err := locateBudget(q.Limit)
	if err != nil {
		return LocateResult{}, err
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return LocateResult{}, err
	}
	result := spatial.Locate(l.tree, q.Point)
	if len(result.Features) > budget {
		return LocateResult{}, exhausted("locate result budget %d exceeded", budget)
	}
	copyFeatures(result.Features)
	return LocateResult{ReadStamp: stamp(s, l), MatchSet: result}, nil
}

type LocateGroup struct {
	Features []catalog.Feature `json:"features"`
	Visit    packed.Visit      `json:"visit"`
}

type LocateBatchResult struct {
	ReadStamp
	Groups []LocateGroup `json:"groups"`
}

func (d *Database) LocateBatch(name string, q LocateQuery) (LocateBatchResult, error) {
	if q.Point.X != 0 || q.Point.Y != 0 {
		return LocateBatchResult{}, invalid("locate accepts either a point or points, not both")
	}
	if len(q.Points) == 0 {
		return LocateBatchResult{}, invalid("locate requires at least one point")
	}
	if len(q.Points) > spatial.MaxLocatePoints {
		return LocateBatchResult{}, invalid("locate accepts at most %d points", spatial.MaxLocatePoints)
	}
	budget, err := locateBudget(q.Limit)
	if err != nil {
		return LocateBatchResult{}, err
	}
	for i, p := range q.Points {
		if err := p.Validate(); err != nil {
			return LocateBatchResult{}, invalid("points[%d]: %v", i, err)
		}
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return LocateBatchResult{}, err
	}
	result := LocateBatchResult{ReadStamp: stamp(s, l), Groups: make([]LocateGroup, 0, len(q.Points))}
	total := 0
	for i, p := range q.Points {
		match := spatial.Locate(l.tree, p)
		total += len(match.Features)
		if total > budget {
			return LocateBatchResult{}, exhausted("locate result budget %d exceeded at points[%d]", budget, i)
		}
		copyFeatures(match.Features)
		result.Groups = append(result.Groups, LocateGroup{Features: match.Features, Visit: match.Visit})
	}
	return result, nil
}

type NearestQuery struct {
	Point plane.Point `json:"point"`
	Class string      `json:"class,omitempty"`
	Limit int         `json:"limit"`
}

type NearestResult struct {
	ReadStamp
	Neighbors []packed.Neighbor `json:"neighbors"`
	Visit     packed.Visit      `json:"visit"`
}

func (d *Database) Nearest(name string, q NearestQuery) (NearestResult, error) {
	if err := q.Point.Validate(); err != nil {
		return NearestResult{}, invalid("%v", err)
	}
	if q.Limit < 1 || q.Limit > 128 || (q.Class != "" && !catalog.ValidName(q.Class)) {
		return NearestResult{}, invalid("invalid nearest limit or class")
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return NearestResult{}, err
	}
	neighbors, visit := l.tree.Nearest(q.Point, q.Limit, q.Class)
	for i := range neighbors {
		neighbors[i].Feature = neighbors[i].Feature.Copy()
	}
	return NearestResult{ReadStamp: stamp(s, l), Neighbors: neighbors, Visit: visit}, nil
}

func stamp(s *state, l *layer) ReadStamp {
	return ReadStamp{Revision: s.revision, LayerRevision: l.revision}
}

func copyFeatures(rows []catalog.Feature) {
	for i := range rows {
		rows[i] = rows[i].Copy()
	}
}

func validateWindow(bounds plane.Box, class string) error {
	if err := bounds.Validate(); err != nil {
		return invalid("%v", err)
	}
	if class != "" && !catalog.ValidName(class) {
		return invalid("invalid class %q", class)
	}
	return nil
}

type Inspection struct {
	ReadStamp
	Layer catalog.Summary `json:"layer"`
	Tree  packed.Stats    `json:"tree"`
}

func (d *Database) Inspect(name string) (Inspection, error) {
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{ReadStamp: stamp(s, l), Layer: l.summary(), Tree: l.tree.Stats()}, nil
}

type JoinQuery struct {
	Left   string    `json:"left"`
	Right  string    `json:"right"`
	Bounds plane.Box `json:"bounds"`
	Limit  int       `json:"limit"`
}

type JoinResponse struct {
	Revision      uint64 `json:"revision"`
	LeftRevision  uint64 `json:"left_revision"`
	RightRevision uint64 `json:"right_revision"`
	spatial.JoinResult
}

func (d *Database) Join(q JoinQuery) (JoinResponse, error) {
	if q.Left == q.Right || q.Limit < 1 || q.Limit > spatial.MaxPairs {
		return JoinResponse{}, invalid("join requires distinct layers and a limit of 1..%d", spatial.MaxPairs)
	}
	if err := validateWindow(q.Bounds, ""); err != nil {
		return JoinResponse{}, err
	}
	s := d.head.Load()
	left, err := s.getLayer(q.Left)
	if err != nil {
		return JoinResponse{}, err
	}
	right, err := s.getLayer(q.Right)
	if err != nil {
		return JoinResponse{}, err
	}
	result, err := spatial.Join(left.tree, right.tree, q.Bounds, q.Limit)
	if err == spatial.ErrJoinBudget {
		return JoinResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return JoinResponse{}, fmt.Errorf("join: %w", err)
	}
	return JoinResponse{Revision: s.revision, LeftRevision: left.revision, RightRevision: right.revision, JoinResult: result}, nil
}

type CoverageQuery struct {
	Bounds plane.Box `json:"bounds"`
	Class  string    `json:"class,omitempty"`
}

type CoverageResponse struct {
	ReadStamp
	spatial.AreaResult
}

func (d *Database) Coverage(name string, q CoverageQuery) (CoverageResponse, error) {
	if err := validateWindow(q.Bounds, q.Class); err != nil {
		return CoverageResponse{}, err
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return CoverageResponse{}, err
	}
	if l.definition.Kind != plane.Extent {
		return CoverageResponse{}, invalid("coverage requires an extent layer")
	}
	return CoverageResponse{ReadStamp: stamp(s, l), AreaResult: spatial.Coverage(l.tree, q.Bounds, q.Class)}, nil
}
