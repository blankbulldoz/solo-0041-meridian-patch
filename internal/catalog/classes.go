package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// MaxClassList bounds the number of distinct classes in one allow or exclude
// list. Duplicates do not count against the limit because they are removed
// before the check.
const MaxClassList = 256

// ClassList records whether a JSON class-list field was present. For the allow
// side an omitted list and an explicitly empty list are different predicates:
// omitted means unrestricted, empty means no class is allowed.
type ClassList struct {
	Present bool
	Names   []string
}

func (l *ClassList) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("class list must be an array")
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return errors.New("class list must be an array of class names")
	}
	l.Present = true
	l.Names = names
	return nil
}

// ClassFilter is the canonical category predicate used by windows and bound to
// their bookmarks. A nil Allow admits every class; a non-nil Allow admits only
// its members after Exclude is removed. Exclude is only kept when Allow is
// omitted, because removing a class from a finite allow list and listing it in
// Exclude describe the same predicate.
type ClassFilter struct {
	Allow   *[]string `json:"allow_classes"`
	Exclude []string  `json:"exclude_classes"`
}

// BuildClassFilter combines the legacy single class with the optional allow and
// exclude lists. The single class is equivalent to an allow list with one item.
func BuildClassFilter(single string, allow, exclude ClassList) (ClassFilter, error) {
	var filter ClassFilter
	if single != "" && allow.Present {
		return filter, errors.New("class and allow_classes are mutually exclusive")
	}
	excluded, err := canonicalClasses(exclude.Names)
	if err != nil {
		return filter, err
	}
	var allowed []string
	switch {
	case single != "":
		allowed, err = canonicalClasses([]string{single})
	case allow.Present:
		allowed, err = canonicalClasses(allow.Names)
	default:
		filter.Exclude = excluded
		return filter, nil
	}
	if err != nil {
		return filter, err
	}
	kept := admitted(allowed, excluded)
	filter.Allow = &kept
	filter.Exclude = make([]string, 0)
	return filter, nil
}

func canonicalClasses(names []string) ([]string, error) {
	for _, name := range names {
		if !ValidName(name) {
			return nil, fmt.Errorf("invalid class %q", name)
		}
	}
	out := slices.Clone(names)
	slices.Sort(out)
	out = slices.Compact(out)
	if out == nil {
		out = make([]string, 0)
	}
	if len(out) > MaxClassList {
		return nil, fmt.Errorf("class list exceeds %d distinct entries", MaxClassList)
	}
	return out, nil
}

// Matches reports whether a class belongs to the allow side and not to the
// exclude side. Both lists are canonical and therefore sorted.
func (f ClassFilter) Matches(class string) bool {
	if f.Allow != nil {
		if _, found := slices.BinarySearch(*f.Allow, class); !found {
			return false
		}
	}
	_, excluded := slices.BinarySearch(f.Exclude, class)
	return !excluded
}

// Fingerprint identifies the predicate after canonicalization. Bookmarks bind
// this digest instead of the raw lists, so a large exclude list does not inflate
// the token and harmless spelling differences cannot split a continuation.
func (f ClassFilter) Fingerprint() string {
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func admitted(allow, exclude []string) []string {
	if len(exclude) == 0 {
		return allow
	}
	out := make([]string, 0, len(allow))
	for _, name := range allow {
		if _, excluded := slices.BinarySearch(exclude, name); !excluded {
			out = append(out, name)
		}
	}
	return out
}
