package workorder

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FromImage converts a decoded MPB payload (as returned by GET /image) into a
// comparable world, so the live verifier hashes exactly what the server
// stored rather than what it intended to store.
func FromImage(image catalog.CatalogImage) *World {
	w := &World{Layers: make(map[string]*Layer, len(image.Layers))}
	for _, entry := range image.Layers {
		l := &Layer{Def: entry.Definition, Rows: make(map[string]catalog.Feature, len(entry.Features))}
		for _, feature := range entry.Features {
			l.Rows[feature.ID] = feature.Copy()
		}
		w.Layers[entry.Definition.Name] = l
	}
	return w
}

func sortedLines(lines []int) []int {
	ordered := append([]int(nil), lines...)
	sort.Ints(ordered)
	return ordered
}
