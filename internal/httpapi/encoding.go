package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const maxJSON = 1 << 20

type issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *api) json(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		a.log.Warn("write response", "error", err)
	}
}

func (a *api) input(w http.ResponseWriter, r *http.Request, target any) bool {
	return a.inputLimit(w, r, target, maxJSON)
}

func (a *api) inputLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	if !a.media(w, r, "application/json") {
		return false
	}
	reader := http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		a.readError(w, err)
		return false
	}
	if len(raw) == 0 || raw[0] != '{' {
		a.json(w, http.StatusBadRequest, issue{"invalid", "body must be a JSON object"})
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON")
		}
		a.readError(w, err)
		return false
	}
	if err := decodeObject(raw, target); err != nil {
		a.readError(w, err)
		return false
	}
	return true
}

func (a *api) media(w http.ResponseWriter, r *http.Request, expected string) bool {
	actual, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && actual == expected {
		return true
	}
	a.json(w, http.StatusUnsupportedMediaType, issue{"media_type", "expected " + expected})
	return false
}

func (a *api) readError(w http.ResponseWriter, err error) {
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		a.json(w, http.StatusRequestEntityTooLarge, issue{"capacity", "request body exceeds size limit"})
		return
	}
	a.json(w, http.StatusBadRequest, issue{"invalid", err.Error()})
}
