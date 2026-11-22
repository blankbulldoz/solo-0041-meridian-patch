// Package bookmark binds window continuation to one predicate, a layer
// revision, and optionally a stable window session.
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

type Scope struct {
	Layer    string    `json:"layer"`
	Bounds   plane.Box `json:"bounds"`
	Class    string    `json:"class"`
	Revision uint64    `json:"revision"`
}

type payload struct {
	Scope   Scope  `json:"scope"`
	After   string `json:"after"`
	Session uint64 `json:"session,omitempty"`
}

// Token is an opened bookmark. Session is zero for a default bookmark that
// expires with the layer revision, and nonzero for a stable window session.
type Token struct {
	Scope   Scope
	After   string
	Session uint64
}

func (s Signer) Seal(scope Scope, after string, session uint64) string {
	raw, _ := json.Marshal(payload{Scope: scope, After: after, Session: session})
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(s.mac(raw))
}

func (s Signer) Open(token string) (Token, error) {
	if len(token) > 1024 {
		return Token{}, ErrInvalid
	}
	body, tag, ok := strings.Cut(token, ".")
	if !ok {
		return Token{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil {
		return Token{}, ErrInvalid
	}
	given, err := base64.RawURLEncoding.Strict().DecodeString(tag)
	if err != nil || !hmac.Equal(given, s.mac(raw)) {
		return Token{}, ErrInvalid
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Token{}, ErrInvalid
	}
	return Token{Scope: p.Scope, After: p.After, Session: p.Session}, nil
}

func (s Signer) mac(raw []byte) []byte {
	m := hmac.New(sha256.New, s.key[:])
	m.Write(raw)
	return m.Sum(nil)
}
