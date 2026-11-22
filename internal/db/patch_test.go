package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/delta"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func extentFeature(id string, x0, y0, x1, y1 int64) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture", Shape: plane.Shape{Extent: &plane.Box{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}}}
}

func markFeature(id string, x, y int64) catalog.Feature {
	return catalog.Feature{ID: id, Class: "fixture", Shape: plane.Shape{Mark: &plane.Point{X: x, Y: y}}}
}

func seedDatabase(t *testing.T, dir string) *Database {
	t.Helper()
	d, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	bounds := plane.Box{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20}
	if _, err := d.Register(catalog.Definition{Name: "alpha", Kind: plane.Extent, Layout: catalog.Layout{Bounds: bounds}}, nil); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if _, err := d.Register(catalog.Definition{Name: "pins", Kind: plane.Mark, Layout: catalog.Layout{Bounds: bounds, Exclusive: true}}, nil); err != nil {
		t.Fatalf("register pins: %v", err)
	}
	if _, err := d.Edit(catalog.EditBatch{Edits: []catalog.Edit{
		{Layer: "alpha", ID: "a", Mode: "insert", Value: ptr(extentFeature("a", 0, 0, 4, 4))},
		{Layer: "alpha", ID: "b", Mode: "insert", Value: ptr(extentFeature("b", 5, 5, 9, 9))},
		{Layer: "pins", ID: "p1", Mode: "insert", Value: ptr(markFeature("p1", 2, 2))},
	}}); err != nil {
		t.Fatalf("seed edits: %v", err)
	}
	return d
}

func ptr(f catalog.Feature) *catalog.Feature { return &f }

func TestApplyPatchRoundTripAndReplay(t *testing.T) {
	d := seedDatabase(t, t.TempDir())
	baselineRaw, baselineRev, err := d.Export()
	if err != nil {
		t.Fatalf("export baseline: %v", err)
	}

	// Evolve the live database: replacement, removal, layout change and a new layer.
	if _, err := d.Edit(catalog.EditBatch{Edits: []catalog.Edit{
		{Layer: "alpha", ID: "a", Mode: "replace", Value: ptr(extentFeature("a", 1, 1, 4, 4))},
		{Layer: "alpha", ID: "b", Mode: "remove"},
	}}); err != nil {
		t.Fatalf("edits: %v", err)
	}
	if _, err := d.Reframe("alpha", catalog.Layout{
		Bounds:    plane.Box{MinX: 0, MinY: 0, MaxX: 24, MaxY: 24},
		Exclusive: false,
	}, nil); err != nil {
		t.Fatalf("reframe: %v", err)
	}
	if _, err := d.Register(catalog.Definition{
		Name: "beta", Kind: plane.Mark,
		Layout: catalog.Layout{Bounds: plane.Box{MinX: 0, MinY: 0, MaxX: 20, MaxY: 20}},
	}, nil); err != nil {
		t.Fatalf("register beta: %v", err)
	}
	targetRaw, targetRev, err := d.Export()
	if err != nil {
		t.Fatalf("export target: %v", err)
	}
	patch, err := d.Diff(baselineRaw)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if patch.BaselineRevision != baselineRev {
		t.Fatalf("patch must record baseline revision %d, got %d", baselineRev, patch.BaselineRevision)
	}
	patchRaw, err := delta.Encode(patch)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Move a fresh copy of the baseline back to revision targetRev, then patch it.
	restored, err := d.Restore(baselineRaw, targetRev)
	if err != nil {
		t.Fatalf("restore baseline: %v", err)
	}
	receipt, err := d.ApplyPatch(patchRaw, &restored.Revision)
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	// Restore itself consumes a commit (restored.Revision is targetRev+1), then
	// the patch commit advances once more.
	if receipt.Status != "applied" || receipt.Revision != restored.Revision+1 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	gotRaw, _, err := d.Export()
	if err != nil {
		t.Fatalf("export result: %v", err)
	}
	gotDigest, _ := delta.ContentDigest(mustDecode(t, gotRaw))
	wantDigest, _ := delta.ContentDigest(mustDecode(t, targetRaw))
	if gotDigest != wantDigest {
		t.Fatalf("patched database content does not match target: %s != %s", gotDigest, wantDigest)
	}

	// Re-uploading the identical package is idempotent and visibly distinct.
	again, err := d.ApplyPatch(patchRaw, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.Status != "already_applied" || again.Revision != receipt.Revision {
		t.Fatalf("replay receipt wrong: %+v", again)
	}
}

func TestForeignSameRevisionImageRejected(t *testing.T) {
	d := seedDatabase(t, t.TempDir())
	baselineRaw, baselineRev, err := d.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := d.Edit(catalog.EditBatch{Edits: []catalog.Edit{
		{Layer: "alpha", ID: "b", Mode: "remove"},
	}}); err != nil {
		t.Fatalf("edit target: %v", err)
	}
	targetRaw, _, err := d.Export()
	if err != nil {
		t.Fatalf("export target: %v", err)
	}
	patch, err := d.Diff(baselineRaw)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	patchRaw, _ := delta.Encode(patch)

	// Build a foreign image at the SAME revision number as the recorded baseline
	// but with different catalog content, then open it in its own data directory.
	foreign := mustDecode(t, baselineRaw)
	foreign.Layers[0].Features[0] = extentFeature("a", 8, 8, 12, 12)
	foreign.Revision = baselineRev
	foreign.Layers[0].Revision = baselineRev
	foreignRaw, err := folio.Encode(foreign)
	if err != nil {
		t.Fatalf("encode foreign: %v", err)
	}
	foreignDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreignDir, "meridian-state.mpb"), foreignRaw, 0o600); err != nil {
		t.Fatalf("stage foreign image: %v", err)
	}
	other, err := Open(foreignDir)
	if err != nil {
		t.Fatalf("open foreign dir: %v", err)
	}
	defer other.Close()
	if other.Status().Revision != baselineRev {
		t.Fatalf("foreign image must keep revision %d", baselineRev)
	}
	_, err = other.ApplyPatch(patchRaw, nil)
	if problem, ok := err.(*Error); !ok || problem.Code != "baseline_mismatch" {
		t.Fatalf("want baseline_mismatch for same-revision foreign image, got %v", err)
	}

	// The foreign database must remain unchanged and still serve its content.
	stillForeign, _, _ := other.Export()
	foreignDigest, _ := delta.ContentDigest(mustDecode(t, stillForeign))
	if foreignDigest == mustDigest(t, targetRaw) {
		t.Fatal("foreign database was modified by the rejected patch")
	}
}

func TestApplyPatchCASAndCorruption(t *testing.T) {
	d := seedDatabase(t, t.TempDir())
	baselineRaw, rev, err := d.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := d.Edit(catalog.EditBatch{Edits: []catalog.Edit{
		{Layer: "pins", ID: "p1", Mode: "remove"},
	}}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	targetRaw, targetRev, err := d.Export()
	if err != nil {
		t.Fatalf("export target: %v", err)
	}
	patch, _ := d.Diff(baselineRaw)
	patchRaw, _ := delta.Encode(patch)
	if _, err := d.Restore(baselineRaw, targetRev); err != nil {
		t.Fatalf("restore: %v", err)
	}

	stale := rev + 100
	if _, err := d.ApplyPatch(patchRaw, &stale); err == nil {
		t.Fatal("stale CAS revision was accepted")
	} else if problem, ok := err.(*Error); !ok || problem.Code != "conflict" {
		t.Fatalf("want conflict for stale CAS, got %v", err)
	}
	corrupt := append([]byte(nil), patchRaw...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := d.ApplyPatch(corrupt, nil); err == nil {
		t.Fatal("corrupt package was accepted")
	} else if problem, ok := err.(*Error); !ok || problem.Code != "invalid" {
		t.Fatalf("want invalid for corrupt package, got %v", err)
	}
	receipt, err := d.ApplyPatch(patchRaw, nil)
	if err != nil {
		t.Fatalf("apply after rejections: %v", err)
	}
	if receipt.Status != "applied" {
		t.Fatalf("unexpected receipt %+v", receipt)
	}
	gotRaw, _, _ := d.Export()
	if mustDigest(t, gotRaw) != mustDigest(t, targetRaw) {
		t.Fatal("patched content diverged from target")
	}
}

func mustDecode(t *testing.T, raw []byte) catalog.CatalogImage {
	t.Helper()
	image, err := folio.Decode(raw)
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	return image
}

func mustDigest(t *testing.T, raw []byte) string {
	t.Helper()
	digest, err := delta.ContentDigest(mustDecode(t, raw))
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return digest
}
