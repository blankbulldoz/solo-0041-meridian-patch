package httpapi

import (
	"encoding/base64"
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

// transplantPreviewRequest is the JSON form of a transplant preview. The source
// image travels base64 so one JSON request can carry the whole backup region.
type transplantPreviewRequest struct {
	Source      string       `json:"source"`
	SourceLayer string       `json:"source_layer"`
	Target      string       `json:"target"`
	Bounds      plane.Box    `json:"bounds"`
	Offset      plane.Offset `json:"offset"`
	Mode        string       `json:"mode"`
	IfRevision  *uint64      `json:"if_revision,omitempty"`
}

type transplantCommitRequest struct {
	Plan       string  `json:"plan"`
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

func (a *api) previewTransplant(w http.ResponseWriter, r *http.Request) {
	var request transplantPreviewRequest
	if !a.inputLimit(w, r, &request, maxImageJSON) {
		return
	}
	source, err := decodeImage(request.Source)
	if err != nil {
		a.json(w, http.StatusBadRequest, issue{"invalid", "source must be base64 of an MPB v1 image"})
		return
	}
	result, err := a.db.PreviewTransplant(db.TransplantRequest{
		Source:      source,
		SourceLayer: request.SourceLayer,
		Target:      request.Target,
		Bounds:      request.Bounds,
		Offset:      request.Offset,
		Mode:        request.Mode,
		IfRevision:  request.IfRevision,
	})
	a.respond(w, http.StatusOK, result, err)
}

func (a *api) commitTransplant(w http.ResponseWriter, r *http.Request) {
	var request transplantCommitRequest
	if !a.inputLimit(w, r, &request, maxPlanJSON) {
		return
	}
	result, err := a.db.CommitTransplant(request.Plan, request.IfRevision)
	a.respond(w, http.StatusOK, result, err)
}

// decodeImage accepts the standard and URL-safe base64 alphabets with either
// padding style, because an operator may paste a blob from any encoder.
func decodeImage(value string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	var last error
	for _, encoding := range encodings {
		decoded, err := encoding.Strict().DecodeString(value)
		if err == nil {
			return decoded, nil
		}
		last = err
	}
	return nil, last
}
