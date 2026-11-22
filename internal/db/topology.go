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

// IndexWindowAudit keeps the whole-layer field check (records and checked
// nodes) and adds an independent identity comparison inside one small window.
type IndexWindowAudit struct {
	ReadStamp
	packed.Audit
	Window *packed.WindowAudit `json:"window,omitempty"`
}

func (d *Database) AuditWindow(name string, bounds plane.Box) (IndexWindowAudit, error) {
	if err := bounds.Validate(); err != nil {
		return IndexWindowAudit{}, invalid("%v", err)
	}
	s := d.head.Load()
	l, err := s.getLayer(name)
	if err != nil {
		return IndexWindowAudit{}, err
	}
	full, err := l.tree.Audit(l.rows)
	if err != nil {
		return IndexWindowAudit{}, fmt.Errorf("index audit for layer %q at revision %d: %w", name, l.revision, err)
	}
	window, err := l.tree.AuditWindow(bounds, l.rows)
	if err != nil {
		return IndexWindowAudit{}, fmt.Errorf("index window audit for layer %q at revision %d: %w", name, l.revision, err)
	}
	return IndexWindowAudit{ReadStamp: stamp(s, l), Audit: full, Window: &window}, nil
}
