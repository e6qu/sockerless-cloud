package sim

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A host-addressed data plane registered through WrapHandler is logged like
// any path-routed request: Amazon S3, AWS Amplify Hosting and the
// load-balancer data planes claim requests by Host header before the mux sees
// them, and a wrapper outside the logging middleware would hide them from
// the request log.
func TestWrapHandlerRequestsAreObserved(t *testing.T) {
	srv, err := NewServer(Config{Provider: "aws", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	srv.HandleFunc("GET /routed", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Host, "account.s3.") {
				w.WriteHeader(http.StatusCreated)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	claimed := httptest.NewRequest(http.MethodGet, "/container/blob", nil)
	claimed.Host = "account.s3.localhost"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, claimed)
	if rec.Code != http.StatusCreated {
		t.Fatalf("wrapper must claim the host-addressed request: got %d", rec.Code)
	}
	// The request-id header proves the outer observability chain (request-id,
	// logging, tracing) ran for a request the data plane claimed.
	if rec.Header().Get(requestIDHeader("aws")) == "" {
		t.Fatal("a request the data plane served must pass through the observability chain")
	}

	// The wrapper must still delegate everything it does not claim.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/routed", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unclaimed request must reach the mux: got %d", rec.Code)
	}
	if rec.Header().Get(requestIDHeader("aws")) == "" {
		t.Fatal("path-routed request must pass through the observability chain")
	}
}
