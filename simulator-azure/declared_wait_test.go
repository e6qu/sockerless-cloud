package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An Application Gateway holds a request open for as long as its backend
// settings' requestTimeout lets the backend take, so that is the wait it
// declares.
func TestApplicationGatewayForwardDeclaresTheRequestTimeout(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served " + r.URL.Path))
	}))
	t.Cleanup(backend.Close)
	host, portText, err := net.SplitHostPort(backend.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		requestTimeout int32
		want           time.Duration
	}{
		{requestTimeout: 45, want: 45 * time.Second},
		{requestTimeout: 0, want: 30 * time.Second},
	}
	for _, tc := range cases {
		var settings ApplicationGatewayBackendHTTPSettings
		settings.Properties.Port = int32(port)
		settings.Properties.Protocol = "Http"
		settings.Properties.RequestTimeout = tc.requestTimeout

		var wait time.Duration
		var openEnded bool
		var forwardErr error
		rec := httptest.NewRecorder()
		sim.InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwardErr = applicationGatewayForward(w, r, settings, nil, applicationGatewayServer{address: host})
			wait, openEnded = sim.DeclaredWait(r.Context())
		})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders", nil))

		if forwardErr != nil || rec.Code != http.StatusOK || rec.Body.String() != "served /orders" {
			t.Fatalf("forward with requestTimeout %d answered %d %q: %v", tc.requestTimeout, rec.Code, rec.Body.String(), forwardErr)
		}
		if wait != tc.want || openEnded {
			t.Errorf("forward with requestTimeout %d declared %s (open-ended %v), want %s", tc.requestTimeout, wait, openEnded, tc.want)
		}
	}
}
