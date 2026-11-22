package db

import (
	"fmt"
	"maps"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// QuotaDetails identifies the quota that rejected a commit together with the
// union area measured for the rejected final state.
type QuotaDetails struct {
	Quota string `json:"quota"`
	Area  int64  `json:"area"`
	Cap   int64  `json:"cap"`
}

func exceeded(quota catalog.Quota, area int64) *Error {
	return &Error{
		Code:    "capacity",
		Message: fmt.Sprintf("quota %q coverage %d exceeds cap %d", quota.Name, area, quota.MaxArea),
		Details: QuotaDetails{Quota: quota.Name, Area: area, Cap: quota.MaxArea},
	}
}

// quotaArea measures the union coverage of the quota layers inside the quota
// bounds. Quota layers are always extent layers, enforced at registration and
// image load, so every feature carries a rectangle.
func (s *state) quotaArea(quota catalog.Quota) int64 {
	rectangles := make([]plane.Box, 0)
	for _, name := range quota.Layers {
		s.layers[name].tree.Search(quota.Bounds, func(row catalog.Feature) bool {
			if b, present := quota.Bounds.Clip(*row.Shape.Extent); present {
				rectangles = append(rectangles, b)
			}
			return true
		})
	}
	return spatial.UnionArea(rectangles)
}

// checkQuotas verifies every quota that observes at least one of the changed
// layers. Quotas are checked in name order so a rejection is deterministic
// when several caps are exceeded by the same commit.
func (s *state) checkQuotas(changed []string) error {
	for _, name := range s.quotaNames() {
		quota := s.quotas[name]
		touched := false
		for _, layer := range quota.Layers {
			if slices.Contains(changed, layer) {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		if area := s.quotaArea(quota); area > quota.MaxArea {
			return exceeded(quota, area)
		}
	}
	return nil
}

type QuotaStatus struct {
	catalog.Quota
	Used int64 `json:"used"`
}

type QuotaView struct {
	Revision uint64        `json:"revision"`
	Quotas   []QuotaStatus `json:"quotas"`
}

func (d *Database) Quotas() QuotaView {
	s := d.head.Load()
	view := QuotaView{Revision: s.revision, Quotas: make([]QuotaStatus, 0, len(s.quotas))}
	for _, name := range s.quotaNames() {
		quota := s.quotas[name]
		view.Quotas = append(view.Quotas, QuotaStatus{Quota: quota, Used: s.quotaArea(quota)})
	}
	return view
}

type QuotaCommit struct {
	Revision uint64         `json:"revision"`
	Quota    *catalog.Quota `json:"quota,omitempty"`
}

func (d *Database) RegisterQuota(quota catalog.Quota, expected *uint64) (QuotaCommit, error) {
	if err := quota.Validate(); err != nil {
		return QuotaCommit{}, invalid("%v", err)
	}
	quota.Layers = slices.Clone(quota.Layers)
	slices.Sort(quota.Layers)
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		if _, present := current.quotas[quota.Name]; present {
			return nil, conflict("quota %q already exists", quota.Name)
		}
		if len(current.quotas) == catalog.MaxQuotas {
			return nil, exhausted("quota capacity reached")
		}
		for _, name := range quota.Layers {
			l, err := current.getLayer(name)
			if err != nil {
				return nil, err
			}
			if l.definition.Kind != plane.Extent {
				return nil, invalid("quota %q layer %q is not an extent layer", quota.Name, name)
			}
		}
		next := &state{revision: revision, layers: current.layers, quotas: maps.Clone(current.quotas)}
		next.quotas[quota.Name] = quota
		if area := next.quotaArea(quota); area > quota.MaxArea {
			return nil, exceeded(quota, area)
		}
		return next, nil
	})
	if err != nil {
		return QuotaCommit{}, err
	}
	return QuotaCommit{Revision: s.revision, Quota: &quota}, nil
}

func (d *Database) RemoveQuota(name string, expected *uint64) (QuotaCommit, error) {
	if !catalog.ValidName(name) {
		return QuotaCommit{}, invalid("invalid quota name %q", name)
	}
	s, err := d.commit(expected, func(current *state, revision uint64) (*state, error) {
		if _, present := current.quotas[name]; !present {
			return nil, missing("quota %q does not exist", name)
		}
		next := &state{revision: revision, layers: current.layers, quotas: maps.Clone(current.quotas)}
		delete(next.quotas, name)
		return next, nil
	})
	if err != nil {
		return QuotaCommit{}, err
	}
	return QuotaCommit{Revision: s.revision}, nil
}
