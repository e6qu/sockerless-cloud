// Package simkms holds the key-material primitives the three simulators share:
// AES-256-GCM sealing, service-owned keys created on first use, and RFC 5649
// key wrapping.
//
// Each cloud frames its own ciphertext — AWS KMS prefixes the key id, Cloud
// KMS a version number — and keeps that framing in its own module. What it
// hands Seal and gets back from it is the part every framing ends with: the
// GCM nonce followed by the sealed bytes.
package simkms

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KeySize is the length of an AES-256 key.
const KeySize = 32

// NewKey returns fresh AES-256 key material.
func NewKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate key material: %w", err)
	}
	return key, nil
}

// NonceSize and TagSize are AES-GCM's standard nonce and tag lengths.
const (
	NonceSize = 12
	TagSize   = 16
)

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// NewNonce returns a fresh random AES-GCM nonce.
func NewNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, nil
}

// SealWithNonce encrypts plaintext under key with the given nonce, binding
// aad, and returns the ciphertext with its tag appended. Raw-encryption APIs,
// which hand the IV back separately, use it; everything else uses Seal.
func SealWithNonce(key, nonce, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes", NonceSize)
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

// OpenWithNonce decrypts a value SealWithNonce produced.
func OpenWithNonce(key, nonce, sealed, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes", NonceSize)
	}
	return gcm.Open(nil, nonce, sealed, aad)
}

// Seal encrypts plaintext under key with AES-GCM, binding aad, and returns the
// nonce followed by the sealed bytes.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	sealed, err := SealWithNonce(key, nonce, plaintext, aad)
	if err != nil {
		return nil, err
	}
	return append(nonce, sealed...), nil
}

// ErrTruncated reports a sealed value shorter than its nonce.
var ErrTruncated = errors.New("sealed value is truncated")

// Open decrypts a value Seal produced under the same key and aad.
func Open(key, sealed, aad []byte) ([]byte, error) {
	if len(sealed) < NonceSize {
		return nil, ErrTruncated
	}
	return OpenWithNonce(key, sealed[:NonceSize], sealed[NonceSize:], aad)
}

// EnsureKey returns the key material held in store under id, generating and
// storing it on first use. It generates inside the store's Upsert, so two
// first callers seal under one key instead of the second replacing the
// material the first already used. keyOf reads the material out of a row and
// withKey writes it into one, so a cloud keeps its own row shape.
func EnsureKey[T any](store sim.Store[T], id string, keyOf func(T) []byte, withKey func(*T, []byte)) ([]byte, error) {
	var key []byte
	var err error
	store.Upsert(id, func(row *T) {
		if existing := keyOf(*row); len(existing) > 0 {
			key = existing
			return
		}
		if key, err = NewKey(); err == nil {
			withKey(row, key)
		}
	})
	return key, err
}

// kwpPrefix is the RFC 5649 alternative initial value's constant half.
var kwpPrefix = [4]byte{0xa6, 0x59, 0x59, 0xa6}

// WrapWithPadding wraps key under kek with AES Key Wrap with Padding (RFC
// 5649), the scheme Cloud KMS import and AWS KMS RSA_AES_KEY_WRAP use.
func WrapWithPadding(kek, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	if len(key) == 0 || uint64(len(key)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("key length must be between 1 and 2^32-1 bytes")
	}
	n := (len(key) + 7) / 8
	a := make([]byte, 8)
	copy(a[:4], kwpPrefix[:])
	binary.BigEndian.PutUint32(a[4:], uint32(len(key)))
	padded := make([]byte, n*8)
	copy(padded, key)
	if n == 1 {
		out := make([]byte, 16)
		block.Encrypt(out, append(a, padded...))
		return out, nil
	}
	b := make([]byte, 16)
	for j := 0; j < 6; j++ {
		for i := 1; i <= n; i++ {
			copy(b[:8], a)
			copy(b[8:], padded[(i-1)*8:i*8])
			block.Encrypt(b, b)
			binary.BigEndian.PutUint64(a, binary.BigEndian.Uint64(b[:8])^uint64(n*j+i))
			copy(padded[(i-1)*8:i*8], b[8:])
		}
	}
	return append(a, padded...), nil
}

// UnwrapWithPadding reverses WrapWithPadding, checking the integrity value,
// the length indicator and the zero padding.
func UnwrapWithPadding(kek, wrapped []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < 16 || len(wrapped)%8 != 0 {
		return nil, fmt.Errorf("wrapped key length must be a multiple of 8 and at least 16 bytes")
	}
	n := len(wrapped)/8 - 1
	var a, padded []byte
	if n == 1 {
		decrypted := make([]byte, 16)
		block.Decrypt(decrypted, wrapped)
		a, padded = decrypted[:8], decrypted[8:]
	} else {
		a = append([]byte(nil), wrapped[:8]...)
		padded = append([]byte(nil), wrapped[8:]...)
		b := make([]byte, 16)
		for j := 5; j >= 0; j-- {
			for i := n; i >= 1; i-- {
				binary.BigEndian.PutUint64(b[:8], binary.BigEndian.Uint64(a)^uint64(n*j+i))
				copy(b[8:], padded[(i-1)*8:i*8])
				block.Decrypt(b, b)
				copy(a, b[:8])
				copy(padded[(i-1)*8:i*8], b[8:])
			}
		}
	}
	if !bytes.Equal(a[:4], kwpPrefix[:]) {
		return nil, fmt.Errorf("integrity check failed")
	}
	length := int(binary.BigEndian.Uint32(a[4:]))
	if length <= 8*(n-1) || length > 8*n {
		return nil, fmt.Errorf("invalid message length indicator")
	}
	for _, b := range padded[length:] {
		if b != 0 {
			return nil, fmt.Errorf("non-zero RFC 5649 padding")
		}
	}
	return append([]byte(nil), padded[:length]...), nil
}
