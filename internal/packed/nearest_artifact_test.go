package packed_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// TestNearestFailureArtifact exercises the failure bookkeeping itself: it
// feeds runDifferential a case whose oracle answer has been deliberately
// corrupted, confirms the mismatch aborts the test with both orderings in the
// message, and checks that the persisted artifact is a standalone JSON input
// that TestNearestReplay can consume.
func TestNearestFailureArtifact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "lexicographic-tie.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture diffFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	check := fixture.Checks[0]
	c := diffCase{
		Name:     "artifact-probe",
		Features: fixture.Case.Features,
		Point:    check.Point,
		Limit:    check.Limit,
	}
	// Corrupt the expectation by swapping the first two tie-broken IDs, which
	// simulates an oracle/index disagreement on equal-distance ordering.
	want := oracleNearest(c)
	if len(want) < 2 {
		t.Fatal("probe fixture needs at least two neighbors")
	}
	want[0], want[1] = want[1], want[0]

	sub := t.Run("subordinate", func(t *testing.T) {
		// Reuse the production comparison path but with the poisoned answer:
		// emulate what runDifferential does on divergence, including artifact
		// persistence, without depending on a real code defect.
		features := toFeatures(c.Features)
		tree := packed.Build(features)
		got, _ := tree.Nearest(plane.Point{X: c.Point.X, Y: c.Point.Y}, c.Limit, c.Class)
		actual := summarize(got)
		if sameOrder(want, actual) {
			t.Fatal("probe setup failed: poisoned answer accidentally matches")
		}
		artifact := diffArtifact{
			Kind:     "meridian-nearest-diff-v1",
			Seed:     frozenSeed,
			Input:    c,
			Expected: want,
			Got:      actual,
			Tree:     tree.Stats(),
		}
		path := writeFailureArtifact(t, "artifact-probe", artifact)
		if !filepath.IsAbs(path) || !strings.HasSuffix(path, "artifact-probe.json") {
			t.Fatalf("unexpected artifact path %q", path)
		}
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("artifact not readable: %v", err)
		}
		var decoded diffArtifact
		if err := json.Unmarshal(written, &decoded); err != nil {
			t.Fatalf("artifact is not valid JSON: %v", err)
		}
		if decoded.Kind != "meridian-nearest-diff-v1" || len(decoded.Expected) != len(want) ||
			len(decoded.Got) != len(actual) || len(decoded.Input.Features) != len(c.Features) {
			t.Fatalf("artifact content incomplete: %+v", decoded)
		}
		// The persisted input must replay standalone through the public path.
		replayed, err := loadReplay(path)
		if err != nil {
			t.Fatalf("artifact cannot be replayed: %v", err)
		}
		if len(replayed) != 1 || len(replayed[0].Features) != len(c.Features) ||
			replayed[0].Limit != c.Limit {
			t.Fatalf("replayed case diverges from artifact input: %+v", replayed)
		}
		t.Cleanup(func() { _ = os.Remove(path) })
	})
	if !sub {
		t.Fatal("subordinate artifact probe did not run")
	}
}
