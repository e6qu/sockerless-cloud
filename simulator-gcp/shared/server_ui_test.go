package simulator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestRegisterUIReportsAuthenticationDisabledWithoutOIDC(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "gcp", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv.RegisterUI(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ui")}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/config.json", nil))
	var got map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("UI config response: %d %q", rec.Code, rec.Body.String())
	}
	if got["identityEndpoint"] != "" || got["logoutEndpoint"] != "" {
		t.Fatalf("UI config coordinates: got %#v", got)
	}
}

func TestFirstPartyOIDCProtectsOperatorSurfacesOnly(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{
		Provider: "gcp", LogLevel: "disabled", UIOIDCIssuer: "https://auth.example.test",
		UIOIDCClientID: "gcp", UIOIDCClientSecret: "secret", UIPublicURL: "https://gcp.example.test",
		UISessionSecret: "0123456789abcdef0123456789abcdef", ApplicationReleaseRevision: "0123456789ab",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.HandleUIFunc("GET /sim/v1/summary", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("dashboard"))
	})
	srv.RegisterUI(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ui")}})
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/auth/oidc/login?return_to=%2Fui%2F" {
		t.Fatalf("UI redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	recorder = httptest.NewRecorder()
	srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sim/v1/summary", nil))
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/auth/oidc/login?return_to=%2Fui%2F" {
		t.Fatalf("dashboard redirect = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	recorder = httptest.NewRecorder()
	srv.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("cloud-independent health status = %d", recorder.Code)
	}
}
