// Package bookmark binds window and join continuations to their predicate
// and the revisions of the layers they read.
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

// JoinScope binds a join continuation to both layers, the shared window and
// the revision of each side, so a change on either side expires the token.
type JoinScope struct {
	Left          string    `json:"left"`
	Right         string    `json:"right"`
	Bounds        plane.Box `json:"bounds"`
	LeftRevision  uint64    `json:"left_revision"`
	RightRevision uint64    `json:"right_revision"`
}

type payload struct {
	Scope Scope  `json:"scope"`
	After string `json:"after"`
}

type joinPayload struct {
	Scope JoinScope `json:"scope"`
	Left  string    `json:"left"`
	Right string    `json:"right"`
}

func (s Signer) Seal(scope Scope, after string) string {
	raw, _ := json.Marshal(payload{Scope: scope, After: after})
	return s.seal(raw)
}

func (s Signer) Open(token string) (Scope, string, error) {
	raw, err := s.open(token)
	if err != nil {
		return Scope{}, "", err
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Scope{}, "", ErrInvalid
	}
	return p.Scope, p.After, nil
}

// SealJoin records the last pair of a join page as the resume position.
func (s Signer) SealJoin(scope JoinScope, left, right string) string {
	raw, _ := json.Marshal(joinPayload{Scope: scope, Left: left, Right: right})
	return s.seal(raw)
}

// OpenJoin returns the join scope and the (left, right) resume position.
func (s Signer) OpenJoin(token string) (JoinScope, string, string, error) {
	raw, err := s.open(token)
	if err != nil {
		return JoinScope{}, "", "", err
	}
	var p joinPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return JoinScope{}, "", "", ErrInvalid
	}
	return p.Scope, p.Left, p.Right, nil
}

func (s Signer) seal(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(s.mac(raw))
}

func (s Signer) open(token string) ([]byte, error) {
	if len(token) > 1024 {
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
