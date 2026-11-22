package packed_test

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// frozenSeed keeps every generated input identical across machines and runs,
// which is what makes the differential suite repeatable. Bump it deliberately
// when expanding coverage; artifacts record the seed that produced them.
const frozenSeed int64 = 20261122

const fixtureDir = "testdata/nearest_diff"

// expectedShapes pins the tree geometry of the curated scenarios so the
// scenario description (for example "ties cross leaves") stays true even if
// the deterministic builder is tuned later.
var expectedShapes = map[string]packed.Stats{
	"cross-node-ties-marks":   {Depth: 2, Nodes: 3, Leaves: 2, Items: 15, MaxLeaf: 8},
	"cross-node-ties-extents": {Depth: 2, Nodes: 3, Leaves: 2, Items: 13, MaxLeaf: 7},
	"class-drops-near":        {Depth: 3, Nodes: 5, Leaves: 3, Items: 25, MaxLeaf: 12},
	"y-axis-split":            {Depth: 2, Nodes: 3, Leaves: 2, Items: 13, MaxLeaf: 7},
	"empty-tree":              {},
}

func TestNearestFrozenFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no frozen nearest-neighbor fixtures found")
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var fixture diffFixture
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		t.Run(fixture.Case.Name, func(t *testing.T) {
			features := toFeatures(fixture.Case.Features)
			stats := packed.Build(features).Stats()
			if want, pinned := expectedShapes[fixture.Case.Name]; pinned {
				if stats.Depth != want.Depth || stats.Nodes != want.Nodes ||
					stats.Leaves != want.Leaves || stats.Items != want.Items ||
					stats.MaxLeaf != want.MaxLeaf {
					t.Fatalf("tree shape for %s drifted: got %+v, want %+v",
						fixture.Case.Name, stats, want)
				}
			}
			for i, check := range fixture.Checks {
				c := diffCase{
					Name:     fixture.Case.Name,
					Features: fixture.Case.Features,
					Point:    check.Point,
					Limit:    check.Limit,
					Class:    check.Class,
				}
				label := fixture.Case.Name + "/check-" + strconv.Itoa(i)
				// First prove the hand-frozen expectation is itself correct
				// against the independent oracle, then run the tree against it.
				if got := oracleNearest(c); !sameOrder(got, check.Want) {
					t.Fatalf("%s: frozen want does not match oracle\n  frozen:   %s\n  oracle:   %s",
						label, renderOrder(check.Want), renderOrder(got))
				}
				runDifferential(t, label, frozenSeed, c)
			}
		})
	}
}

// genProfile describes one deterministic batch of random cases.
type genProfile struct {
	name     string
	trees    int
	features int
	extents  bool
	span     int64
	queries  int
}

func TestNearestDifferentialRandom(t *testing.T) {
	profiles := []genProfile{
		// Single leaf: the leaf-only branch of the traversal.
		{name: "marks-single-leaf", trees: 12, features: 12, extents: false, span: 16, queries: 6},
		// One internal split with point rows and tight coordinates to force ties.
		{name: "marks-two-leaves", trees: 10, features: 20, extents: false, span: 12, queries: 8},
		// Deeper trees where pruning can interact with ties across leaves.
		{name: "marks-deep", trees: 8, features: 60, extents: false, span: 40, queries: 10},
		// Rectangle rows exercising envelope closure distances.
		{name: "extents-two-leaves", trees: 10, features: 20, extents: true, span: 30, queries: 8},
		{name: "extents-deep", trees: 8, features: 55, extents: true, span: 60, queries: 10},
		// Mixed coordinate magnitudes, including near-limit values.
		{name: "wide-coordinates", trees: 6, features: 40, extents: true, span: 1_000_000_000, queries: 8},
		// Sparse deep trees so many nodes are pruned before the leaves load.
		{name: "sparse-marks", trees: 6, features: 48, extents: false, span: 100_000, queries: 10},
		// Heavy class filtering on clustered data, including empty survivors.
		{name: "filtered-clusters", trees: 8, features: 36, extents: false, span: 8, queries: 10},
	}

	rng := rand.New(rand.NewSource(frozenSeed))
	var total int
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			for treeIndex := 0; treeIndex < profile.trees; treeIndex++ {
				rows, classes := generateFeatures(rng, profile)
				tree := packed.Build(rows)
				if got := tree.Stats().Items; got != len(rows) {
					t.Fatalf("%s tree %d: indexed %d items, want %d",
						profile.name, treeIndex, got, len(rows))
				}
				for queryIndex := 0; queryIndex < profile.queries; queryIndex++ {
					c := generateCase(rng, profile, rows, classes, treeIndex, queryIndex)
					runDifferential(t,
						profile.name+"/t"+strconv.Itoa(treeIndex)+"/q"+strconv.Itoa(queryIndex),
						frozenSeed, c)
					total++
				}
			}
		})
	}
	t.Logf("differential cases executed: %d (seed %d)", total, frozenSeed)
}

func generateFeatures(rng *rand.Rand, p genProfile) ([]catalog.Feature, []string) {
	rows := make([]catalog.Feature, p.features)
	classes := []string{"alpha", "beta", "gamma", "delta"}
	for i := range rows {
		shape := randomShape(rng, p)
		rows[i] = catalog.Feature{
			ID:    featureID(i),
			Class: classes[rng.Intn(len(classes))],
			Shape: shape,
		}
	}
	return rows, classes
}

func randomShape(rng *rand.Rand, p genProfile) plane.Shape {
	x0 := boundedCoordinate(rng, p.span)
	y0 := boundedCoordinate(rng, p.span)
	if !p.extents {
		return plane.Shape{Mark: &plane.Point{X: x0, Y: y0}}
	}
	// Positive-width rectangles only, matching the public shape contract.
	dx := int64(1 + rng.Intn(5))
	dy := int64(1 + rng.Intn(5))
	x1 := x0 + dx
	y1 := y0 + dy
	if x1 > plane.CoordinateLimit {
		x0, x1 = plane.CoordinateLimit-dx, plane.CoordinateLimit
	}
	if y1 > plane.CoordinateLimit {
		y0, y1 = plane.CoordinateLimit-dy, plane.CoordinateLimit
	}
	return plane.Shape{Extent: &plane.Box{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}}
}

func boundedCoordinate(rng *rand.Rand, span int64) int64 {
	switch rng.Intn(10) {
	case 0:
		return -plane.CoordinateLimit
	case 1:
		return plane.CoordinateLimit
	case 2:
		return 0
	default:
		return rng.Int63n(2*span+1) - span
	}
}

func generateCase(rng *rand.Rand, p genProfile, rows []catalog.Feature,
	classes []string, treeIndex, queryIndex int) diffCase {
	var point diffPoint
	switch rng.Intn(3) {
	case 0:
		// Aim at an existing envelope so zero-distance ties are common.
		base := rows[rng.Intn(len(rows))].Shape.Envelope()
		point = diffPoint{
			X: base.MinX + int64(rng.Intn(3)) - 1,
			Y: base.MinY + int64(rng.Intn(3)) - 1,
		}
	case 1:
		point = diffPoint{X: boundedCoordinate(rng, p.span), Y: boundedCoordinate(rng, p.span)}
	default:
		// Off-grid points make exact ties less likely, exercising ordering.
		point = diffPoint{X: rng.Int63n(2*p.span+1) - p.span, Y: rng.Int63n(2*p.span+1) - p.span}
	}

	limit := 1 + rng.Intn(32)
	if rng.Intn(8) == 0 {
		limit = 128 // public maximum, often above the feature count
	}

	class := ""
	switch rng.Intn(4) {
	case 0:
		class = classes[rng.Intn(len(classes))]
	case 1:
		class = "absent" // guaranteed zero survivors
	}

	return diffCase{
		Name:     p.name + "-t" + strconv.Itoa(treeIndex) + "-q" + strconv.Itoa(queryIndex),
		Seed:     frozenSeed,
		Features: fromCatalogFeatures(rows),
		Point:    point,
		Limit:    limit,
		Class:    class,
	}
}

func fromCatalogFeatures(rows []catalog.Feature) []diffFeature {
	out := make([]diffFeature, 0, len(rows))
	for _, row := range rows {
		item := diffFeature{ID: row.ID, Class: row.Class}
		if row.Shape.Mark != nil {
			item.Shape.Mark = &diffPoint{X: row.Shape.Mark.X, Y: row.Shape.Mark.Y}
		} else {
			b := row.Shape.Extent
			item.Shape.Extent = &diffBox{MinX: b.MinX, MinY: b.MinY, MaxX: b.MaxX, MaxY: b.MaxY}
		}
		out = append(out, item)
	}
	return out
}

func featureID(i int) string {
	// Names stay within the catalog name grammar [a-z][a-z0-9-]{0,47}.
	return "row-" + strconv.Itoa(i)
}

// TestNearestReplay reruns a single standalone input. Point it at a frozen
// fixture or at an artifact the harness wrote on failure:
//
//	MERIDIAN_NEAREST_REPLAY=/path/to/case.json go test ./internal/packed/ -run TestNearestReplay -v
func TestNearestReplay(t *testing.T) {
	path := os.Getenv("MERIDIAN_NEAREST_REPLAY")
	if path == "" {
		t.Skip("set MERIDIAN_NEAREST_REPLAY to a fixture or failure artifact to replay")
	}
	cases, err := loadReplay(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Limit < 1 {
			t.Fatalf("replay input must carry a positive limit, got %d", c.Limit)
		}
		runDifferential(t, "replay:"+filepath.Base(path)+":"+c.Name, c.Seed, c)
	}
}
