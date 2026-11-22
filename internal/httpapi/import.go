package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// importBatchRequest mirrors catalog.ImportBatch; named explicitly so the
// strict decoder keeps the request contract visible in one place.
type importBatchRequest = catalog.ImportBatch

func (a *api) importFeatures(w http.ResponseWriter, r *http.Request) {
	var request importBatchRequest
	if a.input(w, r, &request) {
		commit, err := a.db.Import(request)
		a.respond(w, http.StatusOK, commit, err)
	}
}
