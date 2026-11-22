package db

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/spatial"
)

// OwnershipQuery selects a mark layer and the extent layer that may own its
// marks, inside one window of one immutable view. ClassPriority is the
// caller-supplied order of block classes: earlier classes win. It may be empty.
type OwnershipQuery struct {
	Marks         string    `json:"marks"`
	Blocks        string    `json:"blocks"`
	Bounds        plane.Box `json:"bounds"`
	ClassPriority []string  `json:"class_priority,omitempty"`
}

// MarkOwnership is the audited verdict for one mark at the captured revision.
// State is "unowned" (zero blocks), "unique" (one block) or "shared" (many).
// Recommendation is set only when the priority rules leave a single winner;
// otherwise Conflicts lists every block tied at the best priority verbatim.
type MarkOwnership struct {
	Mark           catalog.Feature           `json:"mark"`
	State          string                    `json:"state"`
	OwnerCount     int                       `json:"owner_count"`
	Owners         []spatial.OwnershipRecord `json:"owners"`
	Contacts       []spatial.OwnershipRecord `json:"contacts"`
	Recommendation *string                   `json:"recommendation,omitempty"`
	Conflicts      []spatial.OwnershipRecord `json:"conflicts,omitempty"`
}

// OwnershipResponse carries the per-mark audit under one database revision.
type OwnershipResponse struct {
	Revision       uint64          `json:"revision"`
	MarksRevision  uint64          `json:"marks_revision"`
	BlocksRevision uint64          `json:"blocks_revision"`
	ClassPriority  []string        `json:"class_priority,omitempty"`
	Marks          []MarkOwnership `json:"marks"`
	Visit          packed.Visit    `json:"visit"`
}

func (d *Database) Ownership(q OwnershipQuery) (OwnershipResponse, error) {
	if q.Marks == q.Blocks {
		return OwnershipResponse{}, invalid("ownership audit requires distinct mark and block layers")
	}
	if err := validateWindow(q.Bounds, ""); err != nil {
		return OwnershipResponse{}, err
	}
	rank, err := priorityRank(q.ClassPriority)
	if err != nil {
		return OwnershipResponse{}, err
	}
	s := d.head.Load()
	markLayer, err := s.getLayer(q.Marks)
	if err != nil {
		return OwnershipResponse{}, err
	}
	blockLayer, err := s.getLayer(q.Blocks)
	if err != nil {
		return OwnershipResponse{}, err
	}
	if markLayer.definition.Kind != plane.Mark {
		return OwnershipResponse{}, invalid("ownership audit requires a mark layer, %q holds %s", q.Marks, markLayer.definition.Kind)
	}
	if blockLayer.definition.Kind != plane.Extent {
		return OwnershipResponse{}, invalid("ownership audit requires an extent layer, %q holds %s", q.Blocks, blockLayer.definition.Kind)
	}
	audited, err := spatial.Ownership(markLayer.tree, blockLayer.tree, q.Bounds)
	if err != nil {
		return OwnershipResponse{}, exhausted("%v", err)
	}
	response := OwnershipResponse{
		Revision:       s.revision,
		MarksRevision:  markLayer.revision,
		BlocksRevision: blockLayer.revision,
		ClassPriority:  append([]string(nil), q.ClassPriority...),
		Marks:          make([]MarkOwnership, 0, len(audited.Marks)),
		Visit:          audited.Visit,
	}
	for _, audit := range audited.Marks {
		entry := MarkOwnership{
			Mark:       audit.Mark,
			OwnerCount: len(audit.Owners),
			Owners:     audit.Owners,
			Contacts:   audit.Contacts,
		}
		switch {
		case entry.OwnerCount == 0:
			entry.State = "unowned"
		case entry.OwnerCount == 1:
			entry.State = "unique"
		default:
			entry.State = "shared"
		}
		entry.applyPolicy(rank)
		response.Marks = append(response.Marks, entry)
	}
	return response, nil
}

// applyPolicy chooses the unique recommendation or records the verbatim tie.
// Mentioned classes beat unmentioned ones; among the best rank every block is
// preserved so same-priority conflicts are listed rather than guessed.
func (m *MarkOwnership) applyPolicy(rank map[string]uint64) {
	if len(m.Owners) == 0 {
		return
	}
	best := m.Owners[0]
	bestRank, bestKnown := rank[best.Block.Class]
	for _, record := range m.Owners[1:] {
		nextRank, nextKnown := rank[record.Block.Class]
		if betterOwner(nextRank, nextKnown, bestRank, bestKnown) {
			best, bestRank, bestKnown = record, nextRank, nextKnown
		}
	}
	for _, record := range m.Owners {
		recordRank, recordKnown := rank[record.Block.Class]
		if equalPolicy(recordRank, recordKnown, bestRank, bestKnown) && record.Block.ID != best.Block.ID {
			m.Conflicts = append(m.Conflicts, record)
		}
	}
	if len(m.Conflicts) == 0 {
		recommendation := best.Block.ID
		m.Recommendation = &recommendation
		return
	}
	m.Conflicts = append([]spatial.OwnershipRecord{best}, m.Conflicts...)
	slices.SortFunc(m.Conflicts, func(a, b spatial.OwnershipRecord) int {
		return cmp.Compare(a.Block.ID, b.Block.ID)
	})
}

// betterOwner reports whether a candidate with the given rank outranks the
// current best. Known (priority-listed) classes always outrank unknown ones.
func betterOwner(candidateRank uint64, candidateKnown bool, bestRank uint64, bestKnown bool) bool {
	if candidateKnown != bestKnown {
		return candidateKnown
	}
	if !candidateKnown {
		return false
	}
	return candidateRank < bestRank
}

func equalPolicy(rankA uint64, knownA bool, rankB uint64, knownB bool) bool {
	if knownA != knownB {
		return false
	}
	if !knownA {
		return true
	}
	return rankA == rankB
}

// priorityRank validates the caller priority and returns the 1-based rank of
// every listed class. Duplicate classes would make the order ambiguous.
func priorityRank(classes []string) (map[string]uint64, error) {
	rank := make(map[string]uint64, len(classes))
	for index, class := range classes {
		if !catalog.ValidName(class) {
			return nil, invalid("invalid priority class %q", class)
		}
		if _, duplicate := rank[class]; duplicate {
			return nil, invalid("class %q appears more than once in class_priority", class)
		}
		rank[class] = uint64(index + 1)
	}
	return rank, nil
}
