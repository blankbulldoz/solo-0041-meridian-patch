// Package packed builds and searches immutable bounding-box trees.
package packed

import (
	"cmp"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const leafSize = 12

type Tree struct {
	root *node
}

type node struct {
	bounds plane.Box
	left   *node
	right  *node
	rows   []catalog.Feature
}

func Build(rows []catalog.Feature) Tree {
	return Tree{root: pack(slices.Clone(rows))}
}

func pack(rows []catalog.Feature) *node {
	if len(rows) == 0 {
		return nil
	}
	bounds := rows[0].Shape.Envelope()
	for _, row := range rows[1:] {
		bounds = bounds.Union(row.Shape.Envelope())
	}
	if len(rows) <= leafSize {
		return &node{bounds: bounds, rows: rows}
	}
	xAxis := bounds.MaxX-bounds.MinX >= bounds.MaxY-bounds.MinY
	slices.SortFunc(rows, func(a, b catalog.Feature) int {
		if order := cmp.Compare(center(a.Shape.Envelope(), xAxis), center(b.Shape.Envelope(), xAxis)); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	mid := len(rows) / 2
	return &node{bounds: bounds, left: pack(rows[:mid]), right: pack(rows[mid:])}
}

func center(b plane.Box, xAxis bool) int64 {
	if xAxis {
		return b.MinX + b.MaxX
	}
	return b.MinY + b.MaxY
}

type Visit struct {
	Nodes      int `json:"nodes"`
	Candidates int `json:"candidates"`
}

func (t Tree) Search(bounds plane.Box, accept func(catalog.Feature) bool) Visit {
	var visit Visit
	search(t.root, bounds, accept, &visit)
	return visit
}

func search(n *node, bounds plane.Box, accept func(catalog.Feature) bool, visit *Visit) bool {
	if n == nil {
		return true
	}
	visit.Nodes++
	if !n.bounds.MayMeet(bounds) {
		return true
	}
	for _, row := range n.rows {
		if row.Shape.Envelope().MayMeet(bounds) {
			visit.Candidates++
			if !accept(row) {
				return false
			}
		}
	}
	return search(n.left, bounds, accept, visit) && search(n.right, bounds, accept, visit)
}
