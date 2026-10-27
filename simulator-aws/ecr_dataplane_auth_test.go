package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Returns the Basic parameter GetAuthorizationToken serves and the password
// `aws ecr get-login-password` prints.
func ecrAuthTestToken(t *testing.T) (authorizationToken, password string) {
	t.Helper()
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ecrAuthorizationTokens = sim.MakeStore[ECRAuthorizationToken](nil, "ecr_authorization_tokens")
	token, expires, err := ecrIssueAuthorizationToken()
	if err != nil {
		t.Fatalf("issue authorization token: %v", err)
	}
	if remaining := time.Until(expires); remaining < 11*time.Hour || remaining > 12*time.Hour {
		t.Fatalf("authorization token must be valid for 12 hours; got %v", remaining)
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("authorization token is not base64: %v", err)
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok || user != "AWS" {
		t.Fatalf("decoded authorization token must be AWS:<password>; got %q", raw)
	}
	return token, pass
}

// Reports whether the Authorize hook let the request through, and the
// refusal.
func ecrAuthProbe(t *testing.T, method, target, authorization string) (bool, *http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "123456789012.dkr.ecr.us-east-1.amazonaws.com"
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	repo := ""
	if rest, ok := strings.CutPrefix(target, "/v2/"); ok {
		if idx := strings.Index(rest, "/manifests/"); idx >= 0 {
			repo = rest[:idx]
		}
	}
	allowed := ecrAuthorizeV2(rec, req, repo)
	resp := rec.Result()
	body := rec.Body.String()
	return allowed, resp, body
}

// The whole token is the Basic parameter, as the User Guide's
// `curl -i -H "Authorization: Basic $TOKEN"` example presents it.
func TestECRDataPlaneAcceptsTheAuthorizationTokenItIssued(t *testing.T) {
	token, password := ecrAuthTestToken(t)

	for _, probe := range []struct {
		name   string
		method string
		target string
	}{
		{"base endpoint", http.MethodGet, "/v2/"},
		{"manifest pull", http.MethodGet, "/v2/team/app/manifests/v1"},
		{"manifest push", http.MethodPut, "/v2/team/app/manifests/v1"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if allowed, _, body := ecrAuthProbe(t, probe.method, probe.target, "Basic "+token); !allowed {
				t.Fatalf("the issued authorization token must authenticate; refused with %s", body)
			}
			// The credential Docker rebuilds from `aws ecr get-login-password`.
			docker := base64.StdEncoding.EncodeToString([]byte("AWS:" + password))
			if allowed, _, body := ecrAuthProbe(t, probe.method, probe.target, "Basic "+docker); !allowed {
				t.Fatalf("the docker login credential must authenticate; refused with %s", body)
			}
		})
	}
}

// No credential, a wrong password, a user other than AWS and an undecodable
// parameter each get the Basic challenge and the plain-text body.
func TestECRDataPlaneRefusesEveryOtherCredential(t *testing.T) {
	_, password := ecrAuthTestToken(t)

	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	for _, probe := range []struct {
		name          string
		authorization string
	}{
		{"no credential", ""},
		{"wrong password", basic("AWS", "not-the-password")},
		{"empty password", basic("AWS", "")},
		{"another user", basic("root", password)},
		{"undecodable basic parameter", "Basic not-base64!!"},
		{"bearer instead of basic", "Bearer " + password},
	} {
		t.Run(probe.name, func(t *testing.T) {
			allowed, resp, body := ecrAuthProbe(t, http.MethodGet, "/v2/team/app/manifests/v1", probe.authorization)
			if allowed {
				t.Fatal("the registry must refuse this credential")
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			const challenge = `Basic realm="http://123456789012.dkr.ecr.us-east-1.amazonaws.com/",service="ecr.amazonaws.com"`
			if got := resp.Header.Get("Www-Authenticate"); got != challenge {
				t.Fatalf("challenge = %q, want %q", got, challenge)
			}
			if got := resp.Header.Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
				t.Fatalf("Docker-Distribution-Api-Version = %q, want registry/2.0", got)
			}
			if strings.TrimSpace(body) != "Not Authorized" {
				t.Fatalf("body = %q, want Not Authorized", body)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Fatalf("Content-Type = %q, want text/plain; charset=utf-8", ct)
			}
		})
	}
}

// The expired record is also dropped, so it cannot come back.
func TestECRDataPlaneRefusesAnExpiredAuthorizationToken(t *testing.T) {
	token, password := ecrAuthTestToken(t)

	stored, ok := ecrAuthorizationTokens.Get(password)
	if !ok {
		t.Fatal("the issued authorization token must be recorded under its password")
	}
	expired := stored
	expired.IssuedAt = time.Now().Add(-13 * time.Hour).Unix()
	expired.ExpiresAt = time.Now().Add(-time.Hour).Unix()
	ecrAuthorizationTokens.Put(password, expired)

	allowed, resp, body := ecrAuthProbe(t, http.MethodGet, "/v2/team/app/manifests/v1", "Basic "+token)
	if allowed {
		t.Fatal("an expired authorization token must not authenticate")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("expiry refusal must carry the registry error envelope; got %q: %v", body, err)
	}
	if len(envelope.Errors) != 1 || envelope.Errors[0].Code != "DENIED" {
		t.Fatalf("envelope = %+v, want one DENIED error", envelope.Errors)
	}
	const message = "Your authorization token has expired. Reauthenticate and try again."
	if envelope.Errors[0].Message != message {
		t.Fatalf("message = %q, want %q", envelope.Errors[0].Message, message)
	}
	if _, still := ecrAuthorizationTokens.Get(password); still {
		t.Fatal("an expired authorization token must not stay in the store")
	}
}

// Tokens never repeat and are neither derived from the registry nor short.
func TestECRAuthorizationTokensAreUnguessableAndDistinct(t *testing.T) {
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ecrAuthorizationTokens = sim.MakeStore[ECRAuthorizationToken](nil, "ecr_authorization_tokens")
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		token, _, err := ecrIssueAuthorizationToken()
		if err != nil {
			t.Fatalf("issue authorization token: %v", err)
		}
		if seen[token] {
			t.Fatalf("GetAuthorizationToken repeated a token: %q", token)
		}
		seen[token] = true
		raw, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("authorization token is not base64: %v", err)
		}
		_, password, _ := strings.Cut(string(raw), ":")
		if len(password) < 40 {
			t.Fatalf("authorization token password is only %d characters", len(password))
		}
	}
	if got := len(ecrAuthorizationTokens.List()); got != 8 {
		t.Fatalf("issued token count = %d, want 8", got)
	}
}
