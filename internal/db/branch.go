package db

import (
	"maps"
	"math"
	"slices"
	"sync"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// A branch is an in-memory trial view forked from one formal revision. Its
// layers carry branch-local revisions and its edits never touch the durable
// image or the formal revision counter. Discarding it therefore costs the
// formal database nothing, and only an explicit publish can change the formal
// catalog.
type branch struct {
	name         string
	baseRevision uint64
	base         *state

	// mu serializes packs against this branch only, so a slow pack on one
	// branch never stalls another branch's reads or packs.
	mu   sync.Mutex
	head *state
}

type BranchView struct {
	Name         string            `json:"name"`
	BaseRevision uint64            `json:"base_revision"`
	Revision     uint64            `json:"revision"`
	Layers       []catalog.Summary `json:"layers"`
	Changed      []string          `json:"changed"`
}

type BranchList struct {
	Revision uint64       `json:"revision"`
	Branches []BranchView `json:"branches"`
}

type BranchDiscard struct {
	Name      string `json:"name"`
	Discarded bool   `json:"discarded"`
	Revision  uint64 `json:"revision"`
}

// forkState derives an independent view at branch revision zero. Rows and
// trees are immutable and may be shared with the fork base; every branch
// mutation clones the rows it stages before writing.
func forkState(base *state, name string) *state {
	forked := &state{revision: 0, branch: name, layers: make(map[string]*layer, len(base.layers))}
	for name, l := range base.layers {
		forked.layers[name] = &layer{definition: l.definition, revision: 0, rows: l.rows, tree: l.tree}
	}
	return forked
}

// changedNames lists the layers touched since the fork. Branch revisions start
// at zero, so any layer at a higher revision was rebuilt inside the branch.
func changedNames(s *state) []string {
	names := make([]string, 0)
	for name, l := range s.layers {
		if l.revision > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (b *branch) view() BranchView {
	head := b.head
	view := BranchView{
		Name:         b.name,
		BaseRevision: b.baseRevision,
		Revision:     head.revision,
		Layers:       make([]catalog.Summary, 0, len(head.layers)),
		Changed:      changedNames(head),
	}
	for _, name := range head.names() {
		view.Layers = append(view.Layers, head.layers[name].summary())
	}
	return view
}

func (d *Database) CreateBranch(name string, expected *uint64) (BranchView, error) {
	if !catalog.ValidName(name) {
		return BranchView{}, invalid("invalid branch name %q", name)
	}
	d.branchesMu.Lock()
	defer d.branchesMu.Unlock()
	if d.blocked.Load() {
		return BranchView{}, unavailable("database is fenced; reopen the data directory")
	}
	base := d.head.Load()
	if expected != nil && *expected != base.revision {
		return BranchView{}, conflict("expected database revision %d, current revision is %d", *expected, base.revision)
	}
	if d.branches[name] != nil {
		return BranchView{}, conflict("branch %q already exists", name)
	}
	if len(d.branches) >= catalog.MaxBranches {
		return BranchView{}, exhausted("branch capacity reached")
	}
	b := &branch{name: name, baseRevision: base.revision, base: base, head: forkState(base, name)}
	d.branches[name] = b
	return b.view(), nil
}

func (d *Database) Branches() BranchList {
	d.branchesMu.RLock()
	branches := maps.Clone(d.branches)
	d.branchesMu.RUnlock()
	out := BranchList{
		Revision: d.head.Load().revision,
		Branches: make([]BranchView, 0, len(branches)),
	}
	names := make([]string, 0, len(branches))
	for name := range branches {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		out.Branches = append(out.Branches, branches[name].snapshot())
	}
	return out
}

// snapshot reads one branch head under its own mutex.
func (b *branch) snapshot() BranchView {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.view()
}

// ListBranch returns the branch catalog in name order, so a caller can inspect
// a trial catalog without touching the formal listing.
func (d *Database) ListBranch(name string) (Commit, error) {
	s, err := d.branchState(name)
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, s.names()), nil
}

func (d *Database) Branch(name string) (BranchView, error) {
	if !catalog.ValidName(name) {
		return BranchView{}, invalid("invalid branch name %q", name)
	}
	d.branchesMu.RLock()
	b := d.branches[name]
	d.branchesMu.RUnlock()
	if b == nil {
		return BranchView{}, missing("branch %q does not exist", name)
	}
	return b.snapshot(), nil
}

// branchState resolves a read view. An empty branch name selects the formal
// database; otherwise the branch head is returned so every query in the
// request sees one immutable view with branch-local revisions.
func (d *Database) branchState(name string) (*state, error) {
	if name == "" {
		return d.head.Load(), nil
	}
	if !catalog.ValidName(name) {
		return nil, invalid("invalid branch name %q", name)
	}
	d.branchesMu.RLock()
	b := d.branches[name]
	d.branchesMu.RUnlock()
	if b == nil {
		return nil, missing("branch %q does not exist", name)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.head, nil
}

func (d *Database) mutateBranch(name string, expected *uint64, prepare func(*state, uint64) (*state, []string, error)) (BranchView, error) {
	if !catalog.ValidName(name) {
		return BranchView{}, invalid("invalid branch name %q", name)
	}
	d.branchesMu.RLock()
	b := d.branches[name]
	d.branchesMu.RUnlock()
	if b == nil {
		return BranchView{}, missing("branch %q does not exist", name)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if d.blocked.Load() {
		return BranchView{}, unavailable("database is fenced; reopen the data directory")
	}
	current := b.head
	if expected != nil && *expected != current.revision {
		return BranchView{}, conflict("expected branch revision %d, current revision is %d", *expected, current.revision)
	}
	if current.revision == math.MaxUint64 {
		return BranchView{}, exhausted("branch revision space is exhausted")
	}
	next, _, err := prepare(current, current.revision+1)
	if err != nil {
		return BranchView{}, err
	}
	if next != current {
		b.head = next
	}
	return b.view(), nil
}

// Branch packs use the same apply helpers as formal commits, so ordering,
// per-item validation and final-state exclusivity behave identically.
func (d *Database) BranchRegister(name string, def catalog.Definition, expected *uint64) (BranchView, error) {
	return d.mutateBranch(name, expected, func(current *state, revision uint64) (*state, []string, error) {
		return applyRegister(current, revision, def)
	})
}

func (d *Database) BranchReframe(name, layer string, layout catalog.Layout, expected *uint64) (BranchView, error) {
	return d.mutateBranch(name, expected, func(current *state, revision uint64) (*state, []string, error) {
		return applyReframe(current, revision, layer, layout)
	})
}

func (d *Database) BranchEdit(name string, batch catalog.EditBatch) (BranchView, error) {
	return d.mutateBranch(name, batch.IfRevision, func(current *state, revision uint64) (*state, []string, error) {
		return applyEditBatch(current, revision, batch)
	})
}

// mergeBranch replaces only the layers the branch rebuilt. Every such layer
// must still match its fork-time formal revision, otherwise the formal catalog
// moved on and the publish is rejected instead of overwriting it. Layers the
// branch never touched always keep their current formal contents.
func mergeBranch(current, base, head *state, revision uint64) (*state, []string, error) {
	changed := changedNames(head)
	next := &state{revision: revision, branch: current.branch, layers: maps.Clone(current.layers)}
	for _, name := range changed {
		branchLayer := head.layers[name]
		baseLayer := base.layers[name]
		formal := current.layers[name]
		if baseLayer == nil {
			if formal != nil {
				return nil, nil, conflict("branch layer %q already exists in the formal database", name)
			}
		} else if formal == nil || formal.revision != baseLayer.revision {
			return nil, nil, conflict("layer %q moved in the formal database after the branch forked at revision %d", name, base.revision)
		}
		rebuilt, err := buildLayer(branchLayer.definition, revision, branchLayer.rows)
		if err != nil {
			return nil, nil, err
		}
		next.layers[name] = rebuilt
	}
	if len(next.layers) > catalog.MaxLayers {
		return nil, nil, exhausted("layer capacity reached")
	}
	var total int
	for _, l := range next.layers {
		total += len(l.rows)
	}
	if total > catalog.MaxRows {
		return nil, nil, exhausted("database feature capacity reached")
	}
	return next, changed, nil
}

// PublishBranch durably installs the branch's rebuilt layers at one new formal
// revision. It takes the formal write lock and the branch lock, so no formal
// commit or branch pack can interleave with the merge.
func (d *Database) PublishBranch(name string, expected *uint64) (Commit, error) {
	if !catalog.ValidName(name) {
		return Commit{}, invalid("invalid branch name %q", name)
	}
	d.write.Lock()
	defer d.write.Unlock()
	d.branchesMu.Lock()
	defer d.branchesMu.Unlock()
	if d.blocked.Load() {
		return Commit{}, unavailable("database is fenced; reopen the data directory")
	}
	b := d.branches[name]
	if b == nil {
		return Commit{}, missing("branch %q does not exist", name)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current := d.head.Load()
	if expected != nil && *expected != current.revision {
		return Commit{}, conflict("expected database revision %d, current revision is %d", *expected, current.revision)
	}
	if len(changedNames(b.head)) == 0 {
		delete(d.branches, name)
		return summaries(current, current.names()), nil
	}
	var changed []string
	s, err := d.commitUnderLock(nil, func(cur *state, revision uint64) (*state, error) {
		next, names, err := mergeBranch(cur, b.base, b.head, revision)
		changed = names
		return next, err
	})
	if err != nil {
		return Commit{}, err
	}
	delete(d.branches, name)
	return summaries(s, changed), nil
}

// DiscardBranch drops the trial view. It never touches the durable image, the
// formal revision counter or any other branch.
func (d *Database) DiscardBranch(name string) (BranchDiscard, error) {
	if !catalog.ValidName(name) {
		return BranchDiscard{}, invalid("invalid branch name %q", name)
	}
	d.branchesMu.Lock()
	defer d.branchesMu.Unlock()
	if d.branches[name] == nil {
		return BranchDiscard{}, missing("branch %q does not exist", name)
	}
	delete(d.branches, name)
	return BranchDiscard{Name: name, Discarded: true, Revision: d.head.Load().revision}, nil
}
