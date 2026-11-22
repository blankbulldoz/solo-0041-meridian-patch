package archive

import (
	"slices"
	"strconv"
	"strings"
)

// Verdict classifies how two archives differ.
type Verdict string

const (
	// Identical: every row and every revision match.
	Identical Verdict = "identical"
	// RevisionOnly: only commit numbers (database and layer revisions) moved.
	RevisionOnly Verdict = "revision-only"
	// Relayout: layer bounds or exclusivity changed; every feature row is intact.
	Relayout Verdict = "relayout"
	// ContentChanged: feature rows or the layer set itself changed.
	ContentChanged Verdict = "content-changed"
)

// LayoutChange records one layer whose definition changed between archives.
type LayoutChange struct {
	Layer      string `json:"layer"`
	KindA      string `json:"kind_a"`
	KindB      string `json:"kind_b"`
	BoundsA    string `json:"bounds_a"`
	BoundsB    string `json:"bounds_b"`
	ExclusiveA bool   `json:"exclusive_a"`
	ExclusiveB bool   `json:"exclusive_b"`
}

// FeatureChange records one feature row present in only one archive, or
// present in both with a different identity digest.
type FeatureChange struct {
	Layer   string `json:"layer"`
	ID      string `json:"id"`
	Change  string `json:"change"` // "added", "removed", or "changed"
	DigestA string `json:"digest_a,omitempty"`
	DigestB string `json:"digest_b,omitempty"`
}

// Report describes the difference between archive A and archive B. Revision
// numbers are decimal strings so values beyond 2^53-1 survive JSON tooling.
type Report struct {
	Verdict         Verdict         `json:"verdict"`
	A               string          `json:"a"`
	B               string          `json:"b"`
	RevisionA       string          `json:"revision_a"`
	RevisionB       string          `json:"revision_b"`
	MatchedLayers   int             `json:"matched_layers"`
	MatchedFeatures int             `json:"matched_features"`
	LayersAdded     []string        `json:"layers_added,omitempty"`
	LayersRemoved   []string        `json:"layers_removed,omitempty"`
	LayoutChanges   []LayoutChange  `json:"layout_changes,omitempty"`
	FeatureChanges  []FeatureChange `json:"feature_changes,omitempty"`
}

// Compare classifies the difference between two validated models. Revision
// metadata never turns a verdict into ContentChanged on its own: a restore
// that only re-numbers commits stays RevisionOnly.
func Compare(a, b *Model) Report {
	report := Report{
		A:         a.Source,
		B:         b.Source,
		RevisionA: strconv.FormatUint(a.Revision, 10),
		RevisionB: strconv.FormatUint(b.Revision, 10),
	}
	layersA := layersByName(a)
	layersB := layersByName(b)

	revisionsEqual := a.Revision == b.Revision
	layoutEqual := true
	featuresEqual := true

	for _, name := range sortedLayerNames(layersA) {
		la := layersA[name]
		lb, ok := layersB[name]
		if !ok {
			report.LayersRemoved = append(report.LayersRemoved, name)
			featuresEqual = false
			continue
		}
		if la.Revision != lb.Revision {
			revisionsEqual = false
		}
		if la.Definition.Kind != lb.Definition.Kind || la.Definition.Bounds != lb.Definition.Bounds ||
			la.Definition.Exclusive != lb.Definition.Exclusive {
			report.LayoutChanges = append(report.LayoutChanges, LayoutChange{
				Layer:      name,
				KindA:      string(la.Definition.Kind),
				KindB:      string(lb.Definition.Kind),
				BoundsA:    boxFields(la.Definition.Bounds),
				BoundsB:    boxFields(lb.Definition.Bounds),
				ExclusiveA: la.Definition.Exclusive,
				ExclusiveB: lb.Definition.Exclusive,
			})
			if la.Definition.Kind != lb.Definition.Kind {
				featuresEqual = false
			}
			layoutEqual = false
		}
		if !compareFeatures(name, la, lb, &report) {
			featuresEqual = false
		}
	}
	for _, name := range sortedLayerNames(layersB) {
		if _, ok := layersA[name]; !ok {
			report.LayersAdded = append(report.LayersAdded, name)
			featuresEqual = false
		}
	}

	switch {
	case layoutEqual && featuresEqual && revisionsEqual:
		report.Verdict = Identical
	case layoutEqual && featuresEqual:
		report.Verdict = RevisionOnly
	case featuresEqual:
		report.Verdict = Relayout
	default:
		report.Verdict = ContentChanged
	}
	return report
}

func layersByName(m *Model) map[string]Layer {
	layers := make(map[string]Layer, len(m.Layers))
	for _, l := range m.Layers {
		layers[l.Definition.Name] = l
	}
	return layers
}

func sortedLayerNames(layers map[string]Layer) []string {
	names := make([]string, 0, len(layers))
	for name := range layers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// compareFeatures matches feature rows of one layer by identity digest and
// appends the differences to the report. It reports whether the rows are
// identical.
func compareFeatures(layer string, a, b Layer, report *Report) bool {
	byID := make(map[string]string, len(a.Features))
	for _, f := range a.Features {
		byID[f.ID] = FeatureDigest(layer, f)
	}
	matched := true
	seen := make(map[string]bool, len(b.Features))
	for _, f := range b.Features {
		seen[f.ID] = true
		digestB := FeatureDigest(layer, f)
		digestA, ok := byID[f.ID]
		switch {
		case !ok:
			report.FeatureChanges = append(report.FeatureChanges, FeatureChange{Layer: layer, ID: f.ID, Change: "added", DigestB: digestB})
			matched = false
		case digestA != digestB:
			report.FeatureChanges = append(report.FeatureChanges, FeatureChange{Layer: layer, ID: f.ID, Change: "changed", DigestA: digestA, DigestB: digestB})
			matched = false
		default:
			report.MatchedFeatures++
		}
	}
	for _, f := range a.Features {
		if !seen[f.ID] {
			report.FeatureChanges = append(report.FeatureChanges, FeatureChange{Layer: layer, ID: f.ID, Change: "removed", DigestA: byID[f.ID]})
			matched = false
		}
	}
	if matched {
		report.MatchedLayers++
	}
	return matched
}

// Summary renders the verdict with its evidence as human-readable text.
func (r Report) Summary() string {
	var out strings.Builder
	switch r.Verdict {
	case Identical:
		out.WriteString("结论: identical — 两份档案完全相同\n")
	case RevisionOnly:
		out.WriteString("结论: revision-only — 两份档案仅提交号不同\n")
	case Relayout:
		out.WriteString("结论: relayout — 图层布局已调整, 地物内容未变\n")
	case ContentChanged:
		out.WriteString("结论: content-changed — 地物内容已改变\n")
	}
	out.WriteString("数据库修订: " + r.RevisionA + " -> " + r.RevisionB + "\n")
	for _, name := range r.LayersRemoved {
		out.WriteString("  移除图层: " + name + "\n")
	}
	for _, name := range r.LayersAdded {
		out.WriteString("  新增图层: " + name + "\n")
	}
	for _, change := range r.LayoutChanges {
		out.WriteString("  布局变化: " + change.Layer +
			" bounds " + change.BoundsA + " -> " + change.BoundsB +
			" exclusive " + strconv.FormatBool(change.ExclusiveA) + " -> " + strconv.FormatBool(change.ExclusiveB) + "\n")
	}
	for _, change := range r.FeatureChanges {
		out.WriteString("  地物" + change.Change + ": " + change.Layer + "/" + change.ID + "\n")
	}
	out.WriteString("匹配图层: " + strconv.Itoa(r.MatchedLayers) + ", 匹配地物行: " + strconv.Itoa(r.MatchedFeatures) + "\n")
	return out.String()
}
