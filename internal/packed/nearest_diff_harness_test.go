package packed_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// This file contains the differential harness for packed.Tree.Nearest.
//
// The reference oracle below never touches the index: it computes the closure
// distance of every feature envelope with an explicitly written formula and
// sorts the survivors with the standard library, so a bug shared with the
// production heap/insertion path cannot mask itself. Every generated input is
// deterministic (fixed RNG seed) and every curated input is a frozen JSON file
// under testdata/nearest_diff, so any failure can be replayed standalone with
// MERIDIAN_NEAREST_REPLAY pointing at the artifact the harness writes.

// ---------- replayable wire spelling (mirrors the public HTTP JSON API) ----

type diffPoint struct {
	X int64 `json:"x"`
	Y int64 `json:"y"`
}

type diffBox struct {
	MinX int64 `json:"min_x"`
	MinY int64 `json:"min_y"`
	MaxX int64 `json:"max_x"`
	MaxY int64 `json:"max_y"`
}

type diffShape struct {
	Mark   *diffPoint `json:"mark,omitempty"`
	Extent *diffBox   `json:"extent,omitempty"`
}

type diffFeature struct {
	ID    string    `json:"id"`
	Class string    `json:"class"`
	Shape diffShape `json:"shape"`
}

// diffCase is the minimal standalone replayable input: features, query point,
// limit and optional class filter.
type diffCase struct {
	Name     string        `json:"name"`
	Seed     int64         `json:"seed,omitempty"`
	Features []diffFeature `json:"features"`
	Point    diffPoint     `json:"point"`
	Limit    int           `json:"limit"`
	Class    string        `json:"class,omitempty"`
}

type diffNeighbor struct {
	ID              string `json:"id"`
	Class           string `json:"class"`
	DistanceSquared int64  `json:"distance_squared"`
}

type diffExpectation struct {
	Point diffPoint      `json:"point"`
	Limit int            `json:"limit"`
	Class string         `json:"class,omitempty"`
	Want  []diffNeighbor `json:"want"`
}

type diffFixture struct {
	Scenario string            `json:"scenario"`
	Case     diffCase          `json:"case"`
	Checks   []diffExpectation `json:"checks"`
}

// diffArtifact is written on failure so the small input and both orderings can
// be replayed independently of the random generator.
type diffArtifact struct {
	Kind     string         `json:"artifact"`
	Seed     int64          `json:"seed"`
	Input    diffCase       `json:"input"`
	Expected []diffNeighbor `json:"expected"`
	Got      []diffNeighbor `json:"got"`
	Tree     packed.Stats   `json:"tree_stats"`
}

func toFeatures(rows []diffFeature) []catalog.Feature {
	out := make([]catalog.Feature, 0, len(rows))
	for _, row := range rows {
		feature := catalog.Feature{ID: row.ID, Class: row.Class}
		switch {
		case row.Shape.Mark != nil:
			p := plane.Point{X: row.Shape.Mark.X, Y: row.Shape.Mark.Y}
			feature.Shape = plane.Shape{Mark: &p}
		case row.Shape.Extent != nil:
			b := plane.Box{
				MinX: row.Shape.Extent.MinX, MinY: row.Shape.Extent.MinY,
				MaxX: row.Shape.Extent.MaxX, MaxY: row.Shape.Extent.MaxY,
			}
			feature.Shape = plane.Shape{Extent: &b}
		}
		out = append(out, feature)
	}
	return out
}

// envelopeOf re-derives the envelope without routing through plane.Shape so
// the oracle stays structurally independent of the production shape helpers.
func envelopeOf(shape diffShape) diffBox {
	if shape.Mark != nil {
		return diffBox{MinX: shape.Mark.X, MinY: shape.Mark.Y, MaxX: shape.Mark.X, MaxY: shape.Mark.Y}
	}
	return *shape.Extent
}

// closureDistanceSquared is the independently written reference distance:
// squared Euclidean distance from p to the closed box envelope, zero when p is
// inside or on any edge (closure rule from PROJECT_SPEC.md).
func closureDistanceSquared(b diffBox, p diffPoint) int64 {
	var dx, dy int64
	switch {
	case p.X < b.MinX:
		dx = b.MinX - p.X
	case p.X > b.MaxX:
		dx = p.X - b.MaxX
	}
	switch {
	case p.Y < b.MinY:
		dy = b.MinY - p.Y
	case p.Y > b.MaxY:
		dy = p.Y - b.MaxY
	}
	return dx*dx + dy*dy
}

// oracleNearest is the brute-force reference answer. It deliberately uses
// sort.Slice with a hand-written comparator instead of the production heap,
// binary search and slices.Insert machinery.
func oracleNearest(c diffCase) []diffNeighbor {
	answer := make([]diffNeighbor, 0, len(c.Features))
	for _, feature := range c.Features {
		if c.Class != "" && feature.Class != c.Class {
			continue
		}
		answer = append(answer, diffNeighbor{
			ID:              feature.ID,
			Class:           feature.Class,
			DistanceSquared: closureDistanceSquared(envelopeOf(feature.Shape), c.Point),
		})
	}
	sort.Slice(answer, func(i, j int) bool {
		if answer[i].DistanceSquared != answer[j].DistanceSquared {
			return answer[i].DistanceSquared < answer[j].DistanceSquared
		}
		return answer[i].ID < answer[j].ID
	})
	if len(answer) > c.Limit {
		answer = answer[:c.Limit]
	}
	return answer
}

func summarize(rows []packed.Neighbor) []diffNeighbor {
	out := make([]diffNeighbor, 0, len(rows))
	for _, row := range rows {
		out = append(out, diffNeighbor{
			ID:              row.Feature.ID,
			Class:           row.Feature.Class,
			DistanceSquared: row.DistanceSquared,
		})
	}
	return out
}

func renderOrder(rows []diffNeighbor) string {
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = fmt.Sprintf("%s(d=%d)", row.ID, row.DistanceSquared)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// runDifferential builds the production tree from the same input the oracle
// sees, runs the public Nearest entry point twice (the rebuilt tree must give
// the same stable order), and compares the full ordered answer against the
// oracle. On mismatch it persists a replay artifact and fails the test.
func runDifferential(t *testing.T, label string, seed int64, c diffCase) {
	t.Helper()

	expected := oracleNearest(c)
	features := toFeatures(c.Features)

	tree := packed.Build(features)
	got, visit := tree.Nearest(plane.Point{X: c.Point.X, Y: c.Point.Y}, c.Limit, c.Class)
	actual := summarize(got)

	rebuilt := packed.Build(features)
	again, _ := rebuilt.Nearest(plane.Point{X: c.Point.X, Y: c.Point.Y}, c.Limit, c.Class)
	if !sameOrder(actual, summarize(again)) {
		t.Fatalf("%s: non-deterministic result across rebuilt trees: %s vs %s",
			label, renderOrder(actual), renderOrder(summarize(again)))
	}

	if len(c.Features) > 0 && visit.Nodes == 0 {
		t.Fatalf("%s: tree reported no visited nodes for %d features", label, len(c.Features))
	}

	if sameOrder(expected, actual) {
		return
	}

	artifact := diffArtifact{
		Kind:     "meridian-nearest-diff-v1",
		Seed:     seed,
		Input:    c,
		Expected: expected,
		Got:      actual,
		Tree:     tree.Stats(),
	}
	path := writeFailureArtifact(t, label, artifact)
	t.Fatalf(
		"%s: nearest ordering diverges from the independent oracle\n"+
			"  query point=(%d,%d) limit=%d class=%q features=%d\n"+
			"  expected: %s\n"+
			"  got:      %s\n"+
			"  replay:   MERIDIAN_NEAREST_REPLAY=%s go test ./internal/packed/ -run TestNearestReplay -v",
		label, c.Point.X, c.Point.Y, c.Limit, c.Class, len(c.Features),
		renderOrder(expected), renderOrder(actual), path)
}

func sameOrder(a, b []diffNeighbor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeFailureArtifact(t *testing.T, label string, artifact diffArtifact) string {
	dir := filepath.Join(os.TempDir(), "meridian-nearest-diff")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("could not create artifact directory: %v", err)
		return "<unwritable>"
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, strings.ToLower(label))
	path := filepath.Join(dir, safe+".json")
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(artifact); err != nil {
		t.Logf("could not encode failure artifact: %v", err)
		return "<unwritable>"
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Logf("could not write failure artifact %s: %v", path, err)
		return "<unwritable>"
	}
	return path
}

// loadReplay reads any of the three supported replay inputs: a bare diffCase,
// a failure artifact produced by writeFailureArtifact, or a frozen fixture
// carrying several checks. A fixture expands into one case per check.
func loadReplay(path string) ([]diffCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Kind  string   `json:"artifact"`
		Input diffCase `json:"input"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Kind != "" {
		return []diffCase{wrapper.Input}, nil
	}
	var fixture diffFixture
	if err := json.Unmarshal(raw, &fixture); err == nil && len(fixture.Checks) > 0 {
		cases := make([]diffCase, 0, len(fixture.Checks))
		for i, check := range fixture.Checks {
			cases = append(cases, diffCase{
				Name:     fixture.Case.Name + "/check-" + strconv.Itoa(i),
				Features: fixture.Case.Features,
				Point:    check.Point,
				Limit:    check.Limit,
				Class:    check.Class,
			})
		}
		return cases, nil
	}
	var c diffCase
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return []diffCase{c}, nil
}
