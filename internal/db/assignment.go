package db

import (
	"math"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/assign"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// AssignmentMarker is a construction marker and its inclusive feasible radius.
type AssignmentMarker struct {
	ID     string      `json:"id"`
	Point  plane.Point `json:"point"`
	Radius int64       `json:"radius"`
}

// AssignmentCandidate is an integer coordinate that may host one marker.
type AssignmentCandidate struct {
	ID    string      `json:"id"`
	Point plane.Point `json:"point"`
}

// AssignmentQuery is a self-contained, read-only placement request. Occupied
// coordinates are excluded from every marker's candidate set.
type AssignmentQuery struct {
	Markers    []AssignmentMarker    `json:"markers"`
	Candidates []AssignmentCandidate `json:"candidates"`
	Occupied   []plane.Point         `json:"occupied,omitempty"`
}

// AssignmentPlacement is one realized assignment.
type AssignmentPlacement struct {
	MarkerID        string      `json:"marker_id"`
	CandidateID     string      `json:"candidate_id"`
	Point           plane.Point `json:"point"`
	DistanceSquared int64       `json:"distance_squared"`
}

// AssignmentResult carries the optimum and an explicit unassigned list.
type AssignmentResult struct {
	Assigned         int                   `json:"assigned"`
	Unassigned       []string              `json:"unassigned"`
	Placements       []AssignmentPlacement `json:"placements"`
	TotalSquaredCost int64                 `json:"total_squared_cost"`
}

// AssignMarkers solves a placement request without modifying the database:
// it maximizes the number of placed markers, then minimizes the sum of
// squared displacements, and breaks ties deterministically.
func (d *Database) AssignMarkers(q AssignmentQuery) (AssignmentResult, error) {
	if len(q.Markers) < 1 || len(q.Markers) > assign.MaxMarkers {
		return AssignmentResult{}, invalid("markers must contain 1..%d entries", assign.MaxMarkers)
	}
	if len(q.Candidates) < 1 || len(q.Candidates) > assign.MaxCandidates {
		return AssignmentResult{}, invalid("candidates must contain 1..%d entries", assign.MaxCandidates)
	}

	markers := make([]assign.Marker, 0, len(q.Markers))
	seenMarkers := make(map[string]bool, len(q.Markers))
	for _, marker := range q.Markers {
		if !catalog.ValidName(marker.ID) {
			return AssignmentResult{}, invalid("invalid marker id %q", marker.ID)
		}
		if seenMarkers[marker.ID] {
			return AssignmentResult{}, invalid("duplicate marker id %q", marker.ID)
		}
		seenMarkers[marker.ID] = true
		if err := marker.Point.Validate(); err != nil {
			return AssignmentResult{}, invalid("%v", err)
		}
		if marker.Radius < 0 {
			return AssignmentResult{}, invalid("marker %q radius must be non-negative", marker.ID)
		}
		// Radius is compared through its square, so reject values whose square
		// cannot be represented instead of silently wrapping.
		if marker.Radius > 0 && marker.Radius > math.MaxInt64/marker.Radius {
			return AssignmentResult{}, invalid("marker %q radius is too large", marker.ID)
		}
		markers = append(markers, assign.Marker{ID: marker.ID, Point: marker.Point, Radius: marker.Radius})
	}

	candidates := make([]assign.Candidate, 0, len(q.Candidates))
	seenCandidates := make(map[string]bool, len(q.Candidates))
	seenPoints := make(map[plane.Point]bool, len(q.Candidates))
	for _, candidate := range q.Candidates {
		if !catalog.ValidName(candidate.ID) {
			return AssignmentResult{}, invalid("invalid candidate id %q", candidate.ID)
		}
		if seenCandidates[candidate.ID] {
			return AssignmentResult{}, invalid("duplicate candidate id %q", candidate.ID)
		}
		seenCandidates[candidate.ID] = true
		if err := candidate.Point.Validate(); err != nil {
			return AssignmentResult{}, invalid("%v", err)
		}
		if seenPoints[candidate.Point] {
			return AssignmentResult{}, invalid("duplicate candidate coordinate (%d,%d)", candidate.Point.X, candidate.Point.Y)
		}
		seenPoints[candidate.Point] = true
		candidates = append(candidates, assign.Candidate{ID: candidate.ID, Point: candidate.Point})
	}

	occupied := make(map[plane.Point]struct{}, len(q.Occupied))
	for _, point := range q.Occupied {
		if err := point.Validate(); err != nil {
			return AssignmentResult{}, invalid("%v", err)
		}
		occupied[point] = struct{}{}
	}

	// Sorting by id fixes the solver's index-based tie breaking, so the same
	// request always produces the same response regardless of array order.
	slices.SortFunc(markers, func(a, b assign.Marker) int { return compareNames(a.ID, b.ID) })
	slices.SortFunc(candidates, func(a, b assign.Candidate) int { return compareNames(a.ID, b.ID) })

	solution := assign.Solve(markers, candidates, occupied)

	result := AssignmentResult{
		Assigned:   solution.Assigned,
		Unassigned: solution.Unassigned,
		Placements: make([]AssignmentPlacement, 0, len(solution.Placements)),
	}
	for _, placement := range solution.Placements {
		if result.TotalSquaredCost > math.MaxInt64-placement.DistanceSquared {
			return AssignmentResult{}, exhausted("total squared displacement exceeds 64-bit range")
		}
		result.TotalSquaredCost += placement.DistanceSquared
		result.Placements = append(result.Placements, AssignmentPlacement{
			MarkerID:        placement.MarkerID,
			CandidateID:     placement.CandidateID,
			Point:           placement.Point,
			DistanceSquared: placement.DistanceSquared,
		})
	}
	slices.SortFunc(result.Placements, func(a, b AssignmentPlacement) int { return compareNames(a.MarkerID, b.MarkerID) })
	slices.Sort(result.Unassigned)
	return result, nil
}

func compareNames(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
