package workorder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// Step is one portable HTTP action. Values are the exact request bodies, so a
// replay artifact can be executed by any client without re-deriving shapes.
type Step struct {
	Sequence int    `json:"sequence"`
	Action   string `json:"action"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Body     any    `json:"body,omitempty"`
	Group    string `json:"group,omitempty"`
	// SourceLines is present on edit batches and matches Body["edits"] index
	// for index.
	SourceLines []int  `json:"source_lines,omitempty"`
	Note        string `json:"note,omitempty"`
}

// ReplayArtifact is a client-agnostic, executable description of the whole
// ledger: baseline prelude, then one batch per decided group in commit order.
type ReplayArtifact struct {
	Order          string `json:"order"`
	Description    string `json:"description,omitempty"`
	BaselineDigest string `json:"baseline_digest"`
	// RevisionContract explains how a caller must pin the target database.
	// It is prose, not a value, because the artifact is portable across
	// databases and the right revision only exists at execution time.
	RevisionContract string           `json:"revision_contract"`
	Steps            []Step           `json:"steps"`
	Skipped          []SkippedGroup   `json:"skipped_groups"`
	Unresolved       []UnresolvedLine `json:"unresolved_lines"`
}

type SkippedGroup struct {
	Group          string `json:"group"`
	Classification string `json:"classification"`
	Reason         string `json:"reason"`
	SourceLines    []int  `json:"source_lines"`
}

// UnresolvedLine lists every ledger line that cannot be replayed. Delivering
// this list is the explicit answer to "which rows can I not recover?".
type UnresolvedLine struct {
	Line           int    `json:"line"`
	Group          string `json:"group"`
	Layer          string `json:"layer"`
	ID             string `json:"id"`
	Mode           string `json:"mode"`
	Classification string `json:"classification"`
}

// BuildArtifact turns a report into an executable replay plan. It emits only
// steps the ledger can justify; everything else lands in Skipped/Unresolved.
func BuildArtifact(order *Order, report *Report) *ReplayArtifact {
	artifact := &ReplayArtifact{
		Order:          report.Order,
		Description:    report.Description,
		BaselineDigest: report.BaselineDigest,
		RevisionContract: "Run against a target whose current revision is captured first; " +
			"pass that value as if_revision on the first edit batch so a concurrent writer aborts the replay " +
			"instead of silently interleaving. Do not reuse the revision of the source database.",
		Skipped:    make([]SkippedGroup, 0),
		Unresolved: make([]UnresolvedLine, 0),
		Steps:      make([]Step, 0),
	}
	sequence := 0
	next := func() int { sequence++; return sequence }
	for _, def := range order.Layers {
		artifact.Steps = append(artifact.Steps, Step{
			Sequence: next(),
			Action:   "create-layer",
			Method:   "POST",
			Path:     "/meridian/v1/strata",
			Body:     def,
			Note:     "baseline prelude; skip if the layer already exists with this definition",
		})
	}
	if len(order.Features) > 0 {
		edits := make([]catalog.Edit, 0, len(order.Features))
		for _, spec := range order.Features {
			feature := catalog.Feature{ID: spec.ID, Class: spec.Class, Shape: spec.Shape}
			edits = append(edits, catalog.Edit{Layer: spec.Layer, ID: spec.ID, Mode: "insert", Value: &feature})
		}
		artifact.Steps = append(artifact.Steps, Step{
			Sequence: next(),
			Action:   "seed-baseline",
			Method:   "POST",
			Path:     "/meridian/v1/edits",
			Body:     catalog.EditBatch{Edits: edits},
			Note:     fmt.Sprintf("baseline content; resulting shape digest should be %s", report.BaselineDigest),
		})
	}
	for _, group := range report.Groups {
		switch group.Classification {
		case "unique":
			artifact.Steps = append(artifact.Steps, Step{
				Sequence:    next(),
				Action:      "replay-group",
				Method:      "POST",
				Path:        "/meridian/v1/edits",
				Body:        group.Replay.Request,
				Group:       group.Name,
				SourceLines: group.Replay.SourceLines,
				Note:        replayNote(group),
			})
		default:
			artifact.Skipped = append(artifact.Skipped, SkippedGroup{
				Group:          group.Name,
				Classification: group.Classification,
				Reason:         classLabel(group.Classification),
				SourceLines:    linesOf(group.Rows),
			})
			for _, row := range group.Rows {
				artifact.Unresolved = append(artifact.Unresolved, UnresolvedLine{
					Line: row.Line, Group: group.Name, Layer: row.Layer,
					ID: row.ID, Mode: row.Mode, Classification: group.Classification,
				})
			}
		}
	}
	return artifact
}

func replayNote(group Group) string {
	note := "send as one batch; source_lines[i] is the provenance of edits[i]"
	if group.AtomicRequired {
		note += "; no legal ordering of this group can be split into several commits"
	} else if group.ReplayNeedsWholeGroup {
		note += "; this ordering must be sent as one batch even though another ordering could be split"
	}
	return note
}

// WriteArtifact writes replay.json next to the other deliverables.
func WriteArtifact(dir string, artifact *ReplayArtifact) error {
	raw, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "replay.json"), raw, 0o644)
}
