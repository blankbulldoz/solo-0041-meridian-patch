package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// MaxRequestRecords bounds how many accepted edit batches keep a retry record.
// The ledger travels inside the database image, so the old entries are dropped
// as new commits arrive rather than growing with process uptime.
const MaxRequestRecords = 64

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func ValidRequestID(id string) bool {
	return requestPattern.MatchString(id)
}

// RequestRecord is the durable answer to one accepted /edits request. It is
// deliberately independent of the live catalog: a replay reports the commit
// that the caller originally observed, not the current state of those layers.
type RequestRecord struct {
	ID       string    `json:"id"`
	Digest   string    `json:"digest"`
	Revision uint64    `json:"revision"`
	Layers   []Summary `json:"layers"`
}

func (r RequestRecord) Validate(imageRevision uint64) error {
	if !ValidRequestID(r.ID) {
		return fmt.Errorf("request record has invalid id %q", r.ID)
	}
	if len(r.Digest) != sha256.Size*2 {
		return fmt.Errorf("request record %q has invalid digest length", r.ID)
	}
	for _, digit := range r.Digest {
		if !(digit >= '0' && digit <= '9') && !(digit >= 'a' && digit <= 'f') {
			return fmt.Errorf("request record %q has non-canonical digest", r.ID)
		}
	}
	if r.Revision == 0 || r.Revision > imageRevision {
		return fmt.Errorf("request record %q has revision outside the image", r.ID)
	}
	if len(r.Layers) == 0 || len(r.Layers) > MaxLayers {
		return fmt.Errorf("request record %q has invalid layer count", r.ID)
	}
	var previous string
	for _, layer := range r.Layers {
		if layer.Name <= previous {
			return fmt.Errorf("request record %q has unordered or duplicate layers", r.ID)
		}
		previous = layer.Name
		if layer.Revision == 0 || layer.Revision > imageRevision {
			return fmt.Errorf("request record %q has layer revision outside the image", r.ID)
		}
		if layer.Count < 0 || layer.Count > MaxLayerRows {
			return fmt.Errorf("request record %q has invalid layer count", r.ID)
		}
		if err := layer.Definition.Validate(); err != nil {
			return fmt.Errorf("request record %q: %w", r.ID, err)
		}
	}
	return nil
}

// EditDigest is the canonical content fingerprint of an edit batch. The
// optional revision precondition belongs to the content: retrying with a
// changed precondition is a different request, while an unchanged retry keeps
// matching even though that revision has since been surpassed.
func EditDigest(batch EditBatch) string {
	material := struct {
		IfRevision *uint64 `json:"if_revision,omitempty"`
		Edits      []Edit  `json:"edits"`
	}{batch.IfRevision, batch.Edits}
	raw, _ := json.Marshal(material)
	sum := sha256.Sum256(append([]byte("meridian/edits/v1\n"), raw...))
	return hex.EncodeToString(sum[:])
}

// FindRequest returns the retained record for id, if it is still in the window.
func FindRequest(records []RequestRecord, id string) (RequestRecord, bool) {
	for _, record := range records {
		if record.ID == id {
			return record, true
		}
	}
	return RequestRecord{}, false
}

// AppendRequest adds a record in commit order and drops the oldest records
// beyond the retention window. The input slice is never mutated.
func AppendRequest(records []RequestRecord, record RequestRecord) []RequestRecord {
	out := append(slices.Clone(records), record)
	if len(out) > MaxRequestRecords {
		out = out[len(out)-MaxRequestRecords:]
	}
	return out
}

// RebaseRequests rewrites a restored ledger onto the revision assigned by the
// target database. A record's observed revision is the revision the restored
// catalog now carries, so imported identifiers answer with that revision
// instead of a revision number that belongs to the source database.
func RebaseRequests(records []RequestRecord, revision uint64) []RequestRecord {
	if len(records) == 0 {
		return records
	}
	out := make([]RequestRecord, len(records))
	for i, record := range records {
		record.Revision = revision
		layers := make([]Summary, len(record.Layers))
		for j, layer := range record.Layers {
			layer.Revision = revision
			layers[j] = layer
		}
		record.Layers = layers
		out[i] = record
	}
	return out
}
