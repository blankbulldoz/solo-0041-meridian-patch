package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/bookmark"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// ReadinessError identifies the startup gate that did not pass. A takeover
// drill uses Stage to tell a busy writer lock apart from a corrupt image, a
// failing index audit, or a locate probe that disagrees with the catalog.
type ReadinessError struct {
	Stage string
	Err   error
}

func (e *ReadinessError) Error() string { return fmt.Sprintf("%s readiness: %v", e.Stage, e.Err) }
func (e *ReadinessError) Unwrap() error { return e.Err }

// StageOf returns the readiness stage carried by err, or "" when err is not a
// startup gate failure.
func StageOf(err error) string {
	var gate *ReadinessError
	if errors.As(err, &gate) {
		return gate.Stage
	}
	return ""
}

// Probe is a real locate query that a new writer must reproduce before it
// announces readiness. Expect is compared as a sorted set of feature IDs.
type Probe struct {
	Layer  string
	Point  plane.Point
	Expect []string
}

func (p Probe) Validate() error {
	if !catalog.ValidName(p.Layer) {
		return errors.New("probe layer must be a valid layer name")
	}
	if err := p.Point.Validate(); err != nil {
		return fmt.Errorf("probe point: %w", err)
	}
	for _, id := range p.Expect {
		if !catalog.ValidName(id) {
			return fmt.Errorf("probe expectation %q is not a valid feature id", id)
		}
	}
	return nil
}

// ImageReport records the on-disk image check performed before serving.
type ImageReport struct {
	Verified bool   `json:"verified"`
	Source   string `json:"source"`
	Bytes    int    `json:"bytes"`
	Digest   string `json:"digest,omitempty"`
	Revision uint64 `json:"revision"`
	Layers   int    `json:"layers"`
	Features int    `json:"features"`
}

// LayerAudit records one layer's index-versus-catalog check.
type LayerAudit struct {
	Layer        string `json:"layer"`
	Revision     uint64 `json:"revision"`
	Records      int    `json:"records"`
	CheckedNodes int    `json:"checked_nodes"`
}

// ProbeReport records the locate probe and the rows it actually returned.
type ProbeReport struct {
	Layer         string      `json:"layer"`
	Point         plane.Point `json:"point"`
	Expected      []string    `json:"expected"`
	Observed      []string    `json:"observed"`
	Missing       []string    `json:"missing,omitempty"`
	Unexpected    []string    `json:"unexpected,omitempty"`
	Revision      uint64      `json:"revision"`
	LayerRevision uint64      `json:"layer_revision"`
	Nodes         int         `json:"nodes"`
	Candidates    int         `json:"candidates"`
}

// ReadinessReport is the evidence gathered before the service announces
// readiness. Every field is derived from the same data directory a new writer
// holds exclusively.
type ReadinessReport struct {
	DataDir  string       `json:"data_dir"`
	Lock     string       `json:"lock"`
	Image    ImageReport  `json:"image"`
	Index    []LayerAudit `json:"index"`
	Probe    *ProbeReport `json:"probe,omitempty"`
	Revision uint64       `json:"revision"`
}

// OpenChecked acquires the exclusive writer lock, verifies the stored image,
// audits every layer index, and optionally runs a real locate probe. Any gate
// failure closes the lock again and is reported with its stage.
func OpenChecked(dir string, probe *Probe) (*Database, *ReadinessReport, error) {
	store, err := folio.Open(dir)
	if err != nil {
		// Only a held lock is the lock gate; any other open failure is an I/O
		// fault that must not be reported as a busy writer.
		if folio.IsLocked(err) {
			return nil, nil, &ReadinessError{Stage: "lock", Err: err}
		}
		return nil, nil, err
	}
	d, report, err := openCheckedStore(store, probe)
	if err != nil {
		store.Close()
		return nil, report, err
	}
	return d, report, nil
}

func openCheckedStore(store *folio.Store, probe *Probe) (*Database, *ReadinessReport, error) {
	report := &ReadinessReport{Lock: "acquired", DataDir: store.Dir()}
	signer, err := bookmark.New()
	if err != nil {
		return nil, report, &ReadinessError{Stage: "image", Err: fmt.Errorf("create bookmark signer: %w", err)}
	}
	d := &Database{store: store, signer: signer}
	s := &state{layers: make(map[string]*layer)}

	raw, present, err := store.Read()
	if err != nil {
		return nil, report, &ReadinessError{Stage: "image", Err: fmt.Errorf("read state: %w", err)}
	}
	if present {
		image, err := folio.Decode(raw)
		if err != nil {
			return nil, report, &ReadinessError{Stage: "image", Err: fmt.Errorf("reopen state: %w", err)}
		}
		s, err = fromImage(image)
		if err != nil {
			return nil, report, &ReadinessError{Stage: "image", Err: fmt.Errorf("reopen state: %w", err)}
		}
		sum := sha256.Sum256(raw)
		var features int
		for _, entry := range image.Layers {
			features += len(entry.Features)
		}
		report.Image = ImageReport{
			Verified: true, Source: "file", Bytes: len(raw), Digest: hex.EncodeToString(sum[:]),
			Revision: image.Revision, Layers: len(image.Layers), Features: features,
		}
	} else {
		report.Image = ImageReport{Verified: true, Source: "absent"}
	}
	d.head.Store(s)
	report.Revision = s.revision

	report.Index, err = auditAll(s)
	if err != nil {
		return nil, report, &ReadinessError{Stage: "index", Err: err}
	}
	if probe != nil {
		probeReport, err := runProbe(d, *probe)
		report.Probe = probeReport
		if err != nil {
			return nil, report, &ReadinessError{Stage: "probe", Err: err}
		}
	}
	return d, report, nil
}

func auditAll(s *state) ([]LayerAudit, error) {
	names := s.names()
	audits := make([]LayerAudit, 0, len(names))
	for _, name := range names {
		l := s.layers[name]
		result, err := l.tree.Audit(l.rows)
		if err != nil {
			return nil, fmt.Errorf("index audit for layer %q at revision %d: %w", name, l.revision, err)
		}
		audits = append(audits, LayerAudit{
			Layer: name, Revision: l.revision, Records: result.Records, CheckedNodes: result.CheckedNodes,
		})
	}
	return audits, nil
}

func runProbe(d *Database, probe Probe) (*ProbeReport, error) {
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	expected := slices.Clone(probe.Expect)
	slices.Sort(expected)
	expected = slices.Compact(expected)
	result, err := d.Locate(probe.Layer, probe.Point)
	if err != nil {
		return &ProbeReport{Layer: probe.Layer, Point: probe.Point, Expected: expected}, err
	}
	observed := make([]string, 0, len(result.Features))
	for _, row := range result.Features {
		observed = append(observed, row.ID)
	}
	slices.Sort(observed)
	report := &ProbeReport{
		Layer: probe.Layer, Point: probe.Point, Expected: expected, Observed: observed,
		Revision: result.Revision, LayerRevision: result.LayerRevision,
		Nodes: result.Visit.Nodes, Candidates: result.Visit.Candidates,
	}
	if slices.Equal(expected, observed) {
		return report, nil
	}
	var missing, unexpected []string
	for _, id := range expected {
		if !slices.Contains(observed, id) {
			missing = append(missing, id)
		}
	}
	for _, id := range observed {
		if !slices.Contains(expected, id) {
			unexpected = append(unexpected, id)
		}
	}
	slices.Sort(missing)
	slices.Sort(unexpected)
	report.Missing = missing
	report.Unexpected = unexpected
	return report, fmt.Errorf("locate %s at (%d,%d): expected %v, observed %v (missing %v, unexpected %v)",
		probe.Layer, probe.Point.X, probe.Point.Y, expected, observed, missing, unexpected)
}
