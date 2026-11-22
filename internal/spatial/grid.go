package spatial

import (
	"errors"
	"math/bits"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// A grid partitions a query window into cells anchored at a caller-chosen
// origin. The grid lines are origin.X + k*CellWidth and origin.Y + k*CellHeight,
// for any integer k, so the anchor may lie outside the window and cell indices
// may be negative. Cells are half-open like every other rectangle; the first
// and last column or row are clipped to the window and may be incomplete.
//
// Both limits are whole-response limits: exceeding either rejects the query
// with ErrGridBudget instead of truncating the grid.
const (
	MaxGridCells      = 4096
	maxGridIncidences = 200_000
)

var ErrGridBudget = errors.New("occupancy grid exceeds cell or incidence budget")

type GridCell struct {
	Column    int       `json:"column"`
	Row       int       `json:"row"`
	Bounds    plane.Box `json:"bounds"`
	Area      int64     `json:"area"`
	Covered   int64     `json:"covered"`
	Uncovered int64     `json:"uncovered"`
	Occupancy int64     `json:"occupancy_permille"`
	Vacant    bool      `json:"vacant"`
}

type GridTotals struct {
	Cells       int   `json:"cells"`
	Area        int64 `json:"area"`
	Covered     int64 `json:"covered"`
	Occupancy   int64 `json:"occupancy_permille"`
	VacantCells int   `json:"vacant_cells"`
}

type GridResult struct {
	Origin     plane.Point  `json:"origin"`
	CellWidth  int64        `json:"cell_width"`
	CellHeight int64        `json:"cell_height"`
	Columns    int          `json:"columns"`
	Rows       int          `json:"rows"`
	Cells      []GridCell   `json:"cells"`
	Totals     GridTotals   `json:"totals"`
	Visit      packed.Visit `json:"visit"`
}

func Grid(tree packed.Tree, origin plane.Point, cellWidth, cellHeight int64, window plane.Box, class string) (GridResult, error) {
	firstColumn := plane.FloorDiv(window.MinX-origin.X, cellWidth)
	lastColumn := plane.FloorDiv(window.MaxX-1-origin.X, cellWidth)
	firstRow := plane.FloorDiv(window.MinY-origin.Y, cellHeight)
	lastRow := plane.FloorDiv(window.MaxY-1-origin.Y, cellHeight)
	columns := lastColumn - firstColumn + 1
	rows := lastRow - firstRow + 1
	if columns <= 0 || rows <= 0 || columns*rows > MaxGridCells {
		return GridResult{}, ErrGridBudget
	}

	cellBounds := func(column, row int64) plane.Box {
		b := plane.Box{
			MinX: max(origin.X+column*cellWidth, window.MinX),
			MinY: max(origin.Y+row*cellHeight, window.MinY),
			MaxX: min(origin.X+(column+1)*cellWidth, window.MaxX),
			MaxY: min(origin.Y+(row+1)*cellHeight, window.MaxY),
		}
		return b
	}

	total := int(columns * rows)
	pieces := make([][]plane.Box, total)
	incidences := 0
	visit := tree.Search(window, func(row catalog.Feature) bool {
		if class != "" && row.Class != class {
			return true
		}
		clipped, present := window.Clip(row.Shape.Envelope())
		if !present {
			return true
		}
		fromColumn := max(plane.FloorDiv(clipped.MinX-origin.X, cellWidth), firstColumn)
		toColumn := min(plane.FloorDiv(clipped.MaxX-1-origin.X, cellWidth), lastColumn)
		fromRow := max(plane.FloorDiv(clipped.MinY-origin.Y, cellHeight), firstRow)
		toRow := min(plane.FloorDiv(clipped.MaxY-1-origin.Y, cellHeight), lastRow)
		for column := fromColumn; column <= toColumn; column++ {
			for gridRow := fromRow; gridRow <= toRow; gridRow++ {
				part, meets := clipped.Clip(cellBounds(column, gridRow))
				if !meets {
					continue
				}
				incidences++
				if incidences > maxGridIncidences {
					return false
				}
				index := int((gridRow-firstRow)*columns + (column - firstColumn))
				pieces[index] = append(pieces[index], part)
			}
		}
		return true
	})
	if incidences > maxGridIncidences {
		return GridResult{}, ErrGridBudget
	}

	result := GridResult{
		Origin:     origin,
		CellWidth:  cellWidth,
		CellHeight: cellHeight,
		Columns:    int(columns),
		Rows:       int(rows),
		Cells:      make([]GridCell, 0, total),
		Visit:      visit,
	}
	result.Totals.Cells = total
	for row := int64(0); row < rows; row++ {
		for column := int64(0); column < columns; column++ {
			index := int(row*columns + column)
			bounds := cellBounds(firstColumn+column, firstRow+row)
			area := bounds.Area()
			covered, _ := unionArea(pieces[index])
			cell := GridCell{
				Column:    int(firstColumn + column),
				Row:       int(firstRow + row),
				Bounds:    bounds,
				Area:      area,
				Covered:   covered,
				Uncovered: area - covered,
				Occupancy: permille(covered, area),
				Vacant:    covered == 0,
			}
			result.Cells = append(result.Cells, cell)
			result.Totals.Area += area
			result.Totals.Covered += covered
			if cell.Vacant {
				result.Totals.VacantCells++
			}
		}
	}
	result.Totals.Occupancy = permille(result.Totals.Covered, result.Totals.Area)
	return result, nil
}

// permille reports part/whole in thousandths, rounded down and capped at 1000.
// bits.Mul64 keeps the product exact even when the cell area is near 2^62.
func permille(part, whole int64) int64 {
	if whole <= 0 {
		return 0
	}
	if part >= whole {
		return 1000
	}
	high, low := bits.Mul64(uint64(part), 1000)
	quotient, _ := bits.Div64(high, low, uint64(whole))
	return int64(quotient)
}
