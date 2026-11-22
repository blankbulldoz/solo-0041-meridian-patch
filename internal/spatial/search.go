// Package spatial translates index candidates into spatial query results.
package spatial

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// Order selects the window result order. OrderID is the original order and
// OrderYX sorts by representative coordinate, y before x, then by ID.
type Order string

const (
	OrderID Order = "id"
	OrderYX Order = "yx"
)

// ParseOrder maps a request value to a canonical order. An omitted value keeps
// the historical ID order so existing clients and bookmarks stay valid.
func ParseOrder(value string) (Order, error) {
	switch Order(value) {
	case "":
		return OrderID, nil
	case OrderID, OrderYX:
		return Order(value), nil
	default:
		return "", fmt.Errorf("window order must be %q or %q", OrderID, OrderYX)
	}
}

// Cursor is the position of the last returned feature. Y and X are the
// representative coordinate of its shape inside the query window; ID breaks
// ties between features sharing that coordinate.
type Cursor struct {
	Y  int64  `json:"y"`
	X  int64  `json:"x"`
	ID string `json:"id"`
}

// CursorAt derives the continuation cursor for one feature and window.
func CursorAt(shape plane.Shape, window plane.Box, id string) Cursor {
	anchor := shape.Anchor(window)
	return Cursor{Y: anchor.Y, X: anchor.X, ID: id}
}

type MatchSet struct {
	Features []catalog.Feature `json:"features"`
	Visit    packed.Visit      `json:"visit"`
}

// Window returns matching features in the requested order. A nil after starts
// at the beginning; a non-nil one resumes strictly after that cursor.
func Window(tree packed.Tree, bounds plane.Box, class string, order Order, after *Cursor) MatchSet {
	return matches(tree, bounds, func(row catalog.Feature) bool {
		return (class == "" || row.Class == class) && row.Shape.InWindow(bounds) &&
			(after == nil || beyond(row, bounds, order, *after))
	}, func(a, b catalog.Feature) int {
		return compare(a, b, bounds, order)
	})
}

func Locate(tree packed.Tree, p plane.Point) MatchSet {
	return matches(tree, p.Envelope(), func(row catalog.Feature) bool {
		return row.Shape.At(p)
	}, func(a, b catalog.Feature) int {
		return cmp.Compare(a.ID, b.ID)
	})
}

// beyond reports whether row follows the cursor in the requested order. The
// anchor depends on the window, so callers must not reuse a cursor returned for
// a different window; bookmarks enforce that by binding the whole scope.
func beyond(row catalog.Feature, bounds plane.Box, order Order, after Cursor) bool {
	if order == OrderYX {
		switch anchor := row.Shape.Anchor(bounds); {
		case anchor.Y != after.Y:
			return anchor.Y > after.Y
		case anchor.X != after.X:
			return anchor.X > after.X
		}
	}
	return row.ID > after.ID
}

func compare(a, b catalog.Feature, bounds plane.Box, order Order) int {
	if order == OrderYX {
		left, right := a.Shape.Anchor(bounds), b.Shape.Anchor(bounds)
		if result := cmp.Compare(left.Y, right.Y); result != 0 {
			return result
		}
		if result := cmp.Compare(left.X, right.X); result != 0 {
			return result
		}
	}
	return cmp.Compare(a.ID, b.ID)
}

func matches(tree packed.Tree, envelope plane.Box, accept func(catalog.Feature) bool, order func(a, b catalog.Feature) int) MatchSet {
	rows := make([]catalog.Feature, 0)
	visit := tree.Search(envelope, func(row catalog.Feature) bool {
		if accept(row) {
			rows = append(rows, row)
		}
		return true
	})
	slices.SortFunc(rows, order)
	return MatchSet{Features: rows, Visit: visit}
}
