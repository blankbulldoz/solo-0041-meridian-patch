package httpapi

import (
	"errors"
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

func (a *api) respond(w http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		a.json(w, status, value)
		return
	}
	var problem *db.Error
	if !errors.As(err, &problem) {
		a.log.Error("database operation failed", "error", err)
		a.json(w, http.StatusInternalServerError, issue{"internal", "database operation failed"})
		return
	}
	statuses := map[string]int{
		"invalid":          http.StatusBadRequest,
		"missing":          http.StatusNotFound,
		"conflict":         http.StatusConflict,
		"capacity":         http.StatusUnprocessableEntity,
		"unavailable":      http.StatusServiceUnavailable,
		"commit_uncertain": http.StatusServiceUnavailable,
	}
	code := statuses[problem.Code]
	if code == 0 {
		code = http.StatusInternalServerError
	}
	if code >= 500 {
		a.log.Error("database unavailable", "error", err)
	}
	report := imageIssue{issue: issue{Code: problem.Code, Message: problem.Message}}
	var damaged *folio.ImageError
	if errors.As(err, &damaged) {
		report.ImageVersion = damaged.Version.String()
		report.Segment = damaged.Segment
		report.Layer = damaged.Layer
	}
	var semantic *db.LayerImageError
	if errors.As(err, &semantic) {
		report.Layer = semantic.Layer
	}
	a.json(w, code, report)
}
