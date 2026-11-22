// Package bookmark binds window continuation to one predicate and layer revision.
package bookmark

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/plane"
)

var ErrInvalid = errors.New("invalid window bookmark")

type Signer struct{ key [32]byte }

func New() (Signer, error) {
	var signer Signer
	_, err := rand.Read(signer.key[:])
	return signer, err
}

// Scope binds a continuation to one layer revision and one canonical category
// predicate. The predicate travels as a digest: duplicate or reordered class
// lists in a later request canonicalize to the same digest, while any semantic
// difference produces a different one, and long lists cannot overflow the token.
type Scope struct {
	Layer    string    `json:"layer"`
	Bounds   plane.Box `json:"bounds"`
	Classes  string    `json:"classes"`
	Revision uint64    `json:"revision"`
}

type payload struct {
	Scope Scope  `json:"scope"`
	After string `json:"after"`
}

func (s Signer) Seal(scope Scope, after string) string {
	raw, _ := json.Marshal(payload{Scope: scope, After: after})
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(s.mac(raw))
}

func (s Signer) Open(token string) (Scope, string, error) {
	if len(token) > 1024 {
		return Scope{}, "", ErrInvalid
	}
	body, tag, ok := strings.Cut(token, ".")
	if !ok {
		return Scope{}, "", ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil {
		return Scope{}, "", ErrInvalid
	}
	given, err := base64.RawURLEncoding.Strict().DecodeString(tag)
	if err != nil || !hmac.Equal(given, s.mac(raw)) {
		return Scope{}, "", ErrInvalid
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Scope{}, "", ErrInvalid
	}
	return p.Scope, p.After, nil
}

func (s Signer) mac(raw []byte) []byte {
	m := hmac.New(sha256.New, s.key[:])
	m.Write(raw)
	return m.Sum(nil)
}
