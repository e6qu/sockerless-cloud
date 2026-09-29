// Package simjwt signs and verifies the JSON Web Tokens a simulator issues as
// its own identity provider, persists the signing key, and publishes it as a
// JSON Web Key Set with an OpenID Connect discovery document. Claim shapes,
// issuers and audiences belong to each cloud.
package simjwt

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// JWS algorithms a Signer signs with.
const (
	RS256 = "RS256"
	ES384 = "ES384"
)

const (
	pemRSAPrivateKey = "RSA PRIVATE KEY"
	pemECPrivateKey  = "EC PRIVATE KEY"
	es384Size        = 48
)

// Signer is one signing key with the algorithm and key id it signs under.
type Signer struct {
	key   crypto.Signer
	alg   string
	keyID string
}

// KeyStore persists PEM-encoded private keys by id. sim.Store[string]
// satisfies it.
type KeyStore interface {
	Get(id string) (string, bool)
	Put(id string, pemText string)
}

// NewSigner wraps a private key. The algorithm follows the key: RSA signs
// RS256 and P-384 ECDSA signs ES384. An empty keyID takes Thumbprint of the
// public key.
func NewSigner(key crypto.Signer, keyID string) (*Signer, error) {
	alg, err := algorithmFor(key)
	if err != nil {
		return nil, err
	}
	if keyID == "" {
		keyID, err = Thumbprint(key.Public())
		if err != nil {
			return nil, err
		}
	}
	return &Signer{key: key, alg: alg, keyID: keyID}, nil
}

func algorithmFor(key crypto.Signer) (string, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return RS256, nil
	case *ecdsa.PrivateKey:
		if k.Curve == elliptic.P384() {
			return ES384, nil
		}
		return "", fmt.Errorf("ECDSA curve %s has no supported JWS algorithm", k.Curve.Params().Name)
	default:
		return "", fmt.Errorf("private key type %T has no supported JWS algorithm", key)
	}
}

// GenerateKey creates a fresh private key for alg.
func GenerateKey(alg string) (crypto.Signer, error) {
	switch alg {
	case RS256:
		return rsa.GenerateKey(rand.Reader, 2048)
	case ES384:
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	default:
		return nil, fmt.Errorf("JWS algorithm %q is not supported", alg)
	}
}

// EncodePrivateKeyPEM renders an RSA key as PKCS#1 `RSA PRIVATE KEY` and an
// ECDSA key as SEC 1 `EC PRIVATE KEY`.
func EncodePrivateKeyPEM(key crypto.Signer) (string, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return string(pem.EncodeToMemory(&pem.Block{Type: pemRSAPrivateKey, Bytes: x509.MarshalPKCS1PrivateKey(k)})), nil
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return "", fmt.Errorf("marshal ECDSA private key: %w", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: pemECPrivateKey, Bytes: der})), nil
	default:
		return "", fmt.Errorf("private key type %T cannot be encoded", key)
	}
}

// ParsePrivateKeyPEM reads a key EncodePrivateKeyPEM wrote.
func ParsePrivateKeyPEM(pemText string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("private key is not PEM")
	}
	switch block.Type {
	case pemRSAPrivateKey:
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case pemECPrivateKey:
		return x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("PEM block %q is not an RSA or EC private key", block.Type)
	}
}

// LoadOrCreate returns the signer whose key store holds under id, generating
// and persisting an alg key on first use. A persisted key that no longer parses
// fails rather than being replaced: regenerating would silently invalidate
// every token already issued.
func LoadOrCreate(store KeyStore, id, alg string) (*Signer, error) {
	if pemText, ok := store.Get(id); ok {
		key, err := ParsePrivateKeyPEM(pemText)
		if err != nil {
			return nil, fmt.Errorf("persisted signing key %q: %w", id, err)
		}
		signer, err := NewSigner(key, "")
		if err != nil {
			return nil, err
		}
		if signer.alg != alg {
			return nil, fmt.Errorf("persisted signing key %q signs %s, not %s", id, signer.alg, alg)
		}
		return signer, nil
	}
	key, err := GenerateKey(alg)
	if err != nil {
		return nil, fmt.Errorf("generate signing key %q: %w", id, err)
	}
	pemText, err := EncodePrivateKeyPEM(key)
	if err != nil {
		return nil, err
	}
	store.Put(id, pemText)
	return NewSigner(key, "")
}

// Thumbprint derives a stable key id from the public key's PKIX DER encoding.
func Thumbprint(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:16]), nil
}

func (s *Signer) Alg() string { return s.alg }

func (s *Signer) KeyID() string { return s.keyID }

// Key is the private key, for a caller that signs something other than a JWT
// with the same key the JWKS publishes.
func (s *Signer) Key() crypto.Signer { return s.key }

// Sign encodes claims as the JWT payload and signs it.
func (s *Signer) Sign(claims any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode JWT claims: %w", err)
	}
	return s.SignPayload(payload)
}

// SignPayload signs payload verbatim as the JWS payload, for a caller handed a
// claim set it must not re-encode.
func (s *Signer) SignPayload(payload []byte) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": s.alg, "typ": "JWT", "kid": s.keyID})
	if err != nil {
		return "", fmt.Errorf("encode JWT header: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sig, err := s.signBytes([]byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *Signer) signBytes(data []byte) ([]byte, error) {
	switch k := s.key.(type) {
	case *rsa.PrivateKey:
		digest := sha256.Sum256(data)
		sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
		if err != nil {
			return nil, fmt.Errorf("sign JWT: %w", err)
		}
		return sig, nil
	case *ecdsa.PrivateKey:
		digest := sha512.Sum384(data)
		r, sv, err := ecdsa.Sign(rand.Reader, k, digest[:])
		if err != nil {
			return nil, fmt.Errorf("sign JWT: %w", err)
		}
		// JWS carries an ECDSA signature as the fixed-width R || S pair
		// (RFC 7518 §3.4), not the ASN.1 form crypto/ecdsa produces.
		sig := make([]byte, 2*es384Size)
		r.FillBytes(sig[:es384Size])
		sv.FillBytes(sig[es384Size:])
		return sig, nil
	}
	return nil, fmt.Errorf("private key type %T cannot sign", s.key)
}

// JWK renders the public key as a JSON Web Key (RFC 7517).
func (s *Signer) JWK() map[string]any {
	jwk := map[string]any{"kid": s.keyID, "alg": s.alg, "use": "sig"}
	switch pub := s.key.Public().(type) {
	case *rsa.PublicKey:
		jwk["kty"] = "RSA"
		jwk["n"] = base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
		jwk["e"] = base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	case *ecdsa.PublicKey:
		ecdhKey, err := pub.ECDH()
		if err != nil {
			panic(fmt.Sprintf("ECDSA public key has no uncompressed point: %v", err))
		}
		point := ecdhKey.Bytes()
		jwk["kty"] = "EC"
		jwk["crv"] = pub.Curve.Params().Name
		jwk["x"] = base64.RawURLEncoding.EncodeToString(point[1 : 1+es384Size])
		jwk["y"] = base64.RawURLEncoding.EncodeToString(point[1+es384Size:])
	}
	return jwk
}

// JWKS renders the signers' public keys as a JSON Web Key Set document.
func JWKS(signers ...*Signer) map[string]any {
	keys := make([]map[string]any, 0, len(signers))
	for _, s := range signers {
		keys = append(keys, s.JWK())
	}
	return map[string]any{"keys": keys}
}

// Discovery renders the OpenID Connect discovery document (OpenID Connect
// Discovery 1.0 §3) for an issuer whose tokens the signers sign. A cloud adds
// its own endpoints to the returned document.
func Discovery(issuer, jwksURI string, responseTypes []string, signers ...*Signer) map[string]any {
	var algs []string
	seen := map[string]bool{}
	for _, s := range signers {
		if !seen[s.alg] {
			seen[s.alg] = true
			algs = append(algs, s.alg)
		}
	}
	return map[string]any{
		"issuer":                                issuer,
		"jwks_uri":                              jwksURI,
		"response_types_supported":              responseTypes,
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": algs,
	}
}

// Options are the registered-claim checks Verify applies beyond the
// signature. An empty Issuer or Audience is not checked.
type Options struct {
	Issuer        string
	Audience      string
	RequireExpiry bool
}

// Verify checks a compact JWS against the signers' keys and the registered
// claims opts names, then decodes the payload into claims.
func Verify(raw string, claims any, opts Options, signers ...*Signer) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return errors.New("token is not a JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("token header is not base64url: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return fmt.Errorf("token header is not JSON: %w", err)
	}
	var candidates []*Signer
	var algs []string
	for _, s := range signers {
		algs = append(algs, s.alg)
		if s.alg == header.Alg {
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		return fmt.Errorf("token algorithm %q is not %s", header.Alg, strings.Join(algs, " or "))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("token signature is not base64url: %w", err)
	}
	signingInput := []byte(parts[0] + "." + parts[1])
	verified := false
	for _, s := range candidates {
		if verifySignature(s.key.Public(), signingInput, sig) {
			verified = true
			break
		}
	}
	if !verified {
		return errors.New("token signature is invalid")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("token claims are not base64url: %w", err)
	}
	var registered struct {
		Iss string `json:"iss"`
		Aud any    `json:"aud"`
		Exp any    `json:"exp"`
	}
	if err := json.Unmarshal(payload, &registered); err != nil {
		return fmt.Errorf("token claims are not JSON: %w", err)
	}
	if err := json.Unmarshal(payload, claims); err != nil {
		return fmt.Errorf("token claims are not JSON: %w", err)
	}
	if opts.Issuer != "" && registered.Iss != opts.Issuer {
		return fmt.Errorf("token issuer %q is not recognised", registered.Iss)
	}
	if opts.Audience != "" && !audienceContains(registered.Aud, opts.Audience) {
		return fmt.Errorf("token audience does not include %q", opts.Audience)
	}
	exp, hasExp := registered.Exp.(float64)
	if registered.Exp != nil && !hasExp {
		return errors.New("token expiry is not a number")
	}
	if !hasExp || exp == 0 {
		if opts.RequireExpiry {
			return errors.New("token has no expiry")
		}
		return nil
	}
	if time.Now().After(time.Unix(int64(exp), 0)) {
		return errors.New("token has expired")
	}
	return nil
}

func verifySignature(pub crypto.PublicKey, signingInput, sig []byte) bool {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		digest := sha256.Sum256(signingInput)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) == nil
	case *ecdsa.PublicKey:
		if len(sig) != 2*es384Size {
			return false
		}
		digest := sha512.Sum384(signingInput)
		r := new(big.Int).SetBytes(sig[:es384Size])
		s := new(big.Int).SetBytes(sig[es384Size:])
		return ecdsa.Verify(k, digest[:], r, s)
	}
	return false
}

// audienceContains reports whether a JWT `aud` claim — a string or an array of
// strings (RFC 7519 §4.1.3) — includes want.
func audienceContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
