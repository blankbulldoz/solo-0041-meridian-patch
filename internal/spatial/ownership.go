package spatial

import (
	"cmp"
	"errors"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// maxOwnershipCandidates bounds the index work one ownership audit may spend.
// The audit reads one immutable view and never writes, so the cap only rejects
// queries that would scan an excessive share of the extent layer.
const maxOwnershipCandidates = 200_000

// ErrOwnershipBudget reports that an ownership audit exceeded its work budget.
var ErrOwnershipBudget = errors.New("ownership audit exceeds candidate budget")

// EdgeRole describes one coordinate of a point against a rectangle's sides.
// "inside" means the half-open interval holds (the lower side is included);
// "open_max" means the coordinate equals the excluded upper side; "outside"
// means it lies beyond the rectangle.
type EdgeRole string

const (
	RoleInside  EdgeRole = "inside"
	RoleOpenMax EdgeRole = "open_max"
	RoleOutside EdgeRole = "outside"
)

// Membership explains exactly why a block does or does not own a mark. The
// half-open rule is [min,max) on every axis: an open_max role on any axis
// makes Owned false even when the closed-envelope distance is zero.
type Membership struct {
	X        EdgeRole `json:"x"`
	Y        EdgeRole `json:"y"`
	Owned    bool     `json:"owned"`
	OnEdge   bool     `json:"on_edge"`
	Distance int64    `json:"distance_squared"`
}

// OwnershipRecord pairs an extent feature with the half-open membership verdict
// for one mark. Contacts carries the blocks the point merely touches on an open
// side, so boundary hand-offs stay explainable without turning into ownership.
type OwnershipRecord struct {
	Block      catalog.Feature `json:"block"`
	Membership Membership      `json:"membership"`
}

// MarkAudit is the geometric ownership picture for one mark, before policy.
type MarkAudit struct {
	Mark     catalog.Feature   `json:"mark"`
	Owners   []OwnershipRecord `json:"owners"`
	Contacts []OwnershipRecord `json:"contacts"`
}

// OwnershipResult is the deterministic per-mark audit. Marks and the blocks of
// each mark are ordered by feature ID so revisions compare field by field.
type OwnershipResult struct {
	Marks []MarkAudit  `json:"marks"`
	Visit packed.Visit `json:"visit"`
}

// Ownership audits every mark against the extent tree inside window. Blocks the
// point touches at zero distance but does not satisfy under the half-open rule
// are returned as contacts rather than owners. The total index candidate count
// across all marks is bounded by maxOwnershipCandidates.
func Ownership(marks, extents packed.Tree, window plane.Box) (OwnershipResult, error) {
	result := OwnershipResult{Marks: make([]MarkAudit, 0)}
	markRows := Window(marks, window, "", "")
	result.Visit = markRows.Visit
	for _, mark := range markRows.Features {
		point := *mark.Shape.Mark
		audit := MarkAudit{Mark: mark, Owners: make([]OwnershipRecord, 0), Contacts: make([]OwnershipRecord, 0)}
		var exhausted bool
		visit := extents.Search(point.Envelope(), func(block catalog.Feature) bool {
			record := OwnershipRecord{Block: block, Membership: explain(*block.Shape.Extent, point)}
			if record.Membership.Owned {
				audit.Owners = append(audit.Owners, record)
			} else if record.Membership.Distance == 0 {
				audit.Contacts = append(audit.Contacts, record)
			}
			if result.Visit.Candidates >= maxOwnershipCandidates {
				exhausted = true
				return false
			}
			return true
		})
		if exhausted {
			return OwnershipResult{}, ErrOwnershipBudget
		}
		result.Visit.Nodes += visit.Nodes
		result.Visit.Candidates += visit.Candidates
		slices.SortFunc(audit.Owners, compareRecord)
		slices.SortFunc(audit.Contacts, compareRecord)
		result.Marks = append(result.Marks, audit)
	}
	return result, nil
}

func compareRecord(a, b OwnershipRecord) int {
	return cmp.Compare(a.Block.ID, b.Block.ID)
}

func explain(box plane.Box, p plane.Point) Membership {
	m := Membership{
		X:        axisRole(p.X, box.MinX, box.MaxX),
		Y:        axisRole(p.Y, box.MinY, box.MaxY),
		Distance: box.DistanceSquared(p),
	}
	m.Owned = box.Owns(p)
	m.OnEdge = m.X == RoleOpenMax || m.Y == RoleOpenMax
	return m
}

func axisRole(value, min, max int64) EdgeRole {
	switch {
	case value == max:
		return RoleOpenMax
	case value < min || value > max:
		return RoleOutside
	default:
		// value == min is included by the half-open [min,max) interval.
		return RoleInside
	}
}
