package archive

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// ReadDir loads an unpacked plain-data archive and proves it internally
// consistent: the manifest digests match every file, every row digest
// recomputes from the row content, the derived columns match the shapes, and
// the class summary matches the feature rows. The result is validated with
// the same rules the database applies to an image, so a tampered or
// truncated archive is rejected before anyone compares it.
func ReadDir(dir string) (*Model, error) {
	rawManifest, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var man manifest
	decoder := json.NewDecoder(bytes.NewReader(rawManifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&man); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	if man.Format != Format {
		return nil, fmt.Errorf("unsupported archive format %q", man.Format)
	}
	revision, err := strconv.ParseUint(man.DatabaseRevision, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("manifest database_revision is not an unsigned integer")
	}

	files := make(map[string][]byte, len(man.Files))
	rowsWanted := make(map[string]int, len(man.Files))
	for _, f := range man.Files {
		if filepath.Base(f.Path) != f.Path {
			return nil, fmt.Errorf("manifest file %q escapes the archive directory", f.Path)
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Path, err)
		}
		if sha256Hex(data) != f.SHA256 {
			return nil, fmt.Errorf("file %s digest mismatch", f.Path)
		}
		files[f.Path] = data
		rowsWanted[f.Path] = f.Rows
	}
	for _, required := range []string{LayersFile, FeaturesFile, ClassesFile} {
		if files[required] == nil {
			return nil, fmt.Errorf("archive is missing %s", required)
		}
	}

	layerRows, err := readTable(files[LayersFile], layersHeader)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", LayersFile, err)
	}
	featureRows, err := readTable(files[FeaturesFile], featuresHeader)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FeaturesFile, err)
	}
	classRows, err := readTable(files[ClassesFile], classesHeader)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ClassesFile, err)
	}
	if len(layerRows) != rowsWanted[LayersFile] || len(featureRows) != rowsWanted[FeaturesFile] ||
		len(classRows) != rowsWanted[ClassesFile] {
		return nil, fmt.Errorf("manifest row counts do not match the tables")
	}

	image := catalog.CatalogImage{Revision: revision, Layers: make([]catalog.LayerImage, 0, len(layerRows))}
	featuresByLayer := make(map[string][]catalog.Feature)
	for _, row := range layerRows {
		def, layerRevision, count, err := parseLayerRow(row)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", LayersFile, err)
		}
		if LayerDigest(def) != row[9] {
			return nil, fmt.Errorf("%s: layer %q row digest mismatch", LayersFile, def.Name)
		}
		image.Layers = append(image.Layers, catalog.LayerImage{Definition: def, Revision: layerRevision})
		featuresByLayer[def.Name] = make([]catalog.Feature, 0, count)
	}
	for _, row := range featureRows {
		layerName, f, err := parseFeatureRow(row)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", FeaturesFile, err)
		}
		features, ok := featuresByLayer[layerName]
		if !ok {
			return nil, fmt.Errorf("%s: feature %q names undeclared layer %q", FeaturesFile, f.ID, layerName)
		}
		if FeatureDigest(layerName, f) != row[10] {
			return nil, fmt.Errorf("%s: feature %q row digest mismatch", FeaturesFile, f.ID)
		}
		featuresByLayer[layerName] = append(features, f)
	}
	for i := range image.Layers {
		name := image.Layers[i].Definition.Name
		image.Layers[i].Features = featuresByLayer[name]
		if image.Layers[i].Features == nil {
			image.Layers[i].Features = []catalog.Feature{}
		}
	}

	m, err := Validate(image)
	if err != nil {
		return nil, fmt.Errorf("archive content violates image rules: %w", err)
	}
	m.Source = dir

	// Cross-check the derived columns and the summary against the model.
	for i, l := range m.Layers {
		if got, _ := strconv.Atoi(layerRows[i][8]); got != len(l.Features) {
			return nil, fmt.Errorf("%s: layer %q feature count mismatch", LayersFile, l.Definition.Name)
		}
	}
	rowIndex := 0
	totalArea := new(big.Int)
	for _, l := range m.Layers {
		for _, f := range l.Features {
			row := featureRows[rowIndex]
			rowIndex++
			if row[0] != l.Definition.Name || row[1] != f.ID {
				return nil, fmt.Errorf("%s: rows are not in canonical (layer, feature) order", FeaturesFile)
			}
			if area := strconv.FormatInt(Area(f), 10); row[8] != area {
				return nil, fmt.Errorf("%s: feature %q area mismatch", FeaturesFile, f.ID)
			}
			if odist := strconv.FormatInt(OriginDistanceSquared(f), 10); row[9] != odist {
				return nil, fmt.Errorf("%s: feature %q origin distance mismatch", FeaturesFile, f.ID)
			}
			totalArea.Add(totalArea, big.NewInt(Area(f)))
		}
	}
	groups := classGroups(m)
	if len(groups) != len(classRows) {
		return nil, fmt.Errorf("%s: group count mismatch", ClassesFile)
	}
	for i, g := range groups {
		if !slices.Equal(g.row(), classRows[i]) {
			return nil, fmt.Errorf("%s: summary row %d (%s/%s) mismatch", ClassesFile, i+1, g.class, g.kind)
		}
	}
	if man.Layers != len(m.Layers) || man.Features != m.Features() || man.TotalExtentArea != totalArea.String() {
		return nil, fmt.Errorf("manifest totals do not match the tables")
	}
	return m, nil
}

func readTable(data []byte, header []string) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = len(header)
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if !slices.Equal(head, header) {
		return nil, fmt.Errorf("unexpected header %q", strings.Join(head, ","))
	}
	var rows [][]string
	for {
		row, err := r.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
}

func parseLayerRow(row []string) (catalog.Definition, uint64, int, error) {
	var def catalog.Definition
	coords, err := parseInts(row[2:6])
	if err != nil {
		return def, 0, 0, fmt.Errorf("layer %q bounds: %w", row[0], err)
	}
	exclusive, err := strconv.ParseBool(row[6])
	if err != nil {
		return def, 0, 0, fmt.Errorf("layer %q exclusive flag: %w", row[0], err)
	}
	revision, err := strconv.ParseUint(row[7], 10, 64)
	if err != nil {
		return def, 0, 0, fmt.Errorf("layer %q revision: %w", row[0], err)
	}
	count, err := strconv.Atoi(row[8])
	if err != nil {
		return def, 0, 0, fmt.Errorf("layer %q feature count: %w", row[0], err)
	}
	def = catalog.Definition{
		Name: row[0],
		Kind: plane.Kind(row[1]),
		Layout: catalog.Layout{
			Bounds:    plane.Box{MinX: coords[0], MinY: coords[1], MaxX: coords[2], MaxY: coords[3]},
			Exclusive: exclusive,
		},
	}
	return def, revision, count, nil
}

func parseFeatureRow(row []string) (string, catalog.Feature, error) {
	var f catalog.Feature
	coords, err := parseInts(row[4:8])
	if err != nil {
		return row[0], f, fmt.Errorf("feature %q coordinates: %w", row[1], err)
	}
	f = catalog.Feature{ID: row[1], Class: row[2]}
	switch plane.Kind(row[3]) {
	case plane.Mark:
		if coords[0] != coords[2] || coords[1] != coords[3] {
			return row[0], f, fmt.Errorf("feature %q mark envelope must be degenerate", row[1])
		}
		f.Shape = plane.Shape{Mark: &plane.Point{X: coords[0], Y: coords[1]}}
	case plane.Extent:
		f.Shape = plane.Shape{Extent: &plane.Box{MinX: coords[0], MinY: coords[1], MaxX: coords[2], MaxY: coords[3]}}
	default:
		return row[0], f, fmt.Errorf("feature %q has unknown shape kind %q", row[1], row[3])
	}
	return row[0], f, nil
}

func parseInts(fields []string) ([]int64, error) {
	values := make([]int64, len(fields))
	for i, field := range fields {
		value, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", field)
		}
		values[i] = value
	}
	return values, nil
}
