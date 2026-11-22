// Package httpapi exposes Meridian Patch's catalog, query and image protocols.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/db"
)

const Prefix = "/meridian/v1"

type api struct {
	db  *db.Database
	log *slog.Logger
}

func New(database *db.Database, log *slog.Logger) http.Handler {
	a := &api{db: database, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"/status", a.status)
	mux.HandleFunc("GET "+Prefix+"/strata", a.list)
	mux.HandleFunc("POST "+Prefix+"/strata", a.register)
	mux.HandleFunc("PUT "+Prefix+"/strata/{layer}/layout", a.reframe)
	mux.HandleFunc("POST "+Prefix+"/edits", a.edit)
	mux.HandleFunc("POST "+Prefix+"/strata/{layer}/window", a.window)
	mux.HandleFunc("POST "+Prefix+"/strata/{layer}/locate", a.locate)
	mux.HandleFunc("POST "+Prefix+"/strata/{layer}/nearest", a.nearest)
	mux.HandleFunc("POST "+Prefix+"/strata/{layer}/coverage", a.coverage)
	mux.HandleFunc("POST "+Prefix+"/strata/{layer}/grid", a.grid)
	mux.HandleFunc("GET "+Prefix+"/strata/{layer}/index", a.inspect)
	mux.HandleFunc("GET "+Prefix+"/strata/{layer}/index-audit", a.audit)
	mux.HandleFunc("POST "+Prefix+"/strata/{layer}/seams", a.seams)
	mux.HandleFunc("POST "+Prefix+"/join", a.join)
	mux.HandleFunc("POST "+Prefix+"/relation", a.relation)
	mux.HandleFunc("GET "+Prefix+"/image", a.export)
	mux.HandleFunc("PUT "+Prefix+"/image", a.restore)
	return mux
}

func (a *api) status(w http.ResponseWriter, _ *http.Request) {
	status := a.db.Status()
	code := http.StatusOK
	if !status.Writable {
		code = http.StatusServiceUnavailable
	}
	a.json(w, code, status)
}
