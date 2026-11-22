package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

func (a *api) window(w http.ResponseWriter, r *http.Request) {
	var request db.WindowQuery
	if a.input(w, r, &request) {
		result, err := a.db.Window(r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) abandonWindow(w http.ResponseWriter, r *http.Request) {
	var request db.AbandonQuery
	if a.input(w, r, &request) {
		result, err := a.db.AbandonWindow(r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) locate(w http.ResponseWriter, r *http.Request) {
	var request plane.Point
	if a.input(w, r, &request) {
		result, err := a.db.Locate(r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) nearest(w http.ResponseWriter, r *http.Request) {
	var request db.NearestQuery
	if a.input(w, r, &request) {
		result, err := a.db.Nearest(r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) coverage(w http.ResponseWriter, r *http.Request) {
	var request db.CoverageQuery
	if a.input(w, r, &request) {
		result, err := a.db.Coverage(r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) inspect(w http.ResponseWriter, r *http.Request) {
	result, err := a.db.Inspect(r.PathValue("layer"))
	a.respond(w, http.StatusOK, result, err)
}

func (a *api) join(w http.ResponseWriter, r *http.Request) {
	var request db.JoinQuery
	if a.input(w, r, &request) {
		result, err := a.db.Join(request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) seams(w http.ResponseWriter, r *http.Request) {
	var request db.SeamQuery
	if a.input(w, r, &request) {
		result, err := a.db.Seams(r.PathValue("layer"), request)
		a.respond(w, http.StatusOK, result, err)
	}
}

func (a *api) audit(w http.ResponseWriter, r *http.Request) {
	result, err := a.db.Audit(r.PathValue("layer"))
	a.respond(w, http.StatusOK, result, err)
}
