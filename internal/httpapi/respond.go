package httpapi

import (
	"errors"
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
)

func (a *api) respond(w http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		a.json(w, status, value)
		return
	}
	var problem *db.Error
	if !errors.As(err, &problem) {
		a.log.Error("database operation failed", "error", err)
		a.json(w, http.StatusInternalServerError, issue{Code: "internal", Message: "database operation failed"})
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
	a.json(w, code, issue{Code: problem.Code, Message: problem.Message, Details: problem.Details})
}
