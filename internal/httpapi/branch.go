package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

// A branch selector reaches every read and pack endpoint as a query parameter.
// An empty value means the formal database. Branches are views over the same
// catalog, so this changes which immutable state the request captures, not the
// request shape.
const branchParam = "branch"

func branchOf(r *http.Request) (string, bool) {
	values := r.URL.Query()[branchParam]
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 || !catalog.ValidName(values[0]) {
		return "", false
	}
	return values[0], true
}

func (a *api) branchInput(w http.ResponseWriter, r *http.Request) (string, bool) {
	branch, ok := branchOf(r)
	if !ok {
		a.json(w, http.StatusBadRequest, issue{"invalid", "branch must be one valid branch name"})
		return "", false
	}
	return branch, true
}

type createBranchRequest struct {
	Name       string  `json:"name"`
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

type publishBranchRequest struct {
	IfRevision *uint64 `json:"if_revision,omitempty"`
}

func (a *api) branches(w http.ResponseWriter, _ *http.Request) {
	a.json(w, http.StatusOK, a.db.Branches())
}

func (a *api) createBranch(w http.ResponseWriter, r *http.Request) {
	var request createBranchRequest
	if a.input(w, r, &request) {
		view, err := a.db.CreateBranch(request.Name, request.IfRevision)
		a.respond(w, http.StatusCreated, view, err)
	}
}

func (a *api) branch(w http.ResponseWriter, r *http.Request) {
	view, err := a.db.Branch(r.PathValue("branch"))
	a.respond(w, http.StatusOK, view, err)
}

func (a *api) branchRegister(w http.ResponseWriter, r *http.Request) {
	var request createLayerRequest
	if a.input(w, r, &request) {
		view, err := a.db.BranchRegister(r.PathValue("branch"), request.Definition, request.IfRevision)
		a.respond(w, http.StatusOK, view, err)
	}
}

func (a *api) branchReframe(w http.ResponseWriter, r *http.Request) {
	var request reframe
	if a.input(w, r, &request) {
		view, err := a.db.BranchReframe(r.PathValue("branch"), r.PathValue("layer"), request.Layout, request.IfRevision)
		a.respond(w, http.StatusOK, view, err)
	}
}

func (a *api) branchEdit(w http.ResponseWriter, r *http.Request) {
	var request catalog.EditBatch
	if a.input(w, r, &request) {
		view, err := a.db.BranchEdit(r.PathValue("branch"), request)
		a.respond(w, http.StatusOK, view, err)
	}
}

func (a *api) publishBranch(w http.ResponseWriter, r *http.Request) {
	var request publishBranchRequest
	if a.input(w, r, &request) {
		commit, err := a.db.PublishBranch(r.PathValue("branch"), request.IfRevision)
		a.respond(w, http.StatusCreated, commit, err)
	}
}

func (a *api) discardBranch(w http.ResponseWriter, r *http.Request) {
	result, err := a.db.DiscardBranch(r.PathValue("branch"))
	a.respond(w, http.StatusOK, result, err)
}
