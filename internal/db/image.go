package db

import "github.com/blankbulldoz/solo-0041-meridian-patch/internal/folio"

func readImage(raw []byte) (*state, error) {
	image, err := folio.Decode(raw)
	if err != nil {
		return nil, err
	}
	return fromImage(image)
}

func (d *Database) Export() ([]byte, uint64, error) {
	s := d.head.Load()
	raw, err := folio.Encode(s.image())
	return raw, s.revision, err
}

func (d *Database) Restore(raw []byte, expected uint64) (Commit, error) {
	candidate, err := readImage(raw)
	if err != nil {
		return Commit{}, invalid("restore image: %v", err)
	}
	s, err := d.commit(&expected, func(_ *state, revision uint64) (*state, error) {
		candidate.revision = revision
		for _, l := range candidate.layers {
			l.revision = revision
		}
		return candidate, nil
	})
	if err != nil {
		return Commit{}, err
	}
	return summaries(s, s.names()), nil
}
