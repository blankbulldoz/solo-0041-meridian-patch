package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
)

func (a *api) assign(w http.ResponseWriter, r *http.Request) {
	var request db.AssignmentQuery
	if a.input(w, r, &request) {
		result, err := a.db.AssignMarkers(request)
		a.respond(w, http.StatusOK, result, err)
	}
}
