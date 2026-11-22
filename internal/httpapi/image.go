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

func (a *api) export(w http.ResponseWriter, r *http.Request) {
	if !a.formalOnly(w, r) {
		return
	}
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
	if !a.formalOnly(w, r) || !a.media(w, r, imageType) {
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
