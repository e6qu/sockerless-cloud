package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMintAzureSimJWT_StructureAndClaims(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, err := mintAzureSimJWT("tenant-xyz", "https://vault.azure.net", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q: want 3 segments, got %d", tok, len(parts))
	}
	if parts[2] == "" {
		t.Errorf("signature segment is empty (alg:none leak?)")
	}

	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header decode: %v", err)
	}
	var h map[string]string
	if err := json.Unmarshal(header, &h); err != nil {
		t.Fatalf("header unmarshal: %v", err)
	}
	if h["alg"] != "RS256" {
		t.Errorf("alg = %q, want RS256 — alg:none is auth-bypass", h["alg"])
	}
	if h["kid"] == "" {
		t.Errorf("kid missing — clients that fetch JWKS rely on it")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p["tid"] != "tenant-xyz" {
		t.Errorf("tid = %v, want tenant-xyz", p["tid"])
	}
	if p["aud"] != "https://vault.azure.net" {
		t.Errorf("aud = %v, want vault.azure.net", p["aud"])
	}
	if p["iss"] != "https://sts.windows.net/tenant-xyz/" {
		t.Errorf("iss = %v, want sts.windows.net/tenant-xyz/", p["iss"])
	}
	if iat, ok := p["iat"].(float64); !ok || int64(iat) != now.Unix() {
		t.Errorf("iat = %v, want %d", p["iat"], now.Unix())
	}
}

func TestMintAzureSimJWT_SignatureVerifies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tok, err := mintAzureSimJWT("tenant-xyz", defaultAzureTokenAudience, now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 segments, got %d", len(parts))
	}
	signingInput := parts[0] + "." + parts[1]

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature decode: %v", err)
	}
	key, err := azureSimSigningKey()
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("signature verify: %v", err)
	}
}

func TestAzureTokenAudienceFromForm(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{
			name: "default management audience",
			form: url.Values{},
			want: "https://management.azure.com/",
		},
		{
			name: "v2 vault scope strips default suffix",
			form: url.Values{"scope": {"https://vault.azure.net/.default"}},
			want: "https://vault.azure.net",
		},
		{
			name: "v2 bare vault scope is accepted",
			form: url.Values{"scope": {"https://vault.azure.net"}},
			want: "https://vault.azure.net",
		},
		{
			name: "v2 management scope preserves Azure trailing slash audience",
			form: url.Values{"scope": {"https://management.azure.com/.default"}},
			want: "https://management.azure.com/",
		},
		{
			name: "v2 storage scope preserves Azure trailing slash audience",
			form: url.Values{"scope": {"https://storage.azure.com/.default"}},
			want: "https://storage.azure.com/",
		},
		{
			name: "oidc-only scopes use default management audience",
			form: url.Values{"scope": {"openid profile email"}},
			want: "https://management.azure.com/",
		},
		{
			name: "oidc scopes are skipped before resource scope",
			form: url.Values{"scope": {"openid profile email https://graph.microsoft.com/User.Read"}},
			want: "https://graph.microsoft.com",
		},
		{
			name: "v1 resource is audience",
			form: url.Values{"resource": {"https://servicebus.azure.net"}},
			want: "https://servicebus.azure.net",
		},
		{
			name: "scope takes precedence when both fields are sent",
			form: url.Values{
				"scope":    {"https://vault.azure.net/.default"},
				"resource": {"https://management.azure.com/"},
			},
			want: "https://vault.azure.net",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := azureTokenAudienceFromForm(tt.form); got != tt.want {
				t.Fatalf("audience = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAzureSimJWK_PublishesRS256PublicKey(t *testing.T) {
	jwk, err := azureSimJWK()
	if err != nil {
		t.Fatalf("jwk: %v", err)
	}
	if jwk["kty"] != "RSA" {
		t.Errorf("kty = %v, want RSA", jwk["kty"])
	}
	if jwk["alg"] != "RS256" {
		t.Errorf("alg = %v, want RS256", jwk["alg"])
	}
	if jwk["kid"] != "sockerless-sim-key-1" {
		t.Errorf("kid = %v, want sockerless-sim-key-1", jwk["kid"])
	}
	if jwk["n"] == "" || jwk["e"] == "" {
		t.Errorf("n/e missing from RSA JWK: %#v", jwk)
	}
}
