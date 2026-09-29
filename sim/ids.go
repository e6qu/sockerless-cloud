package sim

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/google/uuid"
)

// NewUUID returns a random RFC 9562 version 4 UUID in its canonical form.
func NewUUID() string { return uuid.NewString() }

// RandomHex returns n lowercase hexadecimal characters drawn from crypto/rand.
func RandomHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
