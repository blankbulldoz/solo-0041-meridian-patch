package packed

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

type Audit struct {
	Records      int `json:"records"`
	CheckedNodes int `json:"checked_nodes"`
}

func (t Tree) Audit(rows map[string]catalog.Feature) (Audit, error) {
	seen := make(map[string]bool, len(rows))
	var count int
	if t.root != nil {
		if _, err := auditNode(t.root, rows, seen, &count); err != nil {
			return Audit{}, err
		}
	}
	if len(seen) != len(rows) {
		return Audit{}, fmt.Errorf("tree has %d unique records, catalog has %d", len(seen), len(rows))
	}
	return Audit{Records: len(seen), CheckedNodes: count}, nil
}

func auditNode(n *node, rows map[string]catalog.Feature, seen map[string]bool, count *int) (plane.Box, error) {
	*count += 1
	var total plane.Box
	if n.left == nil && n.right == nil {
		if len(n.rows) == 0 || len(n.rows) > leafSize {
			return plane.Box{}, fmt.Errorf("invalid leaf occupancy: %d", len(n.rows))
		}
		total = n.rows[0].Shape.Envelope()
		for _, feature := range n.rows {
			canonical, present := rows[feature.ID]
			if !present || seen[feature.ID] || !reflect.DeepEqual(canonical, feature) {
				return plane.Box{}, fmt.Errorf("index entry %q inconsistent with catalog", feature.ID)
			}
			seen[feature.ID] = true
			total = total.Union(feature.Shape.Envelope())
		}
	} else {
		if n.left == nil || n.right == nil || len(n.rows) != 0 {
			return plane.Box{}, fmt.Errorf("invalid internal node shape")
		}
		left, err := auditNode(n.left, rows, seen, count)
		if err != nil {
			return plane.Box{}, err
		}
		right, err := auditNode(n.right, rows, seen, count)
		if err != nil {
			return plane.Box{}, err
		}
		total = left.Union(right)
	}
	if n.bounds != total {
		return plane.Box{}, fmt.Errorf("index envelope does not match contents")
	}
	return total, nil
}

// WindowAuditOutcome distinguishes a window with no spatial activity at all
// from a window whose closed envelopes produced candidates but whose exact,
// half-open spatial relation rejected every one of them.
const (
	WindowAuditEmpty       = "empty"
	WindowAuditClosureOnly = "closure_only"
	WindowAuditMatched     = "matched"
	WindowAuditMismatched  = "mismatched"
)

// WindowAudit compares the index traversal against an independent catalog
// scan inside one query window. The tree side reuses the production closure
// pruning (node and envelope MayMeet) and then applies the exact window
// relation; the catalog side applies the same exact relation to every catalog
// row without touching the tree. Identity lists are returned sorted so the two
// sides can be checked field by field.
type WindowAudit struct {
	Bounds       plane.Box `json:"bounds"`
	Visit        Visit     `json:"visit"`
	Candidates   []string  `json:"candidates"`
	IndexHits    []string  `json:"index_hits"`
	CatalogHits  []string  `json:"catalog_hits"`
	Agrees       bool      `json:"agrees"`
	Outcome      string    `json:"outcome"`
	CheckedNodes int       `json:"checked_nodes"`
}

func (t Tree) AuditWindow(bounds plane.Box, rows map[string]catalog.Feature) (WindowAudit, error) {
	report := WindowAudit{
		Bounds:       bounds,
		Candidates:   []string{},
		IndexHits:    []string{},
		CatalogHits:  []string{},
		CheckedNodes: 0,
	}
	var problem error
	report.Visit = t.Search(bounds, func(feature catalog.Feature) bool {
		canonical, present := rows[feature.ID]
		if !present || !reflect.DeepEqual(canonical, feature) {
			problem = fmt.Errorf("index entry %q inconsistent with catalog", feature.ID)
			return false
		}
		report.Candidates = append(report.Candidates, feature.ID)
		if feature.Shape.InWindow(bounds) {
			report.IndexHits = append(report.IndexHits, feature.ID)
		}
		return true
	})
	report.CheckedNodes = report.Visit.Nodes
	if problem != nil {
		return WindowAudit{}, problem
	}
	for _, feature := range rows {
		if feature.Shape.InWindow(bounds) {
			report.CatalogHits = append(report.CatalogHits, feature.ID)
		}
	}
	slices.Sort(report.Candidates)
	slices.Sort(report.IndexHits)
	slices.Sort(report.CatalogHits)
	report.Agrees = slices.Equal(report.IndexHits, report.CatalogHits)
	switch {
	case !report.Agrees:
		report.Outcome = WindowAuditMismatched
	case len(report.Candidates) == 0:
		report.Outcome = WindowAuditEmpty
	case len(report.IndexHits) == 0:
		report.Outcome = WindowAuditClosureOnly
	default:
		report.Outcome = WindowAuditMatched
	}
	return report, nil
}
