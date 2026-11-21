// Package folio encodes portable database images and replaces persistent state.
package folio

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

const MaxImage = 16 << 20
const headerSize = 8 + 4 + sha256.Size

var signature = [8]byte{'M', 'R', 'D', 'N', 'M', 'P', 'B', 1}

func Encode(image catalog.CatalogImage) ([]byte, error) {
	payload, err := json.Marshal(image)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxImage-headerSize {
		return nil, errors.New("image exceeds maximum size")
	}
	sum := sha256.Sum256(payload)
	bytes := make([]byte, headerSize+len(payload))
	copy(bytes, signature[:])
	binary.BigEndian.PutUint32(bytes[8:12], uint32(len(payload)))
	copy(bytes[12:], sum[:])
	copy(bytes[headerSize:], payload)
	return bytes, nil
}

func Decode(raw []byte) (catalog.CatalogImage, error) {
	var image catalog.CatalogImage
	if len(raw) < headerSize || len(raw) > MaxImage || !bytes.Equal(raw[:8], signature[:]) {
		return image, errors.New("invalid image size or version")
	}
	payload := raw[headerSize:]
	if int(binary.BigEndian.Uint32(raw[8:12])) != len(payload) {
		return image, errors.New("image payload length mismatch")
	}
	sum := sha256.Sum256(payload)
	if !bytes.Equal(raw[12:headerSize], sum[:]) {
		return image, errors.New("image checksum mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&image); err != nil {
		return image, fmt.Errorf("decode image: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return image, errors.New("image contains trailing JSON")
	}
	// Images are canonical. Re-encoding also rejects missing required fields,
	// ambiguous duplicate JSON fields, and a null top-level value.
	canonical, err := json.Marshal(image)
	if err != nil || !bytes.Equal(payload, canonical) || image.Layers == nil {
		return image, errors.New("noncanonical image payload")
	}
	return image, nil
}
