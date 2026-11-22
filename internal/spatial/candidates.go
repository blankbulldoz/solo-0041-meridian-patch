package spatial

import (
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// scanCandidates drives the nested index traversal shared by the relation
// queries. Every left row probes the candidate tree with its own envelope and
// each visited candidate is handed to match, which applies the relation's
// window clipping and exact predicate.
//
// The traversal owns the shared bookkeeping once for every relation:
//   - visit counts from the outer selection seed every inner probe;
//   - the maxJoinCandidates budget is global across all probes, including the
//     outer selection, and exhaustion aborts the whole query with exhausted;
//   - reaching limit results aborts the query the same way, before appending.
//
// Adding a new spatial relation therefore only means supplying the left rows,
// their probe envelopes, the limit/error pair and the match predicate.
func scanCandidates[L, R any](
	seed packed.Visit,
	rows []L,
	tree packed.Tree,
	envelope func(L) plane.Box,
	limit int,
	exhausted error,
	match func(left L, candidate catalog.Feature) (R, bool),
) ([]R, packed.Visit, error) {
	results := make([]R, 0)
	visit := seed
	for _, row := range rows {
		remaining := maxJoinCandidates - visit.Candidates
		var done bool
		probe := tree.Search(envelope(row), func(candidate catalog.Feature) bool {
			remaining--
			if remaining < 0 {
				done = true
				return false
			}
			result, ok := match(row, candidate)
			if !ok {
				return true
			}
			if len(results) == limit {
				done = true
				return false
			}
			results = append(results, result)
			return true
		})
		if done {
			return nil, packed.Visit{}, exhausted
		}
		visit.Nodes += probe.Nodes
		visit.Candidates += probe.Candidates
	}
	return results, visit, nil
}
