package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// File names inside an unpacked archive directory.
const (
	ManifestFile = "manifest.json"
	LayersFile   = "layers.csv"
	FeaturesFile = "features.csv"
	ClassesFile  = "classes.csv"
)

var (
	layersHeader   = []string{"layer", "kind", "min_x", "min_y", "max_x", "max_y", "exclusive", "layer_revision", "feature_count", "row_digest"}
	featuresHeader = []string{"layer", "feature_id", "class", "shape", "min_x", "min_y", "max_x", "max_y", "area", "origin_distance_squared", "row_digest"}
	classesHeader  = []string{"class", "kind", "layers", "feature_count", "total_area", "max_origin_distance_squared", "group_digest"}
)

// integerEncoding documents how the archive keeps 64-bit and larger values
// exact for consumers limited to the JavaScript safe integer range.
const integerEncoding = "revisions and coordinate-derived quantities are decimal strings; values may exceed 2^53-1"

type manifest struct {
	Format           string     `json:"format"`
	Source           sourceInfo `json:"source"`
	DatabaseRevision string     `json:"database_revision"`
	IntegerEncoding  string     `json:"integer_encoding"`
	Layers           int        `json:"layers"`
	Features         int        `json:"features"`
	TotalExtentArea  string     `json:"total_extent_area"`
	Files            []fileInfo `json:"files"`
}

type sourceInfo struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type fileInfo struct {
	Path   string `json:"path"`
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
}

// classGroup accumulates the per-(class, kind) verification summary.
type classGroup struct {
	class    string
	kind     plane.Kind
	layers   map[string]bool
	count    int
	total    *big.Int
	maxOdist int64
	members  []string
}

// classGroups folds every feature row into its (class, kind) summary group.
// The result is sorted by class then kind, and member digests are sorted so
// the group checksum does not depend on row order.
func classGroups(m *Model) []*classGroup {
	groups := make(map[string]*classGroup)
	for _, l := range m.Layers {
		kind := l.Definition.Kind
		for _, f := range l.Features {
			key := f.Class + "\x00" + string(kind)
			g := groups[key]
			if g == nil {
				g = &classGroup{class: f.Class, kind: kind, layers: make(map[string]bool), total: new(big.Int)}
				groups[key] = g
			}
			g.layers[l.Definition.Name] = true
			g.count++
			g.total.Add(g.total, big.NewInt(Area(f)))
			if odist := OriginDistanceSquared(f); odist > g.maxOdist {
				g.maxOdist = odist
			}
			g.members = append(g.members, FeatureDigest(l.Definition.Name, f))
		}
	}
	ordered := make([]*classGroup, 0, len(groups))
	for _, g := range groups {
		slices.Sort(g.members)
		ordered = append(ordered, g)
	}
	slices.SortFunc(ordered, func(a, b *classGroup) int {
		if a.class != b.class {
			return strings.Compare(a.class, b.class)
		}
		return strings.Compare(string(a.kind), string(b.kind))
	})
	return ordered
}

func (g *classGroup) row() []string {
	layers := make([]string, 0, len(g.layers))
	for name := range g.layers {
		layers = append(layers, name)
	}
	slices.Sort(layers)
	return []string{
		g.class, string(g.kind), strings.Join(layers, ";"),
		strconv.Itoa(g.count), g.total.String(), strconv.FormatInt(g.maxOdist, 10),
		ClassDigest(g.class, g.kind, g.members),
	}
}

func layerRow(l Layer) []string {
	b := l.Definition.Bounds
	return []string{
		l.Definition.Name, string(l.Definition.Kind),
		strconv.FormatInt(b.MinX, 10), strconv.FormatInt(b.MinY, 10),
		strconv.FormatInt(b.MaxX, 10), strconv.FormatInt(b.MaxY, 10),
		strconv.FormatBool(l.Definition.Exclusive),
		strconv.FormatUint(l.Revision, 10), strconv.Itoa(len(l.Features)),
		LayerDigest(l.Definition),
	}
}

func featureRow(layer string, kind plane.Kind, f catalog.Feature) []string {
	env := Envelope(f)
	return []string{
		layer, f.ID, f.Class, string(kind),
		strconv.FormatInt(env.MinX, 10), strconv.FormatInt(env.MinY, 10),
		strconv.FormatInt(env.MaxX, 10), strconv.FormatInt(env.MaxY, 10),
		strconv.FormatInt(Area(f), 10),
		strconv.FormatInt(OriginDistanceSquared(f), 10),
		FeatureDigest(layer, f),
	}
}

func writeCSV(header []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Unpack decodes and validates raw MPB v1 bytes and writes a plain-data
// archive into dir: a layer table, a feature table, a per-(class, kind)
// verification summary, and a manifest binding them together. The output is
// deterministic: the same image always produces byte-identical files.
func Unpack(raw []byte, sourceName, dir string) (*Model, error) {
	m, err := Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("output directory %s is not empty", dir)
	}

	layerRows := make([][]string, 0, len(m.Layers))
	var featureRows [][]string
	totalArea := new(big.Int)
	for _, l := range m.Layers {
		layerRows = append(layerRows, layerRow(l))
		for _, f := range l.Features {
			featureRows = append(featureRows, featureRow(l.Definition.Name, l.Definition.Kind, f))
			totalArea.Add(totalArea, big.NewInt(Area(f)))
		}
	}
	groupRows := make([][]string, 0)
	for _, g := range classGroups(m) {
		groupRows = append(groupRows, g.row())
	}

	layersCSV, err := writeCSV(layersHeader, layerRows)
	if err != nil {
		return nil, err
	}
	featuresCSV, err := writeCSV(featuresHeader, featureRows)
	if err != nil {
		return nil, err
	}
	classesCSV, err := writeCSV(classesHeader, groupRows)
	if err != nil {
		return nil, err
	}

	man := manifest{
		Format:           Format,
		Source:           sourceInfo{Name: sourceName, Bytes: len(raw), SHA256: sha256Hex(raw)},
		DatabaseRevision: strconv.FormatUint(m.Revision, 10),
		IntegerEncoding:  integerEncoding,
		Layers:           len(m.Layers),
		Features:         m.Features(),
		TotalExtentArea:  totalArea.String(),
	}
	files := []struct {
		path string
		rows int
		data []byte
	}{
		{LayersFile, len(layerRows), layersCSV},
		{FeaturesFile, len(featureRows), featuresCSV},
		{ClassesFile, len(groupRows), classesCSV},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.path), f.data, 0o644); err != nil {
			return nil, err
		}
		man.Files = append(man.Files, fileInfo{Path: f.path, Rows: f.rows, SHA256: sha256Hex(f.data)})
	}
	manifestJSON, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), append(manifestJSON, '\n'), 0o644); err != nil {
		return nil, err
	}
	return m, nil
}
