// Package workorder normalizes a shuffled historical edit ledger into
// per-transaction replay batches and classifies what can be reconstructed
// without guessing. It never touches a live database; the optional verifier
// runs every candidate batch against a real HTTP server in a temp directory.
package workorder

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// Row is one ledger line. Line is the immutable source line number; the slice
// order of Rows is the shuffled order in which the ledger was received.
type Row struct {
	Line  int              `json:"line"`
	Group string           `json:"group"`
	Layer string           `json:"layer"`
	ID    string           `json:"id"`
	Mode  string           `json:"mode"`
	Value *catalog.Feature `json:"value,omitempty"`
}

// FeatureSpec seeds the baseline content of one layer.
type FeatureSpec struct {
	Layer string      `json:"layer"`
	ID    string      `json:"id"`
	Class string      `json:"class"`
	Shape plane.Shape `json:"shape"`
}

// Order is the whole historical ledger plus the committed baseline it was
// written against. Baseline content is the only state the ledger may assume.
type Order struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// CommitOrder names the transaction groups in the order they were
	// originally committed. It is separate from row order because the rows
	// themselves arrive shuffled.
	CommitOrder []string             `json:"commit_order"`
	Layers      []catalog.Definition `json:"layers"`
	Features    []FeatureSpec        `json:"features"`
	Rows        []Row                `json:"rows"`
	// Expect optionally pins each group's expected classification so the
	// ledger is self-checking. Analyses that disagree fail instead of
	// silently producing different replay material.
	Expect map[string]string `json:"expect,omitempty"`
}

// commitOrder validates and returns the declared group commit sequence. Every
// group in the ledger must appear exactly once.
func (o *Order) commitOrder() []string {
	order := append([]string(nil), o.CommitOrder...)
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		seen[name] = true
	}
	for _, row := range o.Rows {
		if !seen[row.Group] {
			order = append(order, row.Group)
			seen[row.Group] = true
		}
	}
	return order
}

func (o *Order) checkGroups() error {
	declared := make(map[string]bool, len(o.CommitOrder))
	for _, name := range o.CommitOrder {
		if name == "" {
			return fmt.Errorf("commit_order contains an empty group name")
		}
		if declared[name] {
			return fmt.Errorf("commit_order lists group %q twice", name)
		}
		declared[name] = true
	}
	for _, row := range o.Rows {
		if !declared[row.Group] {
			return fmt.Errorf("line %d belongs to group %q, which commit_order does not list", row.Line, row.Group)
		}
	}
	return nil
}

func Load(path string) (*Order, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var order Order
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&order); err != nil {
		return nil, fmt.Errorf("decode work order: %w", err)
	}
	if err := order.validate(); err != nil {
		return nil, err
	}
	return &order, nil
}

// MarshalOrder encodes an order deterministically for tests and fixtures.
func MarshalOrder(o *Order) ([]byte, error) {
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func (o *Order) validate() error {
	if o.Name == "" {
		return fmt.Errorf("work order needs a name")
	}
	seen := map[string]bool{}
	for _, def := range o.Layers {
		if err := def.Validate(); err != nil {
			return err
		}
		if seen[def.Name] {
			return fmt.Errorf("duplicate layer %q", def.Name)
		}
		seen[def.Name] = true
	}
	if _, ok := seen[""]; ok {
		return fmt.Errorf("empty layer name")
	}
	for _, spec := range o.Features {
		if !seen[spec.Layer] {
			return fmt.Errorf("baseline feature %q names unknown layer %q", spec.ID, spec.Layer)
		}
	}
	lines := map[int]string{}
	for _, row := range o.Rows {
		if row.Line <= 0 || row.Group == "" {
			return fmt.Errorf("row needs a positive line and a group: %+v", row)
		}
		if previous, ok := lines[row.Line]; ok {
			return fmt.Errorf("source line %d is used by groups %q and %q", row.Line, previous, row.Group)
		}
		lines[row.Line] = row.Group
	}
	return o.checkGroups()
}

// Layer is one mutable catalog layer inside a simulated world.
type Layer struct {
	Def  catalog.Definition
	Rows map[string]catalog.Feature
}

// World is the state a batch would be applied to. It is copied per candidate
// ordering so simulations never share maps or shape pointers.
type World struct {
	Layers map[string]*Layer
}

func (o *Order) Baseline() (*World, error) {
	w := &World{Layers: make(map[string]*Layer, len(o.Layers))}
	for _, def := range o.Layers {
		w.Layers[def.Name] = &Layer{Def: def, Rows: make(map[string]catalog.Feature)}
	}
	for _, spec := range o.Features {
		l := w.Layers[spec.Layer]
		feature := catalog.Feature{ID: spec.ID, Class: spec.Class, Shape: spec.Shape}
		if err := feature.Validate(l.Def); err != nil {
			return nil, err
		}
		if _, present := l.Rows[feature.ID]; present {
			return nil, fmt.Errorf("duplicate baseline feature %q in %q", feature.ID, spec.Layer)
		}
		l.Rows[feature.ID] = feature.Copy()
	}
	for _, l := range w.Layers {
		if err := checkLayer(l.Def, l.Rows); err != nil {
			return nil, fmt.Errorf("baseline layer %q: %w", l.Def.Name, err)
		}
	}
	return w, nil
}

func (w *World) Clone() *World {
	next := &World{Layers: make(map[string]*Layer, len(w.Layers))}
	for name, l := range w.Layers {
		rows := make(map[string]catalog.Feature, len(l.Rows))
		for id, feature := range l.Rows {
			rows[id] = feature.Copy()
		}
		next.Layers[name] = &Layer{Def: l.Def, Rows: rows}
	}
	return next
}

// Fail mirrors the database error codes so the plan and the HTTP verifier use
// the same vocabulary.
type Fail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Fail) Error() string { return f.Message }

func conflictf(format string, args ...any) *Fail {
	return &Fail{Code: "conflict", Message: fmt.Sprintf(format, args...)}
}
func missingf(format string, args ...any) *Fail {
	return &Fail{Code: "missing", Message: fmt.Sprintf(format, args...)}
}
func invalidf(format string, args ...any) *Fail {
	return &Fail{Code: "invalid", Message: fmt.Sprintf(format, args...)}
}

// ApplyBatch reproduces POST /edits semantics: rows are interpreted in the
// given order, and only the final state of each touched layer is checked for
// feature rules and exclusivity.
func (w *World) ApplyBatch(edits []catalog.Edit) (*World, []string, error) {
	next := w.Clone()
	staged := make(map[string]map[string]catalog.Feature)
	changed := make([]string, 0, len(edits))
	for i, edit := range edits {
		l, ok := next.Layers[edit.Layer]
		if !ok {
			return nil, nil, missingf("edit %d: layer %q does not exist", i, edit.Layer)
		}
		if staged[edit.Layer] == nil {
			rows := make(map[string]catalog.Feature, len(l.Rows))
			for id, feature := range l.Rows {
				rows[id] = feature.Copy()
			}
			staged[edit.Layer] = rows
			changed = append(changed, edit.Layer)
		}
		if err := applyEdit(l.Def, staged[edit.Layer], edit, i); err != nil {
			return nil, nil, err
		}
	}
	sort.Strings(changed)
	for _, name := range changed {
		l := next.Layers[name]
		l.Rows = staged[name]
		if err := checkLayer(l.Def, l.Rows); err != nil {
			return nil, nil, err
		}
	}
	return next, changed, nil
}

func applyEdit(def catalog.Definition, rows map[string]catalog.Feature, edit catalog.Edit, index int) error {
	if err := edit.Validate(); err != nil {
		return invalidf("edit %d: %v", index, err)
	}
	_, present := rows[edit.ID]
	if edit.Mode == "insert" && present {
		return conflictf("edit %d: feature %q already exists", index, edit.ID)
	}
	if edit.Mode != "insert" && !present {
		return missingf("edit %d: feature %q does not exist", index, edit.ID)
	}
	if edit.Mode == "remove" {
		delete(rows, edit.ID)
		return nil
	}
	if err := edit.Value.Validate(def); err != nil {
		return invalidf("edit %d: %v", index, err)
	}
	rows[edit.ID] = edit.Value.Copy()
	return nil
}

func checkLayer(def catalog.Definition, rows map[string]catalog.Feature) error {
	if len(rows) > catalog.MaxLayerRows {
		return &Fail{Code: "capacity", Message: fmt.Sprintf("layer %s exceeds feature capacity", def.Name)}
	}
	ordered := make([]catalog.Feature, 0, len(rows))
	for _, feature := range rows {
		if err := feature.Validate(def); err != nil {
			return invalidf("%v", err)
		}
		ordered = append(ordered, feature)
	}
	slices.SortFunc(ordered, func(a, b catalog.Feature) int { return strings.Compare(a.ID, b.ID) })
	if def.Exclusive {
		tree := packed.Build(ordered)
		if left, right, overlap := tree.Conflict(ordered); overlap {
			return conflictf("exclusive layer %s: %s intersects %s", def.Name, left, right)
		}
	}
	return nil
}

// ShapeDigest is independent of revision numbers: it hashes only the layer
// definitions and feature content, sorted, so two replays with the same final
// catalog produce the same digest.
func (w *World) ShapeDigest() string {
	return w.ShapeDigestOf(w.LayerNames())
}

// ShapeDigestOf restricts the digest to the named layers. Transaction groups
// are reported with a layer-scoped digest so a genuinely undecided neighbour
// layer does not make an otherwise settled group look unknown.
func (w *World) ShapeDigestOf(names []string) string {
	type layerView struct {
		Name     string            `json:"name"`
		Kind     plane.Kind        `json:"kind"`
		Layout   catalog.Layout    `json:"layout"`
		Features []catalog.Feature `json:"features"`
	}
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)
	view := make([]layerView, 0, len(ordered))
	for _, name := range ordered {
		l := w.Layers[name]
		if l == nil {
			continue
		}
		features := make([]catalog.Feature, 0, len(l.Rows))
		for _, feature := range l.Rows {
			features = append(features, feature.Copy())
		}
		slices.SortFunc(features, func(a, b catalog.Feature) int { return strings.Compare(a.ID, b.ID) })
		view = append(view, layerView{Name: name, Kind: l.Def.Kind, Layout: l.Def.Layout, Features: features})
	}
	raw, _ := json.Marshal(view)
	return digest(raw)
}

// LayerNames returns every layer name sorted.
func (w *World) LayerNames() []string {
	names := make([]string, 0, len(w.Layers))
	for name := range w.Layers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
