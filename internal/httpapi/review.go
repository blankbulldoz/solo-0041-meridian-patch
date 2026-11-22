package httpapi

import (
	"net/http"
)

// sessionHeader carries an optional review session token on read requests.
const sessionHeader = "Meridian-Session"

// sessionToken reads the optional review session header. More than one value
// is rejected so view selection stays unambiguous.
func (a *api) sessionToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values(sessionHeader)
	if len(values) > 1 {
		a.json(w, http.StatusBadRequest, issue{"invalid", "supply at most one " + sessionHeader + " header"})
		return "", false
	}
	if len(values) == 1 {
		return values[0], true
	}
	return "", true
}

func (a *api) openReview(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if a.input(w, r, &request) {
		info, err := a.db.OpenReview()
		a.respond(w, http.StatusCreated, info, err)
	}
}

func (a *api) closeReview(w http.ResponseWriter, r *http.Request) {
	result, err := a.db.CloseReview(r.PathValue("session"))
	a.respond(w, http.StatusOK, result, err)
}
