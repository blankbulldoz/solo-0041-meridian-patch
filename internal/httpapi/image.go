package httpapi

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

const imageType = "application/vnd.meridian.image"
const imageV1Type = "application/vnd.meridian.image.v1"
const revisionHeader = "Meridian-Revision"
const expectedRevisionHeader = "If-Meridian-Revision"
const formatQuery = "version"

func (a *api) export(w http.ResponseWriter, r *http.Request) {
	version, ok := a.exportVersion(w, r)
	if !ok {
		return
	}
	raw, revision, err := a.db.Export(version)
	if err != nil {
		a.respond(w, http.StatusOK, nil, err)
		return
	}
	w.Header().Set("Content-Type", imageType)
	w.Header().Set("Meridian-Image-Version", version.String())
	w.Header().Set("Vary", "Accept")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set(revisionHeader, strconv.FormatUint(revision, 10))
	if _, err := w.Write(raw); err != nil {
		a.log.Warn("write image", "error", err)
	}
}

// exportVersion accepts either an explicit ?version=1|2 or a legacy v1 media
// type. Without either the current version is emitted so upgraded instances
// get per-layer validation by default.
func (a *api) exportVersion(w http.ResponseWriter, r *http.Request) (folio.Version, bool) {
	switch values := r.URL.Query()[formatQuery]; len(values) {
	case 0:
	case 1:
		version, err := folio.ParseVersion(values[0])
		if err != nil {
			a.json(w, http.StatusBadRequest, issue{"invalid", err.Error()})
			return 0, false
		}
		return version, true
	default:
		a.json(w, http.StatusBadRequest, issue{"invalid", "supply at most one " + formatQuery + " parameter"})
		return 0, false
	}
	if len(r.Header.Values("Accept")) == 0 {
		return folio.CurrentVersion, true
	}
	legacy := false
	for _, entry := range strings.Split(strings.Join(r.Header.Values("Accept"), ","), ",") {
		media, _, err := mime.ParseMediaType(strings.TrimSpace(entry))
		if err != nil {
			continue
		}
		switch media {
		case imageType, "*/*", "application/*":
			return folio.CurrentVersion, true
		case imageV1Type:
			legacy = true
		}
	}
	if legacy {
		return folio.Version1, true
	}
	a.json(w, http.StatusNotAcceptable, issue{"media_type", "expected " + imageType + " or " + imageV1Type})
	return 0, false
}

func (a *api) restore(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (media != imageType && media != imageV1Type) {
		a.json(w, http.StatusUnsupportedMediaType, issue{"media_type", "expected " + imageType + " or " + imageV1Type})
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
	commit, version, err := a.db.Restore(raw, expected)
	if err != nil {
		a.respond(w, http.StatusOK, nil, err)
		return
	}
	w.Header().Set("Meridian-Image-Version", version.String())
	a.json(w, http.StatusOK, commit)
}
