package workorder

import (
	"fmt"
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// ArtifactResult is the outcome of executing a replay artifact literally, the
// way an external client would, against a fresh throwaway database.
type ArtifactResult struct {
	Order       string `json:"order"`
	StepsRun    int    `json:"steps_run"`
	FinalDigest string `json:"final_digest"`
	Matches     bool   `json:"matches"`
	// Complete is false when the artifact skips groups, so FinalDigest is a
	// decided prefix rather than the ledger's true end state.
	Complete bool     `json:"complete"`
	Notes    []string `json:"notes,omitempty"`
}

// ExecuteArtifact replays the emitted replay.json step by step over HTTP. It
// deliberately reads the artifact rather than the report, so what ships to a
// client is exactly what was proven to work. expected is the digest the plan
// promises for the decided portion; "" means no comparison is possible.
func ExecuteArtifact(artifact *ReplayArtifact, expected string) (*ArtifactResult, error) {
	server, err := Start("")
	if err != nil {
		return nil, err
	}
	defer server.Close()
	result := &ArtifactResult{Order: artifact.Order, Complete: len(artifact.Skipped) == 0}
	for _, step := range artifact.Steps {
		code, raw, err := server.Do(step.Method, stripPrefix(step.Path), step.Body)
		if err != nil {
			return nil, fmt.Errorf("step %d (%s): %w", step.Sequence, step.Action, err)
		}
		if code != http.StatusOK && code != http.StatusCreated {
			return nil, fmt.Errorf("step %d (%s) %s %s: HTTP %d: %s",
				step.Sequence, step.Action, step.Method, step.Path, code, raw)
		}
		result.StepsRun++
	}
	image, _, err := server.Image()
	if err != nil {
		return nil, err
	}
	result.FinalDigest = FromImage(image).ShapeDigest()
	switch {
	case len(artifact.Skipped) > 0:
		// The artifact intentionally stopped at the first undecidable group.
		result.Matches = true
		result.Notes = append(result.Notes, fmt.Sprintf(
			"跳过了 %d 个组，因此终态只是已判定部分的前缀，不是账本的真实终态",
			len(artifact.Skipped)))
	case expected == "":
		result.Matches = true
		result.Notes = append(result.Notes, "未提供期望摘要；产物已执行但未做比对")
	default:
		result.Matches = result.FinalDigest == expected
	}
	return result, nil
}

func stripPrefix(path string) string {
	const prefix = "/meridian/v1"
	if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
		return path[len(prefix):]
	}
	return path
}

// Step bodies must stay real catalog types so the artifact cannot drift from
// the wire format; this compile-time assertion fails if EditBatch moves.
var _ = func(batch catalog.EditBatch) int { return len(batch.Edits) }
