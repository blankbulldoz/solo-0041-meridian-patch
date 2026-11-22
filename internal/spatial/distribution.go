package spatial

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/packed"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const (
	// MaxDistributionCells bounds the number of grid cells a single query may
	// report. Empty cells are reported too, so the bound also bounds the
	// response size for windows that contain no features.
	MaxDistributionCells = 16_384
	// maxDistributionEntries bounds the number of clipped feature pieces that
	// are grouped and hashed. One rectangle can touch many cells.
	maxDistributionEntries = 200_000
)

var ErrDistributionCapacity = errors.New("spatial distribution exceeds cell or entry budget")

// DistributionGrid identifies the integer lattice used for grouping. Cell
// (column, row) is [origin.X+column*Cell, origin.X+(column+1)*Cell) on the
// x-axis, and likewise on y.
type DistributionGrid struct {
	Origin plane.Point `json:"origin"`
	Cell   int64       `json:"cell"`
}

// DistributionCell is one grid position. Bounds is the complete grid cell,
// even when the query window clips it. Count and Digest describe only the
// window-clipped pieces of matching features that fall in the cell.
type DistributionCell struct {
	Column int64     `json:"column"`
	Row    int64     `json:"row"`
	Bounds plane.Box `json:"bounds"`
	Count  int       `json:"count"`
	Digest string    `json:"digest"`
}

type DistributionResult struct {
	Grid        DistributionGrid   `json:"grid"`
	Bounds      plane.Box          `json:"bounds"`
	Cells       []DistributionCell `json:"cells"`
	EmptyDigest string             `json:"empty_digest"`
	Digest      string             `json:"digest"`
	Visit       packed.Visit       `json:"visit"`
}

type cellKey struct {
	column int64
	row    int64
}

// Distribution groups matching features by grid cell. Each extent contributes
// only its clip to that cell (and to the query window); a mark contributes its
// point to exactly one cell under half-open ownership. IDs, layer names and
// revisions are deliberately excluded from per-cell digests so two processes
// holding different database revisions can compare identical spatial content.
func Distribution(tree packed.Tree, window plane.Box, class string, grid DistributionGrid) (DistributionResult, error) {
	if grid.Cell < 1 {
		return DistributionResult{}, ErrDistributionCapacity
	}
	firstColumn := floorDiv(window.MinX-grid.Origin.X, grid.Cell)
	lastColumn := floorDiv(window.MaxX-1-grid.Origin.X, grid.Cell)
	firstRow := floorDiv(window.MinY-grid.Origin.Y, grid.Cell)
	lastRow := floorDiv(window.MaxY-1-grid.Origin.Y, grid.Cell)
	columns := lastColumn - firstColumn + 1
	rows := lastRow - firstRow + 1
	if columns <= 0 || rows <= 0 || columns > MaxDistributionCells/rows {
		return DistributionResult{}, ErrDistributionCapacity
	}

	contents := make(map[cellKey][][]byte)
	entries := 0
	add := func(key cellKey, class string, shape plane.Shape) bool {
		if entries == maxDistributionEntries {
			return false
		}
		encoded, err := json.Marshal(canonicalEntry{Class: class, Shape: shape})
		if err != nil {
			return false
		}
		contents[key] = append(contents[key], encoded)
		entries++
		return true
	}

	overflow := false
	visit := tree.Search(window, func(row catalog.Feature) bool {
		if class != "" && row.Class != class {
			return true
		}
		if row.Shape.Mark != nil {
			p := *row.Shape.Mark
			if !window.Owns(p) {
				return true
			}
			key := cellKey{
				column: floorDiv(p.X-grid.Origin.X, grid.Cell),
				row:    floorDiv(p.Y-grid.Origin.Y, grid.Cell),
			}
			if !add(key, row.Class, row.Shape) {
				overflow = true
				return false
			}
			return true
		}
		piece, visible := row.Shape.Extent.Clip(window)
		if !visible {
			return true
		}
		minColumn := floorDiv(piece.MinX-grid.Origin.X, grid.Cell)
		maxColumn := floorDiv(piece.MaxX-1-grid.Origin.X, grid.Cell)
		minRow := floorDiv(piece.MinY-grid.Origin.Y, grid.Cell)
		maxRow := floorDiv(piece.MaxY-1-grid.Origin.Y, grid.Cell)
		for column := minColumn; column <= maxColumn; column++ {
			for index := minRow; index <= maxRow; index++ {
				clip, present := piece.Clip(cellBounds(grid, column, index))
				if !present {
					continue
				}
				if !add(cellKey{column: column, row: index}, row.Class, plane.Shape{Extent: &clip}) {
					overflow = true
					return false
				}
			}
		}
		return true
	})
	if overflow {
		return DistributionResult{}, ErrDistributionCapacity
	}

	emptyDigest := hashEntries(nil)
	result := DistributionResult{
		Grid:        grid,
		Bounds:      window,
		Cells:       make([]DistributionCell, 0, columns*rows),
		EmptyDigest: emptyDigest,
		Visit:       visit,
	}
	for row := firstRow; row <= lastRow; row++ {
		for column := firstColumn; column <= lastColumn; column++ {
			pieces := contents[cellKey{column: column, row: row}]
			sort.Slice(pieces, func(i, j int) bool { return bytes.Compare(pieces[i], pieces[j]) < 0 })
			cell := DistributionCell{
				Column: column,
				Row:    row,
				Bounds: cellBounds(grid, column, row),
				Count:  len(pieces),
				Digest: hashEntries(pieces),
			}
			result.Cells = append(result.Cells, cell)
		}
	}
	raw, err := json.Marshal(struct {
		Grid        DistributionGrid   `json:"grid"`
		Bounds      plane.Box          `json:"bounds"`
		Cells       []DistributionCell `json:"cells"`
		EmptyDigest string             `json:"empty_digest"`
	}{grid, window, result.Cells, emptyDigest})
	if err != nil {
		return DistributionResult{}, err
	}
	result.Digest = hashBytes(raw)
	return result, nil
}

// canonicalEntry fixes the digest payload: feature IDs, revisions and any
// process-local metadata are intentionally absent.
type canonicalEntry struct {
	Class string      `json:"class"`
	Shape plane.Shape `json:"shape"`
}

func hashEntries(pieces [][]byte) string {
	total := 0
	for _, piece := range pieces {
		total += len(piece) + 1
	}
	if total == 0 {
		return hashBytes(nil)
	}
	joined := make([]byte, 0, total-1)
	for index, piece := range pieces {
		if index != 0 {
			joined = append(joined, '\n')
		}
		joined = append(joined, piece...)
	}
	return hashBytes(joined)
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cellBounds(grid DistributionGrid, column, row int64) plane.Box {
	minX := grid.Origin.X + column*grid.Cell
	minY := grid.Origin.Y + row*grid.Cell
	return plane.Box{MinX: minX, MinY: minY, MaxX: minX + grid.Cell, MaxY: minY + grid.Cell}
}

// floorDiv rounds toward negative infinity, keeping cells half-open for
// coordinates below the grid origin as well.
func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value%divisor != 0 && (value < 0) != (divisor < 0) {
		quotient--
	}
	return quotient
}
