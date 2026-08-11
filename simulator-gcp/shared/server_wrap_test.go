package simulator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWrapHandlerRequestsAreObserved proves a host-addressed data plane
// registered through WrapHandler is logged like any path-routed request.
// Cloud Storage and the Cloud Run service data planes all claim requests by
// Host header
// before the mux ever sees them, so a wrapper that ran outside the logging
// middleware made every data-plane request invisible in the request log.
func TestWrapHandlerRequestsAreObserved(t *testing.T) {
	srv, err := NewServer(Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	srv.HandleFunc("GET /routed", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Host, "bucket.storage.") {
				w.WriteHeader(http.StatusCreated)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	claimed := httptest.NewRequest(http.MethodGet, "/container/blob", nil)
	claimed.Host = "bucket.storage.localhost"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, claimed)
	if rec.Code != http.StatusCreated {
		t.Fatalf("wrapper must claim the host-addressed request: got %d", rec.Code)
	}
	// The request-id header proves the outer observability chain (request-id,
	// logging, tracing) ran for a request the data plane claimed.
	if rec.Header().Get(requestIDHeader("gcp")) == "" {
		t.Fatal("a request the data plane served must pass through the observability chain")
	}

	// The wrapper must still delegate everything it does not claim.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/routed", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unclaimed request must reach the mux: got %d", rec.Code)
	}
	if rec.Header().Get(requestIDHeader("gcp")) == "" {
		t.Fatal("path-routed request must pass through the observability chain")
	}
}
