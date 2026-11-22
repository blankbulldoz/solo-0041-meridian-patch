package delta

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func extent(id string, x0, y0, x1, y1 int64) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture", Shape: plane.Shape{Extent: &plane.Box{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}}}
}

func mark(id string, x, y int64) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture", Shape: plane.Shape{Mark: &plane.Point{X: x, Y: y}}}
}

func layer(name string, kind plane.Kind, bounds plane.Box, exclusive bool, revision uint64, features ...catalog.Feature) catalog.LayerImage {
	return catalog.LayerImage{
		Definition: catalog.Definition{Name: name, Kind: kind, Layout: catalog.Layout{Bounds: bounds, Exclusive: exclusive}},
		Revision:   revision,
		Features:   features,
	}
}

var baseBounds = plane.Box{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}
var wideBounds = plane.Box{MinX: 0, MinY: 0, MaxX: 12, MaxY: 12}

func baseImage() catalog.CatalogImage {
	return catalog.CatalogImage{
		Revision: 3,
		Layers: []catalog.LayerImage{
			layer("alpha", plane.Extent, baseBounds, false, 3,
				extent("a", 0, 0, 2, 2), extent("b", 3, 3, 5, 5), extent("c", 6, 6, 8, 8)),
			layer("gamma", plane.Mark, baseBounds, true, 2, mark("g1", 1, 1)),
		},
	}
}

func targetImage() catalog.CatalogImage {
	return catalog.CatalogImage{
		Revision: 7,
		Layers: []catalog.LayerImage{
			layer("alpha", plane.Extent, wideBounds, false, 7,
				extent("a", 1, 1, 3, 3), extent("c", 6, 6, 8, 8), extent("d", 9, 9, 11, 11)),
			layer("beta", plane.Mark, baseBounds, false, 7, mark("p1", 4, 4)),
		},
	}
}

func findLayer(t *testing.T, patch Patch, name string) LayerPatch {
	t.Helper()
	for _, entry := range patch.Layers {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("layer patch %q missing", name)
	return LayerPatch{}
}

func TestDiffCapturesAllChangeKinds(t *testing.T) {
	patch, err := Diff(baseImage(), targetImage())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if patch.Format != 1 || patch.BaselineRevision != 3 || patch.TargetRevision != 7 {
		t.Fatalf("unexpected patch header: %+v", patch)
	}
	if len(patch.Layers) != 3 || patch.Layers[0].Name != "alpha" || patch.Layers[1].Name != "beta" || patch.Layers[2].Name != "gamma" {
		t.Fatalf("layer entries must be sorted: %+v", patch.Layers)
	}
	alpha := findLayer(t, patch, "alpha")
	if alpha.Definition == nil || alpha.Definition.Bounds != wideBounds {
		t.Fatalf("layout change missing: %+v", alpha.Definition)
	}
	modes := map[string]string{}
	for _, op := range alpha.Ops {
		modes[op.ID] = op.Mode
	}
	if modes["a"] != "replace" || modes["b"] != "remove" || modes["d"] != "insert" || modes["c"] != "" {
		t.Fatalf("unexpected alpha ops: %+v", alpha.Ops)
	}
	beta := findLayer(t, patch, "beta")
	if beta.Definition == nil || len(beta.Ops) != 1 || beta.Ops[0].Mode != "insert" {
		t.Fatalf("new layer must arrive with definition and inserts: %+v", beta)
	}
	gamma := findLayer(t, patch, "gamma")
	if !gamma.Drop || len(gamma.Ops) != 0 {
		t.Fatalf("dropped layer must carry drop flag only: %+v", gamma)
	}
}

func TestApplyRoundTripReconstructsTarget(t *testing.T) {
	base := baseImage()
	target := targetImage()
	patch, err := Diff(base, target)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	result, changed, err := Apply(base, patch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	gotDigest, err := ContentDigest(result)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	wantDigest, err := ContentDigest(target)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("reconstructed content %s != target %s", gotDigest, wantDigest)
	}
	if strings.Join(changed, ",") != "alpha,beta,gamma" {
		t.Fatalf("changed layers: %v", changed)
	}
	if err := validateContent(result); err != nil {
		t.Fatalf("reconstructed image invalid: %v", err)
	}
	// A reconstructed image carries no origin revisions; the importer assigns them.
	if result.Revision != 0 || result.Layers[0].Revision != 0 {
		t.Fatalf("reconstructed image must start at revision zero, got db=%d layer=%d",
			result.Revision, result.Layers[0].Revision)
	}
}

func TestContainerRoundTripIsCanonicalAndTamperEvident(t *testing.T) {
	patch, err := Diff(baseImage(), targetImage())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	raw, err := Encode(patch)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasPrefix(raw, signature[:]) {
		t.Fatal("missing MPC signature")
	}
	again, _ := Encode(patch)
	if !bytes.Equal(raw, again) {
		t.Fatal("patch encoding is not deterministic")
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rebaked, _ := json.Marshal(decoded)
	if !bytes.Contains(raw, rebaked[:min(32, len(rebaked))]) {
		t.Fatal("decoded patch diverged from encoding")
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := Decode(corrupt); err == nil {
		t.Fatal("checksum corruption was not rejected")
	}
}

func TestForeignImageWithSameRevisionRejected(t *testing.T) {
	patch, err := Diff(baseImage(), targetImage())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	foreign := baseImage()
	foreign.Layers[0].Features[0] = extent("a", 0, 0, 9, 9) // same revision 3, different content
	if foreign.Revision != patch.BaselineRevision {
		t.Fatal("test setup must keep the same revision number")
	}
	if _, _, err := Apply(foreign, patch); err == nil {
		t.Fatal("package applied to a different same-revision image")
	} else if problem, ok := err.(*Error); !ok || problem.Code != "baseline_mismatch" {
		t.Fatalf("want baseline_mismatch, got %v", err)
	}
}

func TestReplayOnAlreadyTargetedContent(t *testing.T) {
	base := baseImage()
	patch, err := Diff(base, targetImage())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	target, _, err := Apply(base, patch)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Replaying the same package against the resulting image cannot match the
	// baseline digest; the database layer turns this into an explicit outcome.
	_, _, err = Apply(target, patch)
	if problem, ok := err.(*Error); !ok || problem.Code != "baseline_mismatch" {
		t.Fatalf("replay must report a baseline mismatch at the package layer, got %v", err)
	}
}

func TestTamperedTargetDigestRejected(t *testing.T) {
	patch, err := Diff(baseImage(), targetImage())
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	patch.TargetDigest = strings.Repeat("0", DigestLength)
	if _, _, err := Apply(baseImage(), patch); err == nil {
		t.Fatal("patched content with a bogus target digest was accepted")
	} else if problem, ok := err.(*Error); !ok || problem.Code != "content_mismatch" {
		t.Fatalf("want content_mismatch, got %v", err)
	}
}

func TestDigestIgnoresRevisionsAndEmptyDiff(t *testing.T) {
	left := baseImage()
	right := baseImage()
	right.Revision = 99
	right.Layers[0].Revision = 55
	right.Layers[1].Revision = 44
	d1, _ := ContentDigest(left)
	d2, _ := ContentDigest(right)
	if d1 != d2 {
		t.Fatal("identical content at different revisions must share a digest")
	}
	patch, err := Diff(left, right)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(patch.Layers) != 0 {
		t.Fatalf("identical content must produce an empty package, got %+v", patch.Layers)
	}
}

func TestKindChangeRefused(t *testing.T) {
	target := targetImage()
	target.Layers[0].Definition.Kind = plane.Mark
	if _, err := Diff(baseImage(), target); err == nil {
		t.Fatal("geometry kind change must not be expressible as a patch")
	}
}

func TestWholeLayerDropRoundTrip(t *testing.T) {
	base := baseImage()
	target := catalog.CatalogImage{
		Revision: 9,
		Layers:   []catalog.LayerImage{base.Layers[0]},
	}
	target.Layers[0].Revision = 9
	patch, err := Diff(base, target)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(patch.Layers) != 1 || !patch.Layers[0].Drop {
		t.Fatalf("expected a single drop entry, got %+v", patch.Layers)
	}
	result, changed, err := Apply(base, patch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Layers) != 1 || result.Layers[0].Definition.Name != "alpha" {
		t.Fatalf("dropped layer must be absent from the result: %+v", result.Layers)
	}
	if changed[0] != "gamma" {
		t.Fatalf("changed report must name the dropped layer, got %v", changed)
	}
	if digest, _ := ContentDigest(result); digest != patch.TargetDigest {
		t.Fatal("dropped-layer result must match the recorded target digest")
	}
}

func TestMalformedPackageRejected(t *testing.T) {
	patch, _ := Diff(baseImage(), targetImage())
	patch.Format = 2
	raw, err := Encode(patch)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Decode(raw); err == nil {
		t.Fatal("unsupported format accepted")
	}
}
