package oidcfed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/simjwt"
)

type memoryKeys map[string]string

func (m memoryKeys) Get(id string) (string, bool) { v, ok := m[id]; return v, ok }
func (m memoryKeys) Put(id, pemText string)       { m[id] = pemText }

// testIssuer is a real OpenID Connect issuer: discovery, a JSON Web Key Set,
// and a key it can rotate.
type testIssuer struct {
	srv         *httptest.Server
	mu          sync.Mutex
	signer      *simjwt.Signer
	discoveries atomic.Int32
	gate        chan struct{}
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	iss := &testIssuer{}
	iss.rotate(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss.discoveries.Add(1)
		if iss.gate != nil {
			<-iss.gate
		}
		iss.writeJSON(w, simjwt.Discovery(iss.srv.URL, iss.srv.URL+"/jwks", []string{"id_token"}, iss.current()))
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		iss.writeJSON(w, simjwt.JWKS(iss.current()))
	})
	iss.srv = httptest.NewServer(mux)
	t.Cleanup(iss.srv.Close)
	return iss
}

func (iss *testIssuer) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (iss *testIssuer) current() *simjwt.Signer {
	iss.mu.Lock()
	defer iss.mu.Unlock()
	return iss.signer
}

func (iss *testIssuer) rotate(t *testing.T) {
	t.Helper()
	s, err := simjwt.LoadOrCreate(memoryKeys{}, "k", simjwt.RS256)
	if err != nil {
		t.Fatal(err)
	}
	iss.mu.Lock()
	iss.signer = s
	iss.mu.Unlock()
}

func (iss *testIssuer) mint(t *testing.T, subject string) string {
	t.Helper()
	now := time.Now()
	token, err := iss.current().Sign(map[string]any{
		"iss": iss.srv.URL, "sub": subject, "aud": "relying-party",
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// The provider a first exchange discovers is cached for later ones, and it
// refetches the issuer's key set when the key rotates. That refetch must not
// run on the first caller's request context, which is long gone by then.
func TestVerifierOutlivesTheFirstCallersContext(t *testing.T) {
	iss := newTestIssuer(t)
	v := New()

	first, cancel := context.WithCancel(context.Background())
	verifier, err := v.Verifier(first, iss.srv.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, err := verifier.Verify(first, iss.mint(t, "first")); err != nil {
		t.Fatalf("verify first token: %v", err)
	}
	cancel()

	iss.rotate(t)
	later, err := v.Verifier(context.Background(), iss.srv.URL)
	if err != nil {
		t.Fatalf("cached verifier: %v", err)
	}
	token, err := later.Verify(context.Background(), iss.mint(t, "second"))
	if err != nil {
		t.Fatalf("verify after the first context was canceled: %v", err)
	}
	if token.Subject != "second" {
		t.Fatalf("subject = %q", token.Subject)
	}
	if got := iss.discoveries.Load(); got != 1 {
		t.Fatalf("discoveries = %d, want 1", got)
	}
}

func TestConcurrentFirstCallersShareOneDiscovery(t *testing.T) {
	iss := newTestIssuer(t)
	iss.gate = make(chan struct{})
	v := New()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Verifier(context.Background(), iss.srv.URL)
			errs <- err
		}()
	}
	close(iss.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := iss.discoveries.Load(); got != 1 {
		t.Fatalf("discoveries = %d, want 1", got)
	}
}

func TestACanceledCallerStopsWaitingWithoutPoisoningTheCache(t *testing.T) {
	iss := newTestIssuer(t)
	iss.gate = make(chan struct{})
	v := New()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verifier(ctx, iss.srv.URL); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(iss.gate)
	if _, err := v.Verifier(context.Background(), iss.srv.URL); err != nil {
		t.Fatalf("discovery after the canceled caller: %v", err)
	}
}

func TestFailedDiscoveryIsRetried(t *testing.T) {
	var up atomic.Bool
	iss := newTestIssuer(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		iss.srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	iss.srv.URL = proxy.URL
	v := New()
	if _, err := v.Verifier(context.Background(), proxy.URL); err == nil {
		t.Fatal("discovery against an unavailable issuer succeeded")
	}
	up.Store(true)
	if _, err := v.Verifier(context.Background(), proxy.URL); err != nil {
		t.Fatalf("discovery once the issuer is up: %v", err)
	}
}

func TestUnverifiedIssuer(t *testing.T) {
	iss := newTestIssuer(t)
	got, err := UnverifiedIssuer(iss.mint(t, "s"))
	if err != nil || got != iss.srv.URL {
		t.Fatalf("issuer = %q, %v", got, err)
	}
	for raw, want := range map[string]string{
		"a.b":            "is not a JWT",
		"a.e30.c":        "has no issuer",
		"a.!!!.c":        "payload could not be decoded: illegal base64 data at input byte 0",
		"a.bm90anNvbg.c": "claims could not be read: invalid character 'o' in literal null (expecting 'u')",
	} {
		if _, err := UnverifiedIssuer(raw); err == nil || err.Error() != want {
			t.Errorf("UnverifiedIssuer(%q) = %v, want %q", raw, err, want)
		}
	}
}

func TestNormalizeIssuerAndAudience(t *testing.T) {
	if got := NormalizeIssuer("https://token.actions.githubusercontent.com/"); got != "token.actions.githubusercontent.com" {
		t.Fatalf("NormalizeIssuer = %q", got)
	}
	if !AudienceIntersects([]string{"a", "b"}, []string{"c", "b"}) || AudienceIntersects([]string{"a"}, []string{"b"}) {
		t.Fatal("AudienceIntersects")
	}
}

// An issuer that accepts the connection and never answers must not stall a
// token exchange that carries no deadline of its own.
func TestDiscoveryAgainstAHungIssuerTimesOut(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer hung.Close()
	defer close(release)

	v := New()
	v.client.Timeout = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := v.Verifier(context.Background(), hung.URL)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("discovery against a hung issuer succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery against a hung issuer did not time out")
	}
}
