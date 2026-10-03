package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strings"
)

// The Azure Functions host's key material, in the formats the host itself
// reads and writes: identifiable secrets for key values, and ASP.NET Core Data
// Protection payloads for the encrypted values of its file secret store.

// The seeds the host passes to the Marvin checksum of an identifiable secret,
// one per key family.
const (
	functionsMasterKeySeed   uint64 = 0x4d61737465723030
	functionsSystemKeySeed   uint64 = 0x53797374656d3030
	functionsFunctionKeySeed uint64 = 0x46756e6374693030
)

// functionsKeySignature is the identifiable-secret signature the host embeds
// in every key it generates.
const functionsKeySignature = "AzFu"

// newFunctionsKey generates a key value the way the Functions host does: 33
// random bytes, the "AzFu" signature, and the Marvin checksum of both under the
// key family's seed, URL-safe base64 with its padding kept.
func newFunctionsKey(seed uint64) string {
	sig, err := base64.StdEncoding.DecodeString(functionsKeySignature)
	if err != nil {
		panic(err)
	}
	key := make([]byte, 33, 40)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	key = append(key, sig...)
	key = binary.LittleEndian.AppendUint32(key, marvin32(key, seed))
	return base64.URLEncoding.EncodeToString(key)
}

// validFunctionsKey reports whether value is an identifiable secret of the key
// family seed names.
func validFunctionsKey(value string, seed uint64) bool {
	key, err := base64.URLEncoding.DecodeString(value)
	if err != nil || len(key) != 40 || !strings.HasSuffix(value[:48], functionsKeySignature) {
		return false
	}
	return binary.LittleEndian.Uint32(key[36:]) == marvin32(key[:36], seed)
}

// marvin32 is the Marvin checksum .NET computes, folded to 32 bits.
func marvin32(data []byte, seed uint64) uint32 {
	p0, p1 := uint32(seed), uint32(seed>>32)
	block := func() {
		p1 ^= p0
		p0 = bits.RotateLeft32(p0, 20)
		p0 += p1
		p1 = bits.RotateLeft32(p1, 9)
		p1 ^= p0
		p0 = bits.RotateLeft32(p0, 27)
		p0 += p1
		p1 = bits.RotateLeft32(p1, 19)
	}
	for len(data) >= 4 {
		p0 += binary.LittleEndian.Uint32(data)
		block()
		data = data[4:]
	}
	final := uint32(0x80)
	switch len(data) {
	case 1:
		final = 0x8000 | uint32(data[0])
	case 2:
		final = 0x800000 | uint32(data[0]) | uint32(data[1])<<8
	case 3:
		final = 0x80000000 | uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	}
	p0 += final
	block()
	block()
	return p0 ^ p1
}

// newSiteEncryptionKey generates the per-site key App Service hands the site
// as WEBSITE_AUTH_ENCRYPTION_KEY: 256 bits, hex-encoded.
func newSiteEncryptionKey() string {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return strings.ToUpper(hex.EncodeToString(key))
}

// functionsKeyBytes reads a 256-bit encryption key the way the host does:
// hex when it is 64 characters long, else base64.
func functionsKeyBytes(key string) ([]byte, error) {
	var (
		b   []byte
		err error
	)
	if len(key) == 64 {
		b, err = hex.DecodeString(key)
	} else {
		b, err = base64.StdEncoding.DecodeString(key)
	}
	if err != nil {
		return nil, fmt.Errorf("encryption key: %w", err)
	}
	return b, nil
}

// functionSecretsPurpose is the Data Protection purpose the host protects its
// stored key values under.
const functionSecretsPurpose = "function-secrets"

// dataProtectionMagic opens every ASP.NET Core Data Protection payload.
var dataProtectionMagic = []byte{0x09, 0xF0, 0xC9, 0xF0}

// protectFunctionSecret encrypts a key value as the host does when it writes
// its file secret store on App Service: a Data Protection payload under the
// site's encryption key (whose key id the host leaves as the empty GUID),
// AES-256-CBC with an HMAC-SHA256 tag, base64url-encoded.
func protectFunctionSecret(encryptionKey, plaintext string) (string, error) {
	master, err := functionsKeyBytes(encryptionKey)
	if err != nil {
		return "", err
	}
	header := append(append([]byte{}, dataProtectionMagic...), make([]byte, 16)...)
	modifierAndIV := make([]byte, 32)
	if _, err := rand.Read(modifierAndIV); err != nil {
		return "", err
	}
	encKey, macKey := dataProtectionSubkeys(master, header, modifierAndIV[:16])
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append([]byte(plaintext), bytes.Repeat([]byte{byte(pad)}, pad)...)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, modifierAndIV[16:]).CryptBlocks(ciphertext, padded)
	mac := hmac.New(sha256.New, macKey)
	mac.Write(modifierAndIV[16:])
	mac.Write(ciphertext)
	out := append(append(append(header, modifierAndIV...), ciphertext...), mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// unprotectFunctionSecret decrypts a value protectFunctionSecret, or the host,
// wrote under the site's encryption key.
func unprotectFunctionSecret(encryptionKey, protected string) (string, error) {
	master, err := functionsKeyBytes(encryptionKey)
	if err != nil {
		return "", err
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(protected, "="))
	if err != nil {
		return "", fmt.Errorf("protected key value: %w", err)
	}
	const headerLen, modifierLen, macLen = 20, 16, sha256.Size
	if len(data) < headerLen+modifierLen+aes.BlockSize+aes.BlockSize+macLen || !bytes.Equal(data[:4], dataProtectionMagic) {
		return "", errors.New("protected key value is not a Data Protection payload")
	}
	header, body := data[:headerLen], data[headerLen:]
	modifier, iv := body[:modifierLen], body[modifierLen:modifierLen+aes.BlockSize]
	ciphertext := body[modifierLen+aes.BlockSize : len(body)-macLen]
	tag := body[len(body)-macLen:]
	encKey, macKey := dataProtectionSubkeys(master, header, modifier)
	mac := hmac.New(sha256.New, macKey)
	mac.Write(iv)
	mac.Write(ciphertext)
	if !hmac.Equal(mac.Sum(nil), tag) {
		return "", errors.New("protected key value was not protected with this site's encryption key")
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("protected key value has a truncated ciphertext")
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return "", errors.New("protected key value has invalid padding")
	}
	return string(plain[:len(plain)-pad]), nil
}

// dataProtectionSubkeys derives a payload's AES and HMAC keys: SP800-108 in
// counter mode over HMAC-SHA512, labelled with the payload's additional
// authenticated data (its header and purpose chain) and keyed to the
// algorithm's context header and the payload's key modifier.
func dataProtectionSubkeys(master, header, modifier []byte) (encKey, macKey []byte) {
	aad := append(append([]byte{}, header...), 0, 0, 0, 1, byte(len(functionSecretsPurpose)))
	aad = append(aad, functionSecretsPurpose...)
	keys := sp800108CounterHMACSHA512(master, aad, append(append([]byte{}, cbcHMACContextHeader()...), modifier...), 64)
	return keys[:32], keys[32:]
}

// cbcHMACContextHeader identifies AES-256-CBC with HMAC-SHA256 to the key
// derivation: the key and block sizes, then the encryption of an empty message
// and the HMAC of an empty message under subkeys derived from nothing.
func cbcHMACContextHeader() []byte {
	h := []byte{0, 0}
	for _, n := range []uint32{32, aes.BlockSize, 32, sha256.Size} {
		h = binary.BigEndian.AppendUint32(h, n)
	}
	keys := sp800108CounterHMACSHA512(nil, nil, nil, 64)
	block, err := aes.NewCipher(keys[:32])
	if err != nil {
		panic(err)
	}
	empty := bytes.Repeat([]byte{aes.BlockSize}, aes.BlockSize)
	out := make([]byte, aes.BlockSize)
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, empty)
	mac := hmac.New(sha256.New, keys[32:])
	return append(append(h, out...), mac.Sum(nil)...)
}

func sp800108CounterHMACSHA512(key, label, context []byte, n int) []byte {
	var out []byte
	for i := uint32(1); len(out) < n; i++ {
		mac := hmac.New(sha512.New, key)
		mac.Write(binary.BigEndian.AppendUint32(nil, i))
		mac.Write(label)
		mac.Write([]byte{0})
		mac.Write(context)
		mac.Write(binary.BigEndian.AppendUint32(nil, uint32(n*8)))
		out = mac.Sum(out)
	}
	return out[:n]
}
