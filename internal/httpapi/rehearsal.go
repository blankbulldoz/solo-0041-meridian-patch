package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
)

// rehearse evaluates a write pack without landing it. A pack that would be
// rejected returns the same status and problem code that POST /edits would
// produce (invalid/missing/conflict/capacity); only a pack that passes every
// final-state constraint yields a 200 verdict with probe projections.
func (a *api) rehearse(w http.ResponseWriter, r *http.Request) {
	var request db.RehearsalRequest
	if a.input(w, r, &request) {
		result, err := a.db.Rehearse(request)
		a.respond(w, http.StatusOK, result, err)
	}
}
