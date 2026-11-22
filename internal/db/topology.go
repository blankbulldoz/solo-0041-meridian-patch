package db

import (
	"errors"
	"fmt"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

type SeamQuery struct {
	Bounds plane.Box `json:"bounds"`
	Limit  int       `json:"limit"`
}

type SeamResponse struct {
	ReadStamp
	spatial.SeamResult
}

func (d *Database) Seams(name string, q SeamQuery) (SeamResponse, error) {
	if err := validateWindow(q.Bounds, ""); err != nil {
		return SeamResponse{}, err
	}
	if q.Limit < 1 || q.Limit > spatial.MaxPairs {
		return SeamResponse{}, invalid("seam limit must be 1..%d", spatial.MaxPairs)
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return SeamResponse{}, err
	}
	if l.definition.Kind != plane.Extent {
		return SeamResponse{}, invalid("seams require an extent layer")
	}
	result, err := spatial.Seams(l.tree, q.Bounds, q.Limit)
	if errors.Is(err, spatial.ErrSeamBudget) {
		return SeamResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return SeamResponse{}, err
	}
	return SeamResponse{ReadStamp: stamp(s, l), SeamResult: result}, nil
}

type NearestSeamQuery struct {
	Bounds plane.Box   `json:"bounds"`
	Point  plane.Point `json:"point"`
}

type NearestSeamResponse struct {
	ReadStamp
	Found bool             `json:"found"`
	Hit   *spatial.SeamHit `json:"hit,omitempty"`
	Visit packed.Visit     `json:"visit"`
}

func (d *Database) NearestSeam(name string, q NearestSeamQuery) (NearestSeamResponse, error) {
	if err := validateWindow(q.Bounds, ""); err != nil {
		return NearestSeamResponse{}, err
	}
	if err := q.Point.Validate(); err != nil {
		return NearestSeamResponse{}, invalid("%v", err)
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return NearestSeamResponse{}, err
	}
	if l.definition.Kind != plane.Extent {
		return NearestSeamResponse{}, invalid("nearest seam requires an extent layer")
	}
	hit, visit, err := spatial.NearestSeam(l.tree, q.Bounds, q.Point)
	if errors.Is(err, spatial.ErrSeamBudget) {
		return NearestSeamResponse{}, exhausted("%v", err)
	}
	if err != nil {
		return NearestSeamResponse{}, err
	}
	return NearestSeamResponse{ReadStamp: stamp(s, l), Found: hit != nil, Hit: hit, Visit: visit}, nil
}

type IndexAudit struct {
	ReadStamp
	packed.Audit
}

func (d *Database) Audit(name string) (IndexAudit, error) {
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return IndexAudit{}, err
	}
	result, err := l.tree.Audit(l.rows)
	if err != nil {
		return IndexAudit{}, fmt.Errorf("index audit for layer %q at revision %d: %w", name, l.revision, err)
	}
	return IndexAudit{ReadStamp: stamp(s, l), Audit: result}, nil
}
