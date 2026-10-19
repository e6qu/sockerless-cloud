package sim

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An in-process call runs the server's middleware and handler, carries the
// caller it is made as, and returns what the handler answered.
func TestCallServesThroughTheWholeChain(t *testing.T) {
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	var wrapped bool
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped = true
			next.ServeHTTP(w, r)
		})
	})
	srv.HandleFunc("POST /queues/{name}", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := CallerOf(r.Context())
		if !ok || !InProcess(r) {
			http.Error(w, "not an in-process call", http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Queue", r.PathValue("name"))
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, caller.Service+" for "+caller.Source+": "+string(body))
	})

	ctx := WithCaller(context.Background(), Caller{Service: "events.amazonaws.com", Source: "arn:aws:events:us-east-1:123456789012:rule/r", Account: "123456789012"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/queues/orders", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	response := srv.Call(req)
	if !wrapped {
		t.Error("the call skipped the server's middleware")
	}
	if response.StatusCode != http.StatusAccepted || response.Header().Get("X-Queue") != "orders" {
		t.Fatalf("status %d, header %v", response.StatusCode, response.Header())
	}
	if got, want := response.Body.String(), "events.amazonaws.com for arn:aws:events:us-east-1:123456789012:rule/r: hello"; got != want {
		t.Errorf("body %q, want %q", got, want)
	}
}

// A request from outside the simulator is not in-process and names no caller.
func TestAnOutsideRequestCarriesNoCaller(t *testing.T) {
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	srv.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CallerOf(r.Context()); ok || InProcess(r) {
			w.WriteHeader(http.StatusForbidden)
		}
	})
	response := &CallResponse{}
	srv.ServeHTTP(response, mustRequest(t, http.MethodGet, "/whoami"))
	if response.StatusCode != 0 && response.StatusCode != http.StatusOK {
		t.Fatalf("an outside request was taken for an in-process call: %d", response.StatusCode)
	}
}

func mustRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
