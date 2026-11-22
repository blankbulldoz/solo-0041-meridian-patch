// Package spatial translates index candidates into spatial query results.
package spatial

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

type MatchSet struct {
	Features []catalog.Feature `json:"features"`
	Visit    packed.Visit      `json:"visit"`
}

// WindowResult reports the page together with category-filtering cost. The
// index prunes by geometry only, so ClassRejected counts candidates that pass
// the window and cursor but are then dropped by the class predicate; a long
// exclude list therefore shows up as filtering work, not as fewer candidates.
type WindowResult struct {
	MatchSet
	ClassRejected int
}

func Window(tree packed.Tree, bounds plane.Box, classes catalog.ClassFilter, after string) WindowResult {
	var rejected int
	match := matches(tree, bounds, func(row catalog.Feature) bool {
		if row.ID <= after || !row.Shape.InWindow(bounds) {
			return false
		}
		if !classes.Matches(row.Class) {
			rejected++
			return false
		}
		return true
	})
	return WindowResult{MatchSet: match, ClassRejected: rejected}
}

func Locate(tree packed.Tree, p plane.Point) MatchSet {
	return matches(tree, p.Envelope(), func(row catalog.Feature) bool {
		return row.Shape.At(p)
	})
}

func matches(tree packed.Tree, envelope plane.Box, accept func(catalog.Feature) bool) MatchSet {
	rows := make([]catalog.Feature, 0)
	visit := tree.Search(envelope, func(row catalog.Feature) bool {
		if accept(row) {
			rows = append(rows, row)
		}
		return true
	})
	slices.SortFunc(rows, func(a, b catalog.Feature) int { return cmp.Compare(a.ID, b.ID) })
	return MatchSet{Features: rows, Visit: visit}
}
