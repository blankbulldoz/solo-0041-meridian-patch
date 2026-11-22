package db

import "github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"

// Export encodes one committed revision. Version 2 is the current format and
// version 1 produces the legacy single-payload image for older instances.
func (d *Database) Export(version folio.Version) ([]byte, uint64, error) {
	if !version.Supported() {
		return nil, 0, invalid("unsupported image version %d", version)
	}
	s := d.head.Load()
	raw, err := folio.Encode(s.image(), version)
	return raw, s.revision, err
}

// Restore installs any supported image version under a target-revision CAS.
// The returned version says which container was imported.
func (d *Database) Restore(raw []byte, expected uint64) (Commit, folio.Version, error) {
	image, err := folio.Decode(raw)
	if err != nil {
		return Commit{}, 0, invalidCause(err, "restore image: %v", err)
	}
	candidate, err := fromImage(image.Catalog)
	if err != nil {
		return Commit{}, image.Version, invalidCause(err, "restore image: %v", err)
	}
	s, err := d.commit(&expected, func(_ *state, revision uint64) (*state, error) {
		candidate.revision = revision
		for _, l := range candidate.layers {
			l.revision = revision
		}
		return candidate, nil
	})
	if err != nil {
		return Commit{}, image.Version, err
	}
	return summaries(s, s.names()), image.Version, nil
}
