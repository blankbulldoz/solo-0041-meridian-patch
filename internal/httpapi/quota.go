package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

type registerQuotaRequest struct {
	catalog.Quota
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

type removeQuotaRequest struct {
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

func (a *api) quotas(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.db.Quotas())
}

func (a *api) registerQuota(w http.ResponseWriter, r *http.Request) {
	var request registerQuotaRequest
	if a.input(w, r, &request) {
		commit, err := a.db.RegisterQuota(request.Quota, request.IfRevision)
		a.respond(w, http.StatusCreated, commit, err)
	}
}

func (a *api) removeQuota(w http.ResponseWriter, r *http.Request) {
	var request removeQuotaRequest
	if a.input(w, r, &request) {
		commit, err := a.db.RemoveQuota(r.PathValue("quota"), request.IfRevision)
		a.respond(w, http.StatusOK, commit, err)
	}
}
