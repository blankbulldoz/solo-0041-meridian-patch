package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
)

func (a *api) relation(w http.ResponseWriter, r *http.Request) {
	var query db.RelationQuery
	if a.input(w, r, &query) {
		result, err := a.db.Relate(query)
		a.respond(w, http.StatusOK, result, err)
	}
}
