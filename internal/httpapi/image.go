package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

const imageType = "application/vnd.meridian.image"
const revisionHeader = "Meridian-Revision"
const expectedRevisionHeader = "If-Meridian-Revision"

// The same PUT /image endpoint serves both installation and preflight. Mode is
// selected by the query string so the request body, media type and revision
// precondition are identical: ?mode=preflight reports what would happen and
// writes nothing; the default and ?mode=apply install.
const modeParam = "mode"

func (a *api) export(w http.ResponseWriter, _ *http.Request) {
	raw, revision, err := a.db.Export()
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

func (a *api) restore(w http.ResponseWriter, r *http.Request) {
	mode, ok := a.restoreMode(w, r)
	if !ok {
		return
	}
	if !a.media(w, r, imageType) {
		return
	}
	header := r.Header.Values(expectedRevisionHeader)
	// Preflight may omit the precondition, but installation always needs one
	// so a stale report can never be the thing that confirms a write.
	if len(header) > 1 || (len(header) == 0 && mode != "preflight") {
		a.json(w, http.StatusPreconditionRequired, issue{"revision_required", "supply one If-Meridian-Revision header"})
		return
	}
	var expected *uint64
	if len(header) == 1 {
		parsed, err := strconv.ParseUint(header[0], 10, 64)
		if err != nil {
			a.json(w, http.StatusBadRequest, issue{"invalid", "If-Meridian-Revision must be an unsigned integer"})
			return
		}
		expected = &parsed
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, folio.MaxImage))
	if err != nil {
		a.readError(w, err)
		return
	}
	if mode == "preflight" {
		report, err := a.db.Preflight(raw, expected)
		a.respond(w, http.StatusOK, report, err)
		return
	}
	commit, err := a.db.Restore(raw, *expected)
	a.respond(w, http.StatusOK, commit, err)
}

// restoreMode parses the optional mode selector. Unknown values are rejected
// rather than silently treated as a write, so a typo cannot install an image.
func (a *api) restoreMode(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, present := r.URL.Query()[modeParam]
	if !present {
		return "apply", true
	}
	if len(values) != 1 || (values[0] != "apply" && values[0] != "preflight") {
		a.json(w, http.StatusBadRequest, issue{"invalid", "mode must be one of apply or preflight"})
		return "", false
	}
	return values[0], true
}
