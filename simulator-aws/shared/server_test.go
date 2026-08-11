package simulator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHealthReportsProcessRuntimeCapability(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got struct {
		Runtime      string          `json:"runtime"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if got.Runtime != "process" || got.Capabilities["workloadExecution"] {
		t.Fatalf("health capability = %#v, want process runtime without workload execution", got)
	}
}

func TestRegisterUIReportsAuthenticationDisabledWithoutOIDC(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv.RegisterUI(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ui")}})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/config.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("UI config status: got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("UI config cache policy: got %q", got)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode UI config: %v", err)
	}
	if got["identityEndpoint"] != "" || got["logoutEndpoint"] != "" {
		t.Fatalf("UI config coordinates: got %#v", got)
	}
}

func TestNewServerRejectsPartialUIOIDCConfiguration(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	for name, cfg := range map[string]Config{
		"partial":  {Provider: "aws", LogLevel: "disabled", UIOIDCIssuer: "https://auth.example.test"},
		"insecure": {Provider: "aws", LogLevel: "disabled", UIOIDCIssuer: "http://auth.example.test", UIOIDCClientID: "aws", UIOIDCClientSecret: "secret", UIPublicURL: "https://aws.example.test", UISessionSecret: "0123456789abcdef0123456789abcdef", ApplicationReleaseRevision: "0123456789ab"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewServer(cfg); err == nil {
				t.Fatal("invalid UI OpenID Connect configuration must fail startup")
			}
		})
	}
}

func TestFirstPartyOIDCProtectsOperatorSurfacesOnly(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{
		Provider: "aws", LogLevel: "disabled", UIOIDCIssuer: "https://auth.example.test",
		UIOIDCClientID: "aws", UIOIDCClientSecret: "secret", UIPublicURL: "https://aws.example.test",
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

// RegisterUI must not panic the mux when a simulated service already owns
// the API root: S3's ListBuckets registers "GET /{$}", and the root belongs
// to the API surface — the UI stays reachable at /ui/.
func TestRegisterUICoexistsWithAPIRoot(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("api-root"))
	})
	ui := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ui</html>")}}
	srv.RegisterUI(ui) // must not panic on the duplicate "GET /{$}"

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "api-root" {
		t.Fatalf("API root must keep serving: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>ui</html>" {
		t.Fatalf("UI must serve at /ui/: %d %q", rec.Code, rec.Body.String())
	}
}

// Without a service on the API root, RegisterUI's redirect applies.
func TestRegisterUIRedirectsBareRoot(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv.RegisterUI(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ui")}})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("bare root must redirect to /ui/: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
