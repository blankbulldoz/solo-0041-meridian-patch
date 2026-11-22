// Package bookmark binds window continuation and window subscriptions to one predicate.
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

// Subscription binds a change stream to one layer, window, class and the
// database revision at which the initial snapshot was taken.
type Subscription struct {
	Layer    string    `json:"layer"`
	Bounds   plane.Box `json:"bounds"`
	Class    string    `json:"class"`
	Revision uint64    `json:"revision"`
}

type subscriptionPayload struct {
	Kind string       `json:"kind"`
	Sub  Subscription `json:"subscription"`
}

// SealSubscription signs a subscription descriptor. The kind marker keeps
// subscription tokens from being accepted as window bookmarks.
func (s Signer) SealSubscription(sub Subscription) string {
	raw, _ := json.Marshal(subscriptionPayload{Kind: "subscription", Sub: sub})
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(s.mac(raw))
}

// OpenSubscription verifies a token and rejects window bookmarks or altered predicates.
func (s Signer) OpenSubscription(token string) (Subscription, error) {
	if len(token) > 1024 {
		return Subscription{}, ErrInvalid
	}
	body, tag, ok := strings.Cut(token, ".")
	if !ok {
		return Subscription{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil {
		return Subscription{}, ErrInvalid
	}
	given, err := base64.RawURLEncoding.Strict().DecodeString(tag)
	if err != nil || !hmac.Equal(given, s.mac(raw)) {
		return Subscription{}, ErrInvalid
	}
	var p subscriptionPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.Kind != "subscription" {
		return Subscription{}, ErrInvalid
	}
	return p.Sub, nil
}
