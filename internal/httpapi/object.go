package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

func decodeObject(raw []byte, target any) error {
	if err := checkMembers(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// Validate duplicate members before mapping JSON into Go structs. Matching
// struct fields is case-insensitive in encoding/json, so aliases collide too.
func checkMembers(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name := key.(string)
			if !canonicalKey(name) {
				return fmt.Errorf("invalid JSON field %q", name)
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON field %q", name)
			}
			seen[name] = true
			if err := checkMembers(d, depth+1); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for d.More() {
			if err := checkMembers(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	_, err = d.Token()
	return err
}

func canonicalKey(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if (ch < 'a' || ch > 'z') && ch != '_' {
			return false
		}
	}
	return true
}
