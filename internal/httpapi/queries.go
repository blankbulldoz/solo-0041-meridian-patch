package httpapi

import (
	"net/http"
	"strconv"

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
	bounds, windowed, ok := a.auditWindow(w, r)
	if !ok {
		return
	}
	if !windowed {
		result, err := a.db.Audit(r.PathValue("layer"))
		a.respond(w, http.StatusOK, result, err)
		return
	}
	result, err := a.db.AuditWindow(r.PathValue("layer"), bounds)
	a.respond(w, http.StatusOK, result, err)
}

// auditWindow reads the optional window for an index audit. Supplying none of
// the four edges keeps the original whole-layer audit; supplying all four runs
// the windowed identity check. A partial window is rejected.
func (a *api) auditWindow(w http.ResponseWriter, r *http.Request) (plane.Box, bool, bool) {
	values := r.URL.Query()
	keys := []string{"min_x", "min_y", "max_x", "max_y"}
	seen := 0
	for _, key := range keys {
		if values.Has(key) {
			seen++
		}
	}
	if seen == 0 {
		return plane.Box{}, false, true
	}
	if seen != len(keys) {
		a.json(w, http.StatusBadRequest, issue{Code: "invalid", Message: "window requires min_x, min_y, max_x and max_y together"})
		return plane.Box{}, false, false
	}
	var bounds plane.Box
	edges := map[string]*int64{
		"min_x": &bounds.MinX, "min_y": &bounds.MinY,
		"max_x": &bounds.MaxX, "max_y": &bounds.MaxY,
	}
	for key, target := range edges {
		value, err := strconv.ParseInt(values.Get(key), 10, 64)
		if err != nil {
			a.json(w, http.StatusBadRequest, issue{Code: "invalid", Message: "window edge " + key + " must be an integer"})
			return plane.Box{}, false, false
		}
		*target = value
	}
	return bounds, true, true
}
