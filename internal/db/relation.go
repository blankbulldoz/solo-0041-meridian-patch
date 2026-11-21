package db

import (
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

type FeatureKey struct {
	Layer string `json:"layer"`
	ID    string `json:"id"`
}

func (s *state) resolve(key FeatureKey) (catalog.Feature, uint64, error) {
	if !catalog.ValidName(key.Layer) || !catalog.ValidName(key.ID) {
		return catalog.Feature{}, 0, invalid("relation requires valid layer and feature names")
	}
	l, err := s.getLayer(key.Layer)
	if err != nil {
		return catalog.Feature{}, 0, err
	}
	row, exists := l.rows[key.ID]
	if !exists {
		return catalog.Feature{}, 0, missing("feature %q does not exist in layer %q", key.ID, key.Layer)
	}
	return row, l.revision, nil
}

type RelationQuery struct {
	Left  FeatureKey `json:"left"`
	Right FeatureKey `json:"right"`
}

type RelationResult struct {
	Revision      uint64 `json:"revision"`
	LeftRevision  uint64 `json:"left_revision"`
	RightRevision uint64 `json:"right_revision"`
	plane.Relation
}

func (d *Database) Relate(q RelationQuery) (RelationResult, error) {
	s := d.head.Load()
	left, leftRevision, err := s.resolve(q.Left)
	if err != nil {
		return RelationResult{}, err
	}
	right, rightRevision, err := s.resolve(q.Right)
	if err != nil {
		return RelationResult{}, err
	}
	return RelationResult{
		Revision: s.revision, LeftRevision: leftRevision, RightRevision: rightRevision,
		Relation: plane.Relate(left.Shape, right.Shape),
	}, nil
}
