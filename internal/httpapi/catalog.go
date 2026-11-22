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

func (a *api) list(w http.ResponseWriter, r *http.Request) {
	branch, ok := a.branchInput(w, r)
	if !ok {
		return
	}
	if branch == "" {
		a.json(w, http.StatusOK, a.db.List())
		return
	}
	commit, err := a.db.ListBranch(branch)
	a.respond(w, http.StatusOK, commit, err)
}

// Formal packs never consult the branch selector. A stray ?branch= on them
// would silently change the wrong catalog, so it is rejected instead.
func (a *api) formalOnly(w http.ResponseWriter, r *http.Request) bool {
	if _, present := r.URL.Query()[branchParam]; present {
		a.json(w, http.StatusBadRequest, issue{"invalid", "branch packs use /branches/{branch}/...; remove the branch parameter"})
		return false
	}
	return true
}

func (a *api) register(w http.ResponseWriter, r *http.Request) {
	var request createLayerRequest
	if a.formalOnly(w, r) && a.input(w, r, &request) {
		commit, err := a.db.Register(request.Definition, request.IfRevision)
		a.respond(w, http.StatusCreated, commit, err)
	}
}

func (a *api) reframe(w http.ResponseWriter, r *http.Request) {
	var request reframe
	if a.formalOnly(w, r) && a.input(w, r, &request) {
		commit, err := a.db.Reframe(r.PathValue("layer"), request.Layout, request.IfRevision)
		a.respond(w, http.StatusOK, commit, err)
	}
}

func (a *api) edit(w http.ResponseWriter, r *http.Request) {
	var request catalog.EditBatch
	if a.formalOnly(w, r) && a.input(w, r, &request) {
		commit, err := a.db.Edit(request)
		a.respond(w, http.StatusOK, commit, err)
	}
}
