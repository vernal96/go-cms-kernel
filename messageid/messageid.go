package messageid

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// ID is a stable globally unique identity shared by durable messages.
type ID string

// New creates a random 128-bit identifier using only the standard library.
func New() (ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	// Use the RFC 4122 version/variant layout without depending on UUID APIs.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return ID(encoded), nil
}

func (id ID) Validate() error {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return errors.New("message ID is invalid")
	}
	compact := make([]byte, 0, 32)
	for index := range id {
		if id[index] != '-' {
			compact = append(compact, id[index])
		}
	}
	decoded := make([]byte, 16)
	if _, err := hex.Decode(decoded, compact); err != nil {
		return errors.New("message ID is invalid")
	}
	return nil
}
