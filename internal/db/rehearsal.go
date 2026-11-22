package db

import (
	"errors"
	"fmt"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// Rehearse evaluates an edit batch without persisting it. The batch is staged
// through the same final-state machinery used by a durable commit, then the
// supplied probes are answered against both the baseline and the candidate
// state. The published head is never replaced and no revision is consumed.
type RehearsalProbe struct {
	Name   string       `json:"name,omitempty"`
	Kind   string       `json:"kind"`
	Layer  string       `json:"layer,omitempty"`
	Left   string       `json:"left_layer,omitempty"`
	Right  string       `json:"right_layer,omitempty"`
	Bounds *plane.Box   `json:"bounds,omitempty"`
	Point  *plane.Point `json:"point,omitempty"`
	Class  string       `json:"class,omitempty"`
	Limit  int          `json:"limit,omitempty"`
}

type RehearsalRequest struct {
	IfRevision *uint64          `json:"if_revision,omitempty"`
	Edits      []catalog.Edit   `json:"edits"`
	Probes     []RehearsalProbe `json:"probes,omitempty"`
}

// HitReport captures how one probe window changes if the batch were applied.
// Visit describes the candidate (projected) evaluation, matching the index
// cost a post-commit read would pay.
type HitReport struct {
	Before  []string     `json:"before"`
	After   []string     `json:"after"`
	Added   []string     `json:"added"`
	Removed []string     `json:"removed"`
	Count   int          `json:"count"`
	Visit   packed.Visit `json:"visit"`
}

type AreaReport struct {
	Before int64 `json:"before"`
	After  int64 `json:"after"`
	Delta  int64 `json:"delta"`
}

type ProbeResult struct {
	Name      string      `json:"name,omitempty"`
	Kind      string      `json:"kind"`
	Layer     string      `json:"layer,omitempty"`
	Hits      *HitReport  `json:"hits,omitempty"`
	Covered   *AreaReport `json:"covered,omitempty"`
	Repeated  *AreaReport `json:"repeated,omitempty"`
	Uncovered *AreaReport `json:"uncovered,omitempty"`
}

type RehearsalResult struct {
	OK                bool              `json:"ok"`
	BaselineRevision  uint64            `json:"baseline_revision"`
	ProjectedRevision uint64            `json:"projected_revision"`
	Affected          []catalog.Summary `json:"affected"`
	Probes            []ProbeResult     `json:"probes"`
}

// Rehearse never writes. It reports the exact constraint verdict a matching
// POST /edits would produce, together with the projected probe outcomes.
func (d *Database) Rehearse(req RehearsalRequest) (RehearsalResult, error) {
	if len(req.Edits) == 0 || len(req.Edits) > catalog.MaxEdits {
		return RehearsalResult{}, invalid("edit batch must contain 1..%d operations", catalog.MaxEdits)
	}
	if len(req.Probes) > catalog.MaxRehearsalProbes {
		return RehearsalResult{}, invalid("a rehearsal accepts at most %d probes", catalog.MaxRehearsalProbes)
	}
	current := d.head.Load()
	if req.IfRevision != nil && *req.IfRevision != current.revision {
		return RehearsalResult{}, conflict("expected database revision %d, current revision is %d", *req.IfRevision, current.revision)
	}
	if current.revision == maxRevision {
		return RehearsalResult{}, exhausted("database revision space is exhausted")
	}
	projected := current.revision + 1

	candidate, changed, err := stageEdits(current, projected, req.Edits)
	if err != nil {
		// The pack fails the same final-state rules a real POST /edits would
		// enforce. The error carries the identical code (invalid/missing/
		// conflict/capacity), and nothing is ever published.
		return RehearsalResult{}, err
	}

	results := make([]ProbeResult, 0, len(req.Probes))
	for i, probe := range req.Probes {
		result, err := runProbe(current, candidate, probe)
		if err != nil {
			// Preserve the database error code (missing, capacity, ...) so a
			// probe fails with the same status as the read endpoint it mirrors.
			var problem *Error
			if errors.As(err, &problem) {
				return RehearsalResult{}, fmt.Errorf("probe %d (%s): %w", i, probe.Kind, err)
			}
			return RehearsalResult{}, invalid("probe %d (%s): %v", i, probe.Kind, err)
		}
		results = append(results, result)
	}

	return RehearsalResult{
		OK:                true,
		BaselineRevision:  current.revision,
		ProjectedRevision: projected,
		Affected:          summaries(candidate, changed).Layers,
		Probes:            results,
	}, nil
}

const maxRevision = ^uint64(0)

// runProbe answers the same query against the baseline and the candidate and
// diffs the outcomes. Query semantics match the public read endpoints, so the
// projected figures are exactly what a post-commit read would return.
func runProbe(baseline, candidate *state, probe RehearsalProbe) (ProbeResult, error) {
	out := ProbeResult{Name: probe.Name, Kind: probe.Kind, Layer: probe.Layer}
	switch probe.Kind {
	case "window":
		bounds, err := requireBounds(probe)
		if err != nil {
			return ProbeResult{}, err
		}
		if err := validateWindow(*bounds, probe.Class); err != nil {
			return ProbeResult{}, err
		}
		before, err := windowHits(baseline, probe.Layer, *bounds, probe.Class)
		if err != nil {
			return ProbeResult{}, err
		}
		after, err := windowHits(candidate, probe.Layer, *bounds, probe.Class)
		if err != nil {
			return ProbeResult{}, err
		}
		out.Hits = diffHits(before, after)
	case "locate":
		point, err := requirePoint(probe)
		if err != nil {
			return ProbeResult{}, err
		}
		if err := point.Validate(); err != nil {
			return ProbeResult{}, err
		}
		before, err := locateHits(baseline, probe.Layer, *point)
		if err != nil {
			return ProbeResult{}, err
		}
		after, err := locateHits(candidate, probe.Layer, *point)
		if err != nil {
			return ProbeResult{}, err
		}
		out.Hits = diffHits(before, after)
	case "nearest":
		point, err := requirePoint(probe)
		if err != nil {
			return ProbeResult{}, err
		}
		if err := point.Validate(); err != nil {
			return ProbeResult{}, err
		}
		if probe.Limit < 1 || probe.Limit > 128 || (probe.Class != "" && !catalog.ValidName(probe.Class)) {
			return ProbeResult{}, fmt.Errorf("invalid nearest limit or class")
		}
		before, err := nearestHits(baseline, probe.Layer, *point, probe.Limit, probe.Class)
		if err != nil {
			return ProbeResult{}, err
		}
		after, err := nearestHits(candidate, probe.Layer, *point, probe.Limit, probe.Class)
		if err != nil {
			return ProbeResult{}, err
		}
		out.Hits = diffHits(before, after)
	case "coverage":
		bounds, err := requireBounds(probe)
		if err != nil {
			return ProbeResult{}, err
		}
		if err := validateWindow(*bounds, probe.Class); err != nil {
			return ProbeResult{}, err
		}
		if err := requireKind(baseline, candidate, probe.Layer, plane.Extent); err != nil {
			return ProbeResult{}, err
		}
		before, err := coverageAreas(baseline, probe.Layer, *bounds, probe.Class)
		if err != nil {
			return ProbeResult{}, err
		}
		after, err := coverageAreas(candidate, probe.Layer, *bounds, probe.Class)
		if err != nil {
			return ProbeResult{}, err
		}
		out.Covered = areaReport(before.covered, after.covered)
		out.Repeated = areaReport(before.repeated, after.repeated)
		out.Uncovered = areaReport(before.uncovered, after.uncovered)
	case "seams":
		bounds, err := requireBounds(probe)
		if err != nil {
			return ProbeResult{}, err
		}
		if err := validateWindow(*bounds, ""); err != nil {
			return ProbeResult{}, err
		}
		if probe.Limit < 1 || probe.Limit > spatial.MaxPairs {
			return ProbeResult{}, fmt.Errorf("seam limit must be 1..%d", spatial.MaxPairs)
		}
		if err := requireKind(baseline, candidate, probe.Layer, plane.Extent); err != nil {
			return ProbeResult{}, err
		}
		before, err := seamHits(baseline, probe.Layer, *bounds, probe.Limit)
		if err != nil {
			return ProbeResult{}, err
		}
		after, err := seamHits(candidate, probe.Layer, *bounds, probe.Limit)
		if err != nil {
			return ProbeResult{}, err
		}
		out.Hits = diffHits(before, after)
	case "join":
		bounds, err := requireBounds(probe)
		if err != nil {
			return ProbeResult{}, err
		}
		if probe.Left == probe.Right || probe.Limit < 1 || probe.Limit > spatial.MaxPairs {
			return ProbeResult{}, fmt.Errorf("join requires distinct layers and a limit of 1..%d", spatial.MaxPairs)
		}
		if err := validateWindow(*bounds, ""); err != nil {
			return ProbeResult{}, err
		}
		before, err := joinHits(baseline, probe.Left, probe.Right, *bounds, probe.Limit)
		if err != nil {
			return ProbeResult{}, err
		}
		after, err := joinHits(candidate, probe.Left, probe.Right, *bounds, probe.Limit)
		if err != nil {
			return ProbeResult{}, err
		}
		out.Hits = diffHits(before, after)
	default:
		return ProbeResult{}, fmt.Errorf("unknown probe kind %q", probe.Kind)
	}
	return out, nil
}

func requireBounds(p RehearsalProbe) (*plane.Box, error) {
	if p.Bounds == nil {
		return nil, fmt.Errorf("%s probe requires bounds", p.Kind)
	}
	return p.Bounds, nil
}

func requirePoint(p RehearsalProbe) (*plane.Point, error) {
	if p.Point == nil {
		return nil, fmt.Errorf("%s probe requires a point", p.Kind)
	}
	return p.Point, nil
}

// requireKind makes sure the probed layer keeps the required geometric kind.
// Layers cannot change kind, so the baseline check pins the semantics while
// the candidate check yields a clear error if a layer is somehow absent.
func requireKind(baseline, candidate *state, name string, kind plane.Kind) error {
	l, err := baseline.getLayer(name)
	if err != nil {
		return err
	}
	if l.definition.Kind != kind {
		return fmt.Errorf("probe requires a %s layer", kind)
	}
	if _, err := candidate.getLayer(name); err != nil {
		return err
	}
	return nil
}

type hitSet struct {
	ids   []string
	visit packed.Visit
}

type areaSet struct {
	covered, repeated, uncovered int64
}

func windowHits(s *state, name string, bounds plane.Box, class string) (hitSet, error) {
	l, err := s.getLayer(name)
	if err != nil {
		return hitSet{}, err
	}
	match := spatial.Window(l.tree, bounds, class, "")
	return hitSet{ids: featureIDs(match.Features), visit: match.Visit}, nil
}

func locateHits(s *state, name string, p plane.Point) (hitSet, error) {
	l, err := s.getLayer(name)
	if err != nil {
		return hitSet{}, err
	}
	match := spatial.Locate(l.tree, p)
	return hitSet{ids: featureIDs(match.Features), visit: match.Visit}, nil
}

func nearestHits(s *state, name string, p plane.Point, limit int, class string) (hitSet, error) {
	l, err := s.getLayer(name)
	if err != nil {
		return hitSet{}, err
	}
	neighbors, visit := l.tree.Nearest(p, limit, class)
	ids := make([]string, 0, len(neighbors))
	for _, n := range neighbors {
		ids = append(ids, n.Feature.ID)
	}
	return hitSet{ids: ids, visit: visit}, nil
}

func coverageAreas(s *state, name string, bounds plane.Box, class string) (areaSet, error) {
	l, err := s.getLayer(name)
	if err != nil {
		return areaSet{}, err
	}
	area := spatial.Coverage(l.tree, bounds, class)
	return areaSet{covered: area.Covered, repeated: area.Repeated, uncovered: area.Uncovered}, nil
}

func seamHits(s *state, name string, bounds plane.Box, limit int) (hitSet, error) {
	l, err := s.getLayer(name)
	if err != nil {
		return hitSet{}, err
	}
	result, err := spatial.Seams(l.tree, bounds, limit)
	if err != nil {
		if errors.Is(err, spatial.ErrSeamBudget) {
			return hitSet{}, exhausted("%v", err)
		}
		return hitSet{}, err
	}
	ids := make([]string, 0, len(result.Seams))
	for _, seam := range result.Seams {
		ids = append(ids, seam.First+"|"+seam.Second)
	}
	return hitSet{ids: ids, visit: result.Visit}, nil
}

func joinHits(s *state, leftName, rightName string, bounds plane.Box, limit int) (hitSet, error) {
	left, err := s.getLayer(leftName)
	if err != nil {
		return hitSet{}, err
	}
	right, err := s.getLayer(rightName)
	if err != nil {
		return hitSet{}, err
	}
	result, err := spatial.Join(left.tree, right.tree, bounds, limit)
	if err != nil {
		if errors.Is(err, spatial.ErrJoinBudget) {
			return hitSet{}, exhausted("%v", err)
		}
		return hitSet{}, err
	}
	ids := make([]string, 0, len(result.Pairs))
	for _, pair := range result.Pairs {
		ids = append(ids, pair.Left+"|"+pair.Right)
	}
	return hitSet{ids: ids, visit: result.Visit}, nil
}

func featureIDs(rows []catalog.Feature) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// diffHits compares two ordered hit sets. The reported visit statistics come
// from the candidate (projected) evaluation.
func diffHits(before, after hitSet) *HitReport {
	return &HitReport{
		Before:  before.ids,
		After:   after.ids,
		Added:   idDifference(after.ids, before.ids),
		Removed: idDifference(before.ids, after.ids),
		Count:   len(after.ids),
		Visit:   after.visit,
	}
}

// idDifference returns the IDs present in set but absent from other,
// preserving set's order. Hit order is query-defined (ID order for windows,
// distance order for nearest neighbors), so membership uses a map rather than
// assuming a sorted layout.
func idDifference(set, other []string) []string {
	out := make([]string, 0)
	known := make(map[string]struct{}, len(other))
	for _, id := range other {
		known[id] = struct{}{}
	}
	for _, id := range set {
		if _, found := known[id]; !found {
			out = append(out, id)
		}
	}
	return out
}

func areaReport(before, after int64) *AreaReport {
	return &AreaReport{Before: before, After: after, Delta: after - before}
}
