package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

type createLayerRequest struct {
	catalog.Definition
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

type reframe struct {
	catalog.Layout
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

func (a *api) list(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.db.List())
}

func (a *api) register(w http.ResponseWriter, r *http.Request) {
	var request createLayerRequest
	if a.input(w, r, &request) {
		commit, err := a.db.Register(request.Definition, request.IfRevision)
		a.respond(w, http.StatusCreated, commit, err)
	}
}

func (a *api) reframe(w http.ResponseWriter, r *http.Request) {
	var request reframe
	if a.input(w, r, &request) {
		commit, err := a.db.Reframe(r.PathValue("layer"), request.Layout, request.IfRevision)
		a.respond(w, http.StatusOK, commit, err)
	}
}

func (a *api) edit(w http.ResponseWriter, r *http.Request) {
	var request catalog.EditBatch
	if a.input(w, r, &request) {
		commit, err := a.db.Edit(request)
		a.respond(w, http.StatusOK, commit, err)
	}
}

func (a *api) editGroups(w http.ResponseWriter, r *http.Request) {
	var request catalog.GroupBatch
	if a.input(w, r, &request) {
		commit, err := a.db.EditGroups(request)
		a.respond(w, http.StatusOK, commit, err)
	}
}
