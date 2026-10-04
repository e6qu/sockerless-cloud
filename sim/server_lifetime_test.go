package sim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Work a request starts outlives the caller that hung up on it and ends when
// the server stops.
func TestLifetimeContextOutlivesTheRequestAndEndsAtShutdown(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "lifetime-test"})
	if err != nil {
		t.Fatal(err)
	}
	lifetimes := make(chan context.Context, 1)
	srv.HandleFunc("GET /work", func(w http.ResponseWriter, r *http.Request) {
		lifetimes <- LifetimeContext(r.Context())
		<-r.Context().Done()
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	ctx, hangUp := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		called <- err
	}()
	lifetime := <-lifetimes
	hangUp()
	if err := <-called; err == nil {
		t.Fatal("the request returned a response after its caller hung up")
	}
	if err := lifetime.Err(); err != nil {
		t.Fatalf("lifetime context ended with its request: %v", err)
	}

	srv.StopBackground()
	select {
	case <-lifetime.Done():
	default:
		t.Fatal("lifetime context outlived the server's shutdown")
	}
}

// A handler called in-process with a request built from RequestContext finds
// the lifetime the server gives the requests it serves.
func TestRequestContextCarriesTheServersLifetime(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := NewServer(Config{Provider: "request-context-test"})
	if err != nil {
		t.Fatal(err)
	}
	lifetime := LifetimeContext(srv.RequestContext(context.Background()))
	if err := lifetime.Err(); err != nil {
		t.Fatalf("lifetime context ended before the server stopped: %v", err)
	}
	srv.StopBackground()
	select {
	case <-lifetime.Done():
	default:
		t.Fatal("lifetime context outlived the server's shutdown")
	}
}

func TestLifetimeContextRefusesAContextNoServerServes(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("LifetimeContext returned for a context no server serves")
		}
	}()
	LifetimeContext(context.Background())
}
