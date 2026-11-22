package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
)

func (a *api) subscribe(w http.ResponseWriter, r *http.Request) {
	var request db.SubscriptionRequest
	if a.input(w, r, &request) {
		snapshot, err := a.db.Subscribe(r.PathValue("layer"), request)
		a.respond(w, http.StatusCreated, snapshot, err)
	}
}

func (a *api) subscriptionEvents(w http.ResponseWriter, r *http.Request) {
	var request db.EventsRequest
	if a.input(w, r, &request) {
		result, err := a.db.Events(r.Context(), request)
		a.respond(w, http.StatusOK, result, err)
	}
}
