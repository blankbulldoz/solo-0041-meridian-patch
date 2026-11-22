// Command striping turns an integer rectangle range and a set of feature
// rectangles into evenly loaded vertical bands for the cartography team.
//
// Input is a JSON request with "range", "bands" and "features"; output is a
// fully traceable JSON report including real band boundaries, per-band covered
// and repeated area, deviation from the even target and source-shape lineage.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/striping"
)

type inputBox struct {
	MinX int64 `json:"min_x"`
	MinY int64 `json:"min_y"`
	MaxX int64 `json:"max_x"`
	MaxY int64 `json:"max_y"`
}

type inputFeature struct {
	ID    string   `json:"id"`
	Class string   `json:"class,omitempty"`
	Box   inputBox `json:"box"`
}

type inputRequest struct {
	Range       inputBox       `json:"range"`
	Bands       int            `json:"bands"`
	Description string         `json:"description,omitempty"`
	Features    []inputFeature `json:"features"`
}

func main() {
	in := flag.String("input", "", "path to the striping request JSON (defaults to stdin)")
	out := flag.String("output", "", "path for the report JSON (defaults to stdout)")
	flag.Parse()

	raw, err := readInput(*in)
	if err != nil {
		fatal(err)
	}
	var request inputRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		fatal(fmt.Errorf("decode request: %w", err))
	}
	req := striping.Request{
		Range: plane.Box{
			MinX: request.Range.MinX, MinY: request.Range.MinY,
			MaxX: request.Range.MaxX, MaxY: request.Range.MaxY,
		},
		Bands:       request.Bands,
		Description: request.Description,
		Features:    make([]striping.Feature, 0, len(request.Features)),
	}
	for _, f := range request.Features {
		req.Features = append(req.Features, striping.Feature{
			ID:    f.ID,
			Class: f.Class,
			Box: plane.Box{
				MinX: f.Box.MinX, MinY: f.Box.MinY,
				MaxX: f.Box.MaxX, MaxY: f.Box.MaxY,
			},
		})
	}

	report, err := striping.Plan(req)
	if err != nil {
		fatal(err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
	}
	encoded = append(encoded, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(encoded)
	} else {
		err = os.WriteFile(*out, encoded, 0o644)
	}
	if err != nil {
		fatal(err)
	}
}

func readInput(path string) ([]byte, error) {
	if path == "" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "striping:", err)
	os.Exit(1)
}
