package main

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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"

	"github.com/e6qu/sockerless-cloud/sim"
)

// selfSignedJWT signs claims with the bootstrap key the way Google's client
// libraries sign a self-signed service-account JWT, under header kid.
func selfSignedJWT(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()
	block, _ := pem.Decode([]byte(gcpBootstrapPrivateKeyPEM))
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	enc := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := enc(header) + "." + enc(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, parsed.(*rsa.PrivateKey), crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func bootstrapSelfSignedClaims(now time.Time, extra map[string]any) map[string]any {
	claims := map[string]any{
		"iss": gcpBootstrapServiceAccountEmail,
		"sub": gcpBootstrapServiceAccountEmail,
		"aud": "https://secretmanager.googleapis.com/",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	return claims
}

func selfSignedTestServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, gcpTestProjectID)
	srv.WrapHandler(bearerAuthMiddleware(srv))
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts.URL
}

func listSecretsStatus(t *testing.T, base, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/v1/projects/"+gcpTestProjectID+"/secrets", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestRESTAcceptsASelfSignedServiceAccountJWT(t *testing.T) {
	base := selfSignedTestServer(t)
	now := time.Now()
	for name, extra := range map[string]map[string]any{
		"audience":              nil,
		"scope in place of aud": {"aud": nil, "scope": "https://www.googleapis.com/auth/cloud-platform"},
		"scope, empty aud":      {"aud": "", "scope": "https://www.googleapis.com/auth/cloud-platform"},
	} {
		t.Run(name, func(t *testing.T) {
			token := selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, extra))
			if got := listSecretsStatus(t, base, token); got != http.StatusOK {
				t.Fatalf("secrets.list with a self-signed JWT: %d", got)
			}
		})
	}
}

func TestRESTRefusesAnUnacceptableSelfSignedJWT(t *testing.T) {
	base := selfSignedTestServer(t)
	now := time.Now()
	cases := map[string]string{
		"no kid":                selfSignedJWT(t, "", bootstrapSelfSignedClaims(now, nil)),
		"unregistered kid":      selfSignedJWT(t, "0000000000000000000000000000000000000000", bootstrapSelfSignedClaims(now, nil)),
		"foreign audience":      selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"aud": "https://example.com/"})),
		"neither aud nor scope": selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"aud": nil})),
		"empty aud, no scope":   selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"aud": ""})),
		"expired":               selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix()})),
		"longer than an hour":   selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"exp": now.Add(2 * time.Hour).Unix()})),
		"issued in the future":  selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"iat": now.Add(time.Hour).Unix(), "exp": now.Add(90 * time.Minute).Unix()})),
		"unknown account":       selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(now, map[string]any{"iss": "nobody@sockerless.iam.gserviceaccount.com", "sub": "nobody@sockerless.iam.gserviceaccount.com"})),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if got := listSecretsStatus(t, base, token); got != http.StatusUnauthorized {
				t.Fatalf("secrets.list with a %s self-signed JWT: %d, want 401", name, got)
			}
		})
	}
}

func TestRESTRefusesASelfSignedJWTUnderADisabledKey(t *testing.T) {
	base := selfSignedTestServer(t)
	keyName := "projects/" + gcpBootstrapProjectID + "/serviceAccounts/" + gcpBootstrapServiceAccountEmail + "/keys/" + gcpBootstrapKeyID
	key, ok := iamSAKeys.Get(keyName)
	if !ok {
		t.Fatalf("the bootstrap key %s is not registered", keyName)
	}
	key.Disabled = true
	iamSAKeys.Put(keyName, key)
	t.Cleanup(func() {
		key.Disabled = false
		iamSAKeys.Put(keyName, key)
	})
	token := selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(time.Now(), nil))
	if got := listSecretsStatus(t, base, token); got != http.StatusUnauthorized {
		t.Fatalf("secrets.list under a disabled key: %d, want 401", got)
	}
}

func TestGRPCAcceptsASelfSignedServiceAccountJWT(t *testing.T) {
	_, _, conn := grpcAuthTestServer(t)
	token := selfSignedJWT(t, gcpBootstrapKeyID, bootstrapSelfSignedClaims(time.Now(), nil))
	if _, err := smpb.NewSecretManagerServiceClient(conn).ListSecrets(withBearer(context.Background(), token),
		&smpb.ListSecretsRequest{Parent: "projects/" + gcpTestProjectID}); err != nil {
		t.Fatalf("ListSecrets with a self-signed JWT: %v", err)
	}
}
