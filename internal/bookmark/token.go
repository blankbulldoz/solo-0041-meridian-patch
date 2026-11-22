// Package bookmark signs self-contained tokens. Window continuations bind a
// predicate and layer revision; transplant previews bind the selected features
// and the target layer revision they were checked against. Nothing is stored
// server-side, so a token is only accepted by the process that issued it.
package bookmark

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

var ErrInvalid = errors.New("invalid signed token")

const (
	maxScopeToken  = 1024
	maxPlanPayload = 4 << 20
	maxPlanToken   = 6 << 20
)

type Signer struct{ key [32]byte }

func New() (Signer, error) {
	var signer Signer
	_, err := rand.Read(signer.key[:])
	return signer, err
}

type Scope struct {
	Layer    string    `json:"layer"`
	Bounds   plane.Box `json:"bounds"`
	Class    string    `json:"class"`
	Revision uint64    `json:"revision"`
}

type payload struct {
	Scope Scope  `json:"scope"`
	After string `json:"after"`
}

func (s Signer) Seal(scope Scope, after string) string {
	raw, _ := json.Marshal(payload{Scope: scope, After: after})
	return s.seal(raw)
}

func (s Signer) Open(token string) (Scope, string, error) {
	raw, err := s.open(token, maxScopeToken)
	if err != nil {
		return Scope{}, "", ErrInvalid
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Scope{}, "", ErrInvalid
	}
	return p.Scope, p.After, nil
}

// SealValue signs an arbitrary JSON value. Plan tokens travel back to the
// server with the next call, so the previewed content cannot be edited without
// invalidating the signature.
func (s Signer) SealValue(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(raw) > maxPlanPayload {
		return "", ErrInvalid
	}
	return s.seal(raw), nil
}

func (s Signer) OpenValue(token string, value any) error {
	raw, err := s.open(token, maxPlanToken)
	if err != nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func (s Signer) seal(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(s.mac(raw))
}

func (s Signer) open(token string, limit int) ([]byte, error) {
	if len(token) > limit {
		return nil, ErrInvalid
	}
	body, tag, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil {
		return nil, ErrInvalid
	}
	given, err := base64.RawURLEncoding.Strict().DecodeString(tag)
	if err != nil || !hmac.Equal(given, s.mac(raw)) {
		return nil, ErrInvalid
	}
	return raw, nil
}

func (s Signer) mac(raw []byte) []byte {
	m := hmac.New(sha256.New, s.key[:])
	m.Write(raw)
	return m.Sum(nil)
}
