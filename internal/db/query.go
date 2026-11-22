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
	Stable   bool      `json:"stable,omitempty"`
}

type WindowPage struct {
	ReadStamp
	spatial.MatchSet
	Next string `json:"next,omitempty"`
}

// Window returns one page of features inside the query bounds ordered by id.
// Without a bookmark the first page reads the current view; with Stable set,
// that view is pinned in a bounded session as soon as the page emits its
// continuation bookmark, so later pages keep reading the result the first
// page determined, even if the layer changes meanwhile. Bookmarks continue
// either mode: default bookmarks expire with the layer revision, session
// bookmarks outlive same-layer writes until the session is evicted or
// abandoned. The stable flag only applies to the first page; a bookmark alone
// decides how a continuation is served.
func (d *Database) Window(name string, q WindowQuery) (WindowPage, error) {
	if err := validateWindow(q.Bounds, q.Class); err != nil {
		return WindowPage{}, err
	}
	if q.Limit < 1 || q.Limit > 256 {
		return WindowPage{}, invalid("window limit must be 1..256")
	}
	if q.Bookmark != "" {
		return d.windowNext(name, q)
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return WindowPage{}, err
	}
	scope := bookmark.Scope{Layer: name, Bounds: q.Bounds, Class: q.Class, Revision: l.revision}
	return d.windowPage(s, l, scope, "", q, func() uint64 {
		if q.Stable {
			return d.sessions.open(s)
		}
		return 0
	}), nil
}

func (d *Database) windowNext(name string, q WindowQuery) (WindowPage, error) {
	token, err := d.signer.Open(q.Bookmark)
	if err != nil || token.Scope.Layer != name || token.Scope.Bounds != q.Bounds || token.Scope.Class != q.Class {
		return WindowPage{}, invalid("bookmark does not match the window predicate")
	}
	if token.Session != 0 {
		view, err := d.sessions.view(token.Session)
		if err != nil {
			return WindowPage{}, err
		}
		l, err := view.getLayer(name)
		if err != nil {
			return WindowPage{}, err
		}
		return d.windowPage(view, l, token.Scope, token.After, q, func() uint64 { return token.Session }), nil
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return WindowPage{}, err
	}
	if l.revision != token.Scope.Revision {
		return WindowPage{}, windowExpired("bookmark revision has expired")
	}
	return d.windowPage(s, l, token.Scope, token.After, q, func() uint64 { return 0 }), nil
}

// windowPage serves one page from view s. session is consulted only when the
// page emits a continuation bookmark, and yields the stable session id to
// seal into it, or zero for a default bookmark.
func (d *Database) windowPage(s *state, l *layer, scope bookmark.Scope, after string, q WindowQuery, session func() uint64) WindowPage {
	page := WindowPage{ReadStamp: stamp(s, l), MatchSet: spatial.Window(l.tree, q.Bounds, q.Class, after)}
	if len(page.Features) > q.Limit {
		page.Next = d.signer.Seal(scope, page.Features[q.Limit-1].ID, session())
		page.Features = page.Features[:q.Limit]
	}
	copyFeatures(page.Features)
	return page
}

type AbandonQuery struct {
	Bookmark string `json:"bookmark"`
}

type AbandonResult struct {
	Abandoned bool `json:"abandoned"`
}

// AbandonWindow releases the view pinned by a stable window session. Any
// bookmark sealed for the session identifies it; once abandoned, reuse of the
// session reports window_abandoned rather than window_expired.
func (d *Database) AbandonWindow(name string, q AbandonQuery) (AbandonResult, error) {
	if q.Bookmark == "" {
		return AbandonResult{}, invalid("abandon requires a bookmark")
	}
	token, err := d.signer.Open(q.Bookmark)
	if err != nil || token.Scope.Layer != name {
		return AbandonResult{}, invalid("bookmark does not match the window predicate")
	}
	if token.Session == 0 {
		return AbandonResult{}, invalid("bookmark is not a stable window session")
	}
	if err := d.sessions.abandon(token.Session); err != nil {
		return AbandonResult{}, err
	}
	return AbandonResult{Abandoned: true}, nil
}

type LocateResult struct {
	ReadStamp
	spatial.MatchSet
}

func (d *Database) Locate(name string, p plane.Point) (LocateResult, error) {
	if err := p.Validate(); err != nil {
		return LocateResult{}, invalid("%v", err)
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return LocateResult{}, err
	}
	result := spatial.Locate(l.tree, p)
	copyFeatures(result.Features)
	return LocateResult{ReadStamp: stamp(s, l), MatchSet: result}, nil
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
