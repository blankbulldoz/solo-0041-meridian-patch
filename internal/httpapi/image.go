package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

const imageType = "application/vnd.meridian.image"
const revisionHeader = "Meridian-Revision"
const expectedRevisionHeader = "If-Meridian-Revision"

func (a *api) export(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	request := db.ExportRequest{Layers: query["layers"]}
	if window, ok, err := exportWindow(query); err != nil {
		a.json(w, http.StatusBadRequest, issue{"invalid", err.Error()})
		return
	} else if ok {
		request.Window = &window
	}
	raw, revision, err := a.db.ExportSelection(request)
	if err != nil {
		a.respond(w, http.StatusOK, nil, err)
		return
	}
	w.Header().Set("Content-Type", imageType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set(revisionHeader, strconv.FormatUint(revision, 10))
	if _, err := w.Write(raw); err != nil {
		a.log.Warn("write image", "error", err)
	}
}

// exportWindow interprets the four half-open window coordinates. The window is
// either fully supplied or omitted; a partial window, repeated coordinates or
// non-integer values are rejected.
func exportWindow(query map[string][]string) (plane.Box, bool, error) {
	names := []string{"min_x", "min_y", "max_x", "max_y"}
	var values [4]int64
	present := false
	for i, name := range names {
		raw, ok := query[name]
		if !ok || len(raw) == 0 {
			continue
		}
		if len(raw) != 1 {
			return plane.Box{}, false, fmt.Errorf("window coordinate %s must be given once", name)
		}
		value, err := strconv.ParseInt(raw[0], 10, 64)
		if err != nil {
			return plane.Box{}, false, fmt.Errorf("window coordinate %s must be an integer", name)
		}
		values[i] = value
		present = true
	}
	if !present {
		return plane.Box{}, false, nil
	}
	for _, name := range names {
		if len(query[name]) == 0 {
			return plane.Box{}, false, errors.New("window requires min_x, min_y, max_x and max_y together")
		}
	}
	return plane.Box{MinX: values[0], MinY: values[1], MaxX: values[2], MaxY: values[3]}, true, nil
}

func (a *api) restore(w http.ResponseWriter, r *http.Request) {
	if !a.media(w, r, imageType) {
		return
	}
	header := r.Header.Values(expectedRevisionHeader)
	if len(header) != 1 {
		a.json(w, http.StatusPreconditionRequired, issue{"revision_required", "supply one If-Meridian-Revision header"})
		return
	}
	expected, err := strconv.ParseUint(header[0], 10, 64)
	if err != nil {
		a.json(w, http.StatusBadRequest, issue{"invalid", "If-Meridian-Revision must be an unsigned integer"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, folio.MaxImage))
	if err != nil {
		a.readError(w, err)
		return
	}
	commit, err := a.db.Restore(raw, expected)
	a.respond(w, http.StatusOK, commit, err)
}
