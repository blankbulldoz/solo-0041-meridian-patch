package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func (a *api) window(w http.ResponseWriter, r *http.Request) {
	var request db.WindowQuery
	branch, ok := a.branchInput(w, r)
	if ok && a.input(w, r, &request) {
		result, err := a.db.Window(branch, r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) locate(w http.ResponseWriter, r *http.Request) {
	var request plane.Point
	branch, ok := a.branchInput(w, r)
	if ok && a.input(w, r, &request) {
		result, err := a.db.Locate(branch, r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) nearest(w http.ResponseWriter, r *http.Request) {
	var request db.NearestQuery
	branch, ok := a.branchInput(w, r)
	if ok && a.input(w, r, &request) {
		result, err := a.db.Nearest(branch, r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) coverage(w http.ResponseWriter, r *http.Request) {
	var request db.CoverageQuery
	branch, ok := a.branchInput(w, r)
	if ok && a.input(w, r, &request) {
		result, err := a.db.Coverage(branch, r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) inspect(w http.ResponseWriter, r *http.Request) {
	branch, ok := a.branchInput(w, r)
	if !ok {
		return
	}
	result, err := a.db.Inspect(branch, r.PathValue("layer"))
	a.respond(w, http.StatusOK, result, err)
}

func (a *api) join(w http.ResponseWriter, r *http.Request) {
	var request db.JoinQuery
	branch, ok := a.branchInput(w, r)
	if ok && a.input(w, r, &request) {
		result, err := a.db.Join(branch, request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) seams(w http.ResponseWriter, r *http.Request) {
	var request db.SeamQuery
	branch, ok := a.branchInput(w, r)
	if ok && a.input(w, r, &request) {
		result, err := a.db.Seams(branch, r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) audit(w http.ResponseWriter, r *http.Request) {
	branch, ok := a.branchInput(w, r)
	if !ok {
		return
	}
	result, err := a.db.Audit(branch, r.PathValue("layer"))
	a.respond(w, http.StatusOK, result, err)
}
