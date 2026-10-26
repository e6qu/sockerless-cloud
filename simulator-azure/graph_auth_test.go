package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func graphCreateApplication(t *testing.T, srv http.Handler, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1.0/applications", strings.NewReader(`{"displayName":"graph-auth"}`))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestGraphRefusesAnUnacceptableBearer(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	armToken, err := mintAzureSimJWT("tenant", defaultAzureTokenAudience, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := mintAzureSimJWT("tenant", "https://graph.microsoft.com", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ authorization, message string }{
		"no token":     {"", "Access token is empty."},
		"not a bearer": {"Basic dXNlcjpwYXNz", "Access token is empty."},
		"malformed":    {"Bearer not-a-jwt", "Access token validation failure."},
		"expired":      {"Bearer " + expired, "Access token validation failure."},
		"ARM audience": {"Bearer " + armToken, "Access token validation failure. Invalid audience."},
	} {
		t.Run(name, func(t *testing.T) {
			rec := graphCreateApplication(t, srv, c.authorization)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
			}
			var body struct {
				Error struct {
					Code       string            `json:"code"`
					Message    string            `json:"message"`
					InnerError map[string]string `json:"innerError"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "InvalidAuthenticationToken" || body.Error.Message != c.message || body.Error.InnerError["request-id"] == "" {
				t.Fatalf("error = %+v", body.Error)
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `client_id="00000003-0000-0000-c000-000000000000"`) {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
}

func TestGraphServesAGraphAudienceBearer(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	for _, audience := range []string{"https://graph.microsoft.com", "00000003-0000-0000-c000-000000000000", "http://example.com"} {
		t.Run(audience, func(t *testing.T) {
			token, err := mintAzureSimJWT("tenant", audience, now, now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if rec := graphCreateApplication(t, srv, "Bearer "+token); rec.Code != http.StatusCreated {
				t.Fatalf("create application with a %s token = %d: %s", audience, rec.Code, rec.Body)
			}
		})
	}
}

func TestGraphLeavesAnUnservedPathToItsOwnAnswer(t *testing.T) {
	srv := buildAuthTestSim(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1.0/noSuchCollection", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("an unserved Graph path answered 401: %s", rec.Body)
	}
}

func ropcToken(t *testing.T, srv http.Handler, username, password string) (int, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"ropc-client"},
		"username":   {username},
		"password":   {password},
		"scope":      {"https://graph.microsoft.com/.default"},
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost:4568/ropc-tenant/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode token response %d: %v: %s", rec.Code, err, rec.Body)
	}
	return rec.Code, body
}

func TestROPCChecksTheUsersPassword(t *testing.T) {
	srv := buildAuthTestSim(t)
	now := time.Now()
	graph, err := mintAzureSimJWT("tenant", "https://graph.microsoft.com", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	const upn = "ropc-user@example.com"
	req := httptest.NewRequest(http.MethodPost, "/v1.0/users", strings.NewReader(
		`{"accountEnabled":true,"displayName":"ROPC","mailNickname":"ropc","userPrincipalName":"`+upn+`","passwordProfile":{"forceChangePasswordNextSignIn":false,"password":"Correct-Horse-1"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+graph)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("the created user echoes its password: %s", rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { entraUsersStore.Delete(created.ID) })

	if status, body := ropcToken(t, srv, upn, "Correct-Horse-1"); status != http.StatusOK || body["access_token"] == nil {
		t.Fatalf("ROPC with the right password = %d: %v", status, body)
	}
	for name, c := range map[string]struct {
		username, password string
		code               float64
	}{
		"wrong password": {upn, "wrong", 50126},
		"no password":    {upn, "", 50126},
		"unknown user":   {"nobody@example.com", "Correct-Horse-1", 50034},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := ropcToken(t, srv, c.username, c.password)
			if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Fatalf("ROPC = %d: %v", status, body)
			}
			codes, _ := body["error_codes"].([]any)
			if len(codes) != 1 || codes[0] != c.code {
				t.Fatalf("error_codes = %v, want [%v]", body["error_codes"], c.code)
			}
			if c.code == 50126 && !strings.HasPrefix(body["error_description"].(string), "AADSTS50126: Error validating credentials due to invalid username or password.") {
				t.Fatalf("error_description = %v", body["error_description"])
			}
		})
	}

	patch := httptest.NewRequest(http.MethodPatch, "/v1.0/users/"+created.ID, strings.NewReader(`{"passwordProfile":{"password":"Battery-Staple-2"}}`))
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("Authorization", "Bearer "+graph)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, patch)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reset password = %d: %s", rec.Code, rec.Body)
	}
	if status, _ := ropcToken(t, srv, upn, "Correct-Horse-1"); status != http.StatusBadRequest {
		t.Fatalf("ROPC with the replaced password = %d, want 400", status)
	}
	if status, body := ropcToken(t, srv, upn, "Battery-Staple-2"); status != http.StatusOK {
		t.Fatalf("ROPC with the new password = %d: %v", status, body)
	}
}
