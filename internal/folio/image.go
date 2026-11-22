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
	"strconv"

	"github.com/blankbulldoz/solo-0041-meridian-patch/internal/catalog"
)

const MaxImage = 16 << 20
const headerSize = 8 + 4 + sha256.Size

// Version selects the on-disk and portable image layout. Version 1 is the
// original single-payload JSON format. Version 2 splits the payload into
// independently checksummed layer segments.
type Version uint8

const (
	Version1 Version = 1
	Version2 Version = 2

	// CurrentVersion is written for durable state and is the default export.
	CurrentVersion = Version2
)

var signaturePrefix = [7]byte{'M', 'R', 'D', 'N', 'M', 'P', 'B'}

func (v Version) Supported() bool { return v == Version1 || v == Version2 }

func (v Version) String() string {
	switch v {
	case Version1, Version2:
		return strconv.Itoa(int(v))
	default:
		return ""
	}
}

func ParseVersion(text string) (Version, error) {
	switch text {
	case "1":
		return Version1, nil
	case "2":
		return Version2, nil
	default:
		return 0, fmt.Errorf("unsupported image version %q", text)
	}
}

// Image is a decoded portable image together with the format that carried it.
type Image struct {
	Version Version
	Catalog catalog.CatalogImage
}

// ImageError describes a rejected image. Segment is 1-based for version 2 and
// Layer is the declared segment name, which lets a caller identify the damaged
// layer even though installation is rejected as a whole.
type ImageError struct {
	Version Version
	Segment int
	Layer   string
	Reason  string
}

func (e *ImageError) Error() string {
	switch {
	case e.Segment > 0 && e.Layer != "":
		return fmt.Sprintf("MPB v%d segment %d layer %q: %s", e.Version, e.Segment, e.Layer, e.Reason)
	case e.Segment > 0:
		return fmt.Sprintf("MPB v%d segment %d: %s", e.Version, e.Segment, e.Reason)
	case e.Version != 0:
		return fmt.Sprintf("MPB v%d: %s", e.Version, e.Reason)
	default:
		return e.Reason
	}
}

func imageError(version Version, segment int, layer, reason string) error {
	return &ImageError{Version: version, Segment: segment, Layer: layer, Reason: reason}
}

// Encode serializes image in the requested MPB version.
func Encode(image catalog.CatalogImage, version Version) ([]byte, error) {
	var payload []byte
	var err error
	switch version {
	case Version1:
		payload, err = encodeV1(image)
	case Version2:
		payload, err = encodeV2(image)
	default:
		return nil, fmt.Errorf("cannot encode unsupported image version %d", version)
	}
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxImage-headerSize {
		return nil, errors.New("image exceeds maximum size")
	}
	raw := make([]byte, headerSize+len(payload))
	copy(raw, signaturePrefix[:])
	raw[7] = byte(version)
	binary.BigEndian.PutUint32(raw[8:12], uint32(len(payload)))
	sum := sha256.Sum256(payload)
	copy(raw[12:headerSize], sum[:])
	copy(raw[headerSize:], payload)
	return raw, nil
}

func encodeV1(image catalog.CatalogImage) ([]byte, error) {
	return json.Marshal(image)
}

// encodeV2 writes: revision(8) count(4), then for every layer in the supplied
// (name-sorted) order: name length(4), body length(4), segment digest(32),
// name, and canonical LayerImage JSON. The segment digest covers name, a zero
// separator and the body. The image header digest covers this whole payload.
func encodeV2(image catalog.CatalogImage) ([]byte, error) {
	if len(image.Layers) > catalog.MaxLayers {
		return nil, errors.New("image exceeds layer capacity")
	}
	payload := make([]byte, 12, 12+len(image.Layers)*(40+48))
	binary.BigEndian.PutUint64(payload[0:8], image.Revision)
	binary.BigEndian.PutUint32(payload[8:12], uint32(len(image.Layers)))
	var previous string
	for _, layer := range image.Layers {
		name := layer.Definition.Name
		if !catalog.ValidName(name) || name <= previous {
			return nil, fmt.Errorf("layer %q is not in canonical order", name)
		}
		previous = name
		body, err := json.Marshal(layer)
		if err != nil {
			return nil, err
		}
		digest := segmentDigest(name, body)
		var head [8]byte
		binary.BigEndian.PutUint32(head[0:4], uint32(len(name)))
		binary.BigEndian.PutUint32(head[4:8], uint32(len(body)))
		payload = append(payload, head[:]...)
		payload = append(payload, digest[:]...)
		payload = append(payload, name...)
		payload = append(payload, body...)
	}
	return payload, nil
}

func segmentDigest(name string, body []byte) [sha256.Size]byte {
	sum := sha256.New()
	io.WriteString(sum, name)
	sum.Write([]byte{0})
	sum.Write(body)
	var digest [sha256.Size]byte
	copy(digest[:], sum.Sum(nil))
	return digest
}

// Decode accepts every supported MPB version. Version 2 validates each layer
// segment independently and reports the damaged layer; either way a rejection
// returns no catalog that a caller could install.
func Decode(raw []byte) (Image, error) {
	if len(raw) < headerSize {
		return Image{}, imageError(0, 0, "", "invalid image size")
	}
	if !bytes.Equal(raw[:7], signaturePrefix[:]) {
		return Image{}, imageError(0, 0, "", "invalid image signature")
	}
	version := Version(raw[7])
	if !version.Supported() {
		return Image{}, imageError(0, 0, "", fmt.Sprintf("unsupported image version %d", raw[7]))
	}
	if len(raw) > MaxImage {
		return Image{}, imageError(version, 0, "", "image exceeds maximum size")
	}
	payload := raw[headerSize:]
	if int(binary.BigEndian.Uint32(raw[8:12])) != len(payload) {
		return Image{}, imageError(version, 0, "", "image payload length mismatch")
	}
	var image catalog.CatalogImage
	switch version {
	case Version1:
		sum := sha256.Sum256(payload)
		if !bytes.Equal(raw[12:headerSize], sum[:]) {
			return Image{}, imageError(version, 0, "", "image checksum mismatch")
		}
		decoded, err := decodeV1(payload)
		if err != nil {
			return Image{}, imageError(version, 0, "", err.Error())
		}
		image = decoded
	case Version2:
		decoded, err := decodeV2(payload)
		if err != nil {
			return Image{}, err
		}
		// Segment digests cannot observe tampering with the revision, count or
		// segment framing, so the container digest remains authoritative.
		sum := sha256.Sum256(payload)
		if !bytes.Equal(raw[12:headerSize], sum[:]) {
			return Image{}, imageError(version, 0, "", "image checksum mismatch")
		}
		image = decoded
	}
	return Image{Version: version, Catalog: image}, nil
}

func decodeV1(payload []byte) (catalog.CatalogImage, error) {
	var image catalog.CatalogImage
	if err := decodeCanonical(payload, &image); err != nil {
		return catalog.CatalogImage{}, err
	}
	if image.Layers == nil {
		return catalog.CatalogImage{}, errors.New("noncanonical image payload")
	}
	return image, nil
}

func decodeV2(payload []byte) (catalog.CatalogImage, error) {
	image := catalog.CatalogImage{Layers: make([]catalog.LayerImage, 0)}
	if len(payload) < 12 {
		return image, imageError(Version2, 0, "", "truncated image payload")
	}
	image.Revision = binary.BigEndian.Uint64(payload[0:8])
	count := int(binary.BigEndian.Uint32(payload[8:12]))
	if count > catalog.MaxLayers {
		return image, imageError(Version2, 0, "", fmt.Sprintf("layer count %d exceeds capacity", count))
	}
	var previous string
	offset := 12
	for index := 0; index < count; index++ {
		segment := index + 1
		if len(payload)-offset < 40 {
			return image, imageError(Version2, segment, "", "truncated segment header")
		}
		nameLen := int(binary.BigEndian.Uint32(payload[offset : offset+4]))
		bodyLen := int(binary.BigEndian.Uint32(payload[offset+4 : offset+8]))
		var digest [sha256.Size]byte
		copy(digest[:], payload[offset+8:offset+40])
		if nameLen < 1 || nameLen > 48 || bodyLen < 1 || bodyLen > MaxImage {
			return image, imageError(Version2, segment, "", "invalid segment lengths")
		}
		start := offset + 40
		if nameLen > len(payload)-start {
			return image, imageError(Version2, segment, "", "truncated segment name")
		}
		if bodyLen > len(payload)-start-nameLen {
			return image, imageError(Version2, segment, "", "truncated segment body")
		}
		name := string(payload[start : start+nameLen])
		body := payload[start+nameLen : start+nameLen+bodyLen]
		offset = start + nameLen + bodyLen
		// Check the digest before trusting the declared name or parsing JSON.
		if !bytes.Equal(digest[:], segmentDigestBytes(name, body)) {
			return image, imageError(Version2, segment, name, "layer checksum mismatch")
		}
		if !catalog.ValidName(name) {
			return image, imageError(Version2, segment, name, "invalid layer name")
		}
		if name <= previous {
			return image, imageError(Version2, segment, name, "layers are not in canonical order")
		}
		previous = name
		var layer catalog.LayerImage
		if err := decodeCanonical(body, &layer); err != nil {
			return image, imageError(Version2, segment, name, err.Error())
		}
		if layer.Definition.Name != name {
			return image, imageError(Version2, segment, name, "segment name does not match layer definition")
		}
		image.Layers = append(image.Layers, layer)
	}
	if offset != len(payload) {
		return image, imageError(Version2, 0, "", "image contains trailing segment bytes")
	}
	if image.Revision == 0 && len(image.Layers) > 0 {
		return image, imageError(Version2, 0, "", "nonempty image has revision zero")
	}
	return image, nil
}

func segmentDigestBytes(name string, body []byte) []byte {
	digest := segmentDigest(name, body)
	return digest[:]
}

func decodeCanonical(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("image contains trailing JSON")
	}
	// Images are canonical. Re-encoding also rejects missing required fields,
	// ambiguous duplicate JSON fields, and a null top-level value.
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(payload, canonical) {
		return errors.New("noncanonical image payload")
	}
	return nil
}
