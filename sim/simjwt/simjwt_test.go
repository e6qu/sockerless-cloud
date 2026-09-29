package simjwt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

type memoryKeys map[string]string

func (m memoryKeys) Get(id string) (string, bool) { v, ok := m[id]; return v, ok }
func (m memoryKeys) Put(id, pemText string)       { m[id] = pemText }

func mustSigner(t *testing.T, alg string) *Signer {
	t.Helper()
	s, err := LoadOrCreate(memoryKeys{}, "k", alg)
	if err != nil {
		t.Fatalf("LoadOrCreate(%s): %v", alg, err)
	}
	return s
}

func TestSignVerifiesWithAnIndependentVerifier(t *testing.T) {
	for _, alg := range []string{RS256, ES384} {
		t.Run(alg, func(t *testing.T) {
			s := mustSigner(t, alg)
			now := time.Now()
			token, err := s.Sign(map[string]any{
				"iss": "https://issuer.example", "aud": "rp", "sub": "subject",
				"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
			})
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			keys := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{s.Key().Public()}}
			verifier := oidc.NewVerifier("https://issuer.example", keys, &oidc.Config{
				ClientID: "rp", SupportedSigningAlgs: []string{alg},
			})
			idToken, err := verifier.Verify(context.Background(), token)
			if err != nil {
				t.Fatalf("go-oidc rejected the %s token: %v", alg, err)
			}
			if idToken.Subject != "subject" {
				t.Fatalf("subject = %q", idToken.Subject)
			}
		})
	}
}

func TestJWKMatchesTheSigningKey(t *testing.T) {
	for _, alg := range []string{RS256, ES384} {
		t.Run(alg, func(t *testing.T) {
			s := mustSigner(t, alg)
			doc, err := json.Marshal(JWKS(s))
			if err != nil {
				t.Fatal(err)
			}
			var set jose.JSONWebKeySet
			if err := json.Unmarshal(doc, &set); err != nil {
				t.Fatalf("JWKS does not parse as a JSON Web Key Set: %v\n%s", err, doc)
			}
			keys := set.Key(s.KeyID())
			if len(keys) != 1 {
				t.Fatalf("kid %q: %d keys", s.KeyID(), len(keys))
			}
			if keys[0].Algorithm != alg || keys[0].Use != "sig" {
				t.Fatalf("alg/use = %s/%s", keys[0].Algorithm, keys[0].Use)
			}
			type equaler interface{ Equal(crypto.PublicKey) bool }
			if !s.Key().Public().(equaler).Equal(keys[0].Key) {
				t.Fatal("published key is not the signing key")
			}
		})
	}
}

// A key a simulator persisted before the shared signer existed — PKCS#1 PEM
// under its own id — must keep its key id and keep verifying the tokens it
// signed.
func TestLoadOrCreateKeepsAPreviouslyPersistedKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	store := memoryKeys{"signing-key": string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	wantKID := base64.RawURLEncoding.EncodeToString(sum[:16])

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"sockerless-sim-key-1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"before","exp":` +
		jsonNumber(time.Now().Add(time.Hour).Unix()) + `}`))
	digest := sha256.Sum256([]byte(header + "." + payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	legacy := header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(sig)

	s, err := LoadOrCreate(store, "signing-key", RS256)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if s.KeyID() != wantKID {
		t.Fatalf("kid = %q, want %q", s.KeyID(), wantKID)
	}
	var claims map[string]any
	if err := Verify(legacy, &claims, Options{RequireExpiry: true}, s); err != nil {
		t.Fatalf("a token signed before the upgrade no longer verifies: %v", err)
	}
	if claims["sub"] != "before" {
		t.Fatalf("claims = %v", claims)
	}

	again, err := LoadOrCreate(store, "signing-key", RS256)
	if err != nil || again.KeyID() != s.KeyID() {
		t.Fatalf("reload changed the key: %v", err)
	}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestLoadOrCreateRefusesACorruptOrMismatchedKey(t *testing.T) {
	if _, err := LoadOrCreate(memoryKeys{"k": "not pem"}, "k", RS256); err == nil {
		t.Fatal("a corrupt persisted key was accepted")
	}
	store := memoryKeys{}
	if _, err := LoadOrCreate(store, "k", ES384); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(store, "k", RS256); err == nil {
		t.Fatal("an ES384 key was loaded as RS256")
	}
}

func TestVerifyRejects(t *testing.T) {
	rs := mustSigner(t, RS256)
	other := mustSigner(t, RS256)
	now := time.Now()
	sign := func(s *Signer, claims map[string]any) string {
		token, err := s.Sign(claims)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	valid := map[string]any{"iss": "iss", "aud": []string{"a", "b"}, "exp": now.Add(time.Minute).Unix()}
	opts := Options{Issuer: "iss", Audience: "b", RequireExpiry: true}
	var out map[string]any
	if err := Verify(sign(rs, valid), &out, opts, rs); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	unsigned := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{}`)), "",
	}, ".")
	cases := map[string]struct {
		token string
		want  string
	}{
		"not a JWT":       {"a.b", "token is not a JWT"},
		"wrong algorithm": {unsigned, `token algorithm "HS256" is not RS256`},
		"other key":       {sign(other, valid), "token signature is invalid"},
		"issuer":          {sign(rs, map[string]any{"iss": "x", "aud": "b", "exp": now.Add(time.Minute).Unix()}), `token issuer "x" is not recognised`},
		"audience":        {sign(rs, map[string]any{"iss": "iss", "aud": "a", "exp": now.Add(time.Minute).Unix()}), `token audience does not include "b"`},
		"no expiry":       {sign(rs, map[string]any{"iss": "iss", "aud": "b"}), "token has no expiry"},
		"expired":         {sign(rs, map[string]any{"iss": "iss", "aud": "b", "exp": now.Add(-time.Minute).Unix()}), "token has expired"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := Verify(tc.token, &out, opts, rs)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	parts := strings.Split(sign(rs, valid), ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"iss","aud":"b","exp":9999999999,"sub":"root"}`)) + "." + parts[2]
	if err := Verify(tampered, &out, opts, rs); err == nil || err.Error() != "token signature is invalid" {
		t.Fatalf("tampered payload: err = %v", err)
	}
}

func TestVerifyAcceptsAnyPublishedKey(t *testing.T) {
	rs, es := mustSigner(t, RS256), mustSigner(t, ES384)
	token, err := es.Sign(map[string]any{"exp": time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := Verify(token, &out, Options{}, rs, es); err != nil {
		t.Fatalf("ES384 token against an RS256+ES384 set: %v", err)
	}
}

func TestSignPayloadKeepsTheCallersBytes(t *testing.T) {
	s := mustSigner(t, RS256)
	payload := []byte(`{"z":1,  "a":2}`)
	token, err := s.SignPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil || string(got) != string(payload) {
		t.Fatalf("payload = %q, %v", got, err)
	}
	var out map[string]any
	if err := Verify(token, &out, Options{}, s); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryNamesEachAlgorithmOnce(t *testing.T) {
	doc := Discovery("https://i", "https://i/jwks", []string{"id_token"},
		mustSigner(t, RS256), mustSigner(t, RS256), mustSigner(t, ES384))
	algs := doc["id_token_signing_alg_values_supported"].([]string)
	if strings.Join(algs, ",") != "RS256,ES384" {
		t.Fatalf("algs = %v", algs)
	}
	if doc["issuer"] != "https://i" || doc["jwks_uri"] != "https://i/jwks" {
		t.Fatalf("doc = %v", doc)
	}
}
