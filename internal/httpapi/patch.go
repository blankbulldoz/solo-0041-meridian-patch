package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/delta"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

const patchType = "application/vnd.meridian.patch"

// maxMultipart caps the combined size of an uploaded image pair. Each image is
// itself bounded by folio.MaxImage, so two of them plus form overhead fit here.
const maxMultipart = 2*folio.MaxImage + (1 << 16)

// diffPackage derives an incremental change package. The baseline image is a
// required multipart part; an optional target part overrides the database's
// current image, which lets an operator diff two exported files over HTTP.
func (a *api) diffPackage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMultipart)
	if err := r.ParseMultipartForm(maxMultipart); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			a.json(w, http.StatusRequestEntityTooLarge, issue{"capacity", "upload exceeds size limit"})
			return
		}
		a.json(w, http.StatusBadRequest, issue{"invalid", "expected multipart/form-data with baseline and optional target parts"})
		return
	}
	baseline, ok := a.readPart(w, r, "baseline")
	if !ok {
		return
	}
	targetFile, header, err := r.FormFile("target")
	if errors.Is(err, http.ErrMissingFile) {
		patch, derr := a.db.Diff(baseline)
		a.writePatch(w, patch, derr)
		return
	}
	if err != nil {
		a.json(w, http.StatusBadRequest, issue{"invalid", "unreadable target part"})
		return
	}
	defer targetFile.Close()
	target, ok := a.readFilePart(w, targetFile, header.Size)
	if !ok {
		return
	}
	baseImage, ok := a.decodeImagePart(w, baseline)
	if !ok {
		return
	}
	targetImage, ok := a.decodeImagePart(w, target)
	if !ok {
		return
	}
	patch, derr := db.DiffImages(baseImage, targetImage)
	a.writePatch(w, patch, derr)
}

func (a *api) readPart(w http.ResponseWriter, r *http.Request, field string) ([]byte, bool) {
	file, header, err := r.FormFile(field)
	if err != nil {
		a.json(w, http.StatusBadRequest, issue{"invalid", "missing multipart part " + field})
		return nil, false
	}
	defer file.Close()
	return a.readFilePart(w, file, header.Size)
}

func (a *api) readFilePart(w http.ResponseWriter, file io.Reader, size int64) ([]byte, bool) {
	if size > folio.MaxImage {
		a.json(w, http.StatusRequestEntityTooLarge, issue{"capacity", "image part exceeds size limit"})
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, folio.MaxImage+1))
	if err != nil || len(raw) > folio.MaxImage {
		a.json(w, http.StatusRequestEntityTooLarge, issue{"capacity", "image part exceeds size limit"})
		return nil, false
	}
	return raw, true
}

func (a *api) decodeImagePart(w http.ResponseWriter, raw []byte) (catalog.CatalogImage, bool) {
	image, err := folio.Decode(raw)
	if err != nil {
		a.json(w, http.StatusBadRequest, issue{"invalid", "invalid MPB image: " + err.Error()})
		return catalog.CatalogImage{}, false
	}
	return image, true
}

func (a *api) writePatch(w http.ResponseWriter, patch delta.Patch, err error) {
	if err != nil {
		a.respond(w, http.StatusOK, nil, err)
		return
	}
	raw, encErr := delta.Encode(patch)
	if encErr != nil {
		a.respond(w, http.StatusInternalServerError, nil, encErr)
		return
	}
	w.Header().Set("Content-Type", patchType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Meridian-Baseline-Digest", patch.BaselineDigest)
	w.Header().Set("Meridian-Target-Digest", patch.TargetDigest)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(raw); err != nil {
		a.log.Warn("write patch", "error", err)
	}
}

// applyPackage applies an uploaded MPC v1 package. If-Meridian-Revision is the
// optional CAS guard; idempotent replays succeed without it.
func (a *api) applyPackage(w http.ResponseWriter, r *http.Request) {
	if !a.media(w, r, patchType) {
		return
	}
	var expected *uint64
	if headers := r.Header.Values(expectedRevisionHeader); len(headers) > 0 {
		if len(headers) != 1 {
			a.json(w, http.StatusBadRequest, issue{"invalid", "supply at most one If-Meridian-Revision header"})
			return
		}
		parsed, err := strconv.ParseUint(headers[0], 10, 64)
		if err != nil {
			a.json(w, http.StatusBadRequest, issue{"invalid", "If-Meridian-Revision must be an unsigned integer"})
			return
		}
		expected = &parsed
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, delta.MaxPatch))
	if err != nil {
		a.readError(w, err)
		return
	}
	receipt, applyErr := a.db.ApplyPatch(raw, expected)
	a.respond(w, http.StatusOK, receipt, applyErr)
}
