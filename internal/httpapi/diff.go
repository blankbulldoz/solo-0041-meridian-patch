package httpapi

import (
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"
)

// The diff request carries two base64 MPB images. Base64 expands each raw
// image by 4/3, so two maximum-size images plus the JSON envelope stay
// comfortably below three raw image sizes.
const maxDiffBody = 3 * folio.MaxImage

func (a *api) diff(w http.ResponseWriter, r *http.Request) {
	var request db.DiffQuery
	if a.inputLimit(w, r, &request, maxDiffBody) {
		result, err := db.DiffImages(request)
		a.respond(w, http.StatusOK, result, err)
	}
}
