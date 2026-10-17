package lbplane

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseStatusMatcher(t *testing.T) {
	m, err := ParseStatusMatcher("200,202", "300-399")
	require.NoError(t, err)
	for code, want := range map[int]bool{200: true, 201: false, 202: true, 302: true, 400: false} {
		require.Equal(t, want, m.Matches(code), "code %d", code)
	}
	for _, bad := range []string{"", "abc", "200-", "299-200"} {
		_, err := ParseStatusMatcher(bad)
		require.Error(t, err, "%q", bad)
	}
	require.True(t, StatusRange(200, 399).Matches(301))
	require.False(t, StatusRange(200, 399).Matches(404))
}

func TestProbeHTTPJudgesARedirectByItsOwnCode(t *testing.T) {
	var followed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	address := strings.TrimPrefix(srv.URL, "http://")
	only200, err := ParseStatusMatcher("200")
	require.NoError(t, err)

	status, err := ProbeHTTP(context.Background(), HTTPProbe{
		Scheme: "http", Address: address, Path: "/", Timeout: 5 * time.Second, Match: only200,
	})
	var mismatch *StatusMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, http.StatusFound, mismatch.StatusCode)
	require.Equal(t, http.StatusFound, status)
	require.False(t, followed.Load(), "a health check must not follow a redirect")

	status, err = ProbeHTTP(context.Background(), HTTPProbe{
		Scheme: "http", Address: address, Path: "/", Timeout: 5 * time.Second, Match: StatusRange(200, 399),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, status)
}

func TestProbeHTTPSendsHostAndMatchesTheBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "host="+r.Host+" "+strings.Repeat("x", 2000)+" late-marker")
	}))
	defer srv.Close()
	address := strings.TrimPrefix(srv.URL, "https://")
	base := HTTPProbe{
		Scheme: "https", Address: address, Host: "app.example", Path: "/health",
		Timeout: 5 * time.Second, Match: StatusRange(200, 200), BodyLimit: 1024,
	}

	base.BodyContains = "host=app.example"
	_, err := ProbeHTTP(context.Background(), base)
	require.NoError(t, err, "an https probe that does not verify accepts a self-signed target")

	base.BodyContains = "late-marker"
	_, err = ProbeHTTP(context.Background(), base)
	var bodyErr *BodyMismatchError
	require.ErrorAs(t, err, &bodyErr, "text past the body limit does not count")

	base.BodyContains = ""
	base.VerifyCertificate = true
	_, err = ProbeHTTP(context.Background(), base)
	require.Error(t, err, "a verifying probe rejects a certificate the host does not trust")
}

func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := ln.Addr().String()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	require.NoError(t, ProbeTCP(context.Background(), address, time.Second))
	require.NoError(t, ln.Close())
	require.ErrorIs(t, ProbeTCP(context.Background(), address, time.Second), syscall.ECONNREFUSED,
		"a probe of a closed port reports the refused connection")
}

func TestHealthTrackerFollowsThresholds(t *testing.T) {
	tracker := NewHealthTracker[string]()
	var failing atomic.Bool
	probe := func(context.Context) (int, error) {
		if failing.Load() {
			return 503, errors.New("down")
		}
		return 200, nil
	}
	policy := HealthPolicy{Interval: time.Second, InitialHealthyThreshold: 2, HealthyThreshold: 3, UnhealthyThreshold: 2}
	targets := []HealthTarget[string]{{Key: "a", Policy: policy, Probe: probe}}
	now := time.Unix(1_700_000_000, 0)
	step := func() []string {
		changed := tracker.Sweep(context.Background(), now, targets)
		now = now.Add(policy.Interval)
		return changed
	}
	state := func() HealthState {
		h, ok := tracker.Health("a")
		require.True(t, ok)
		return h.State
	}

	require.Empty(t, step())
	require.Equal(t, HealthInitial, state(), "one success is below the initial threshold")
	require.Equal(t, []string{"a"}, step())
	require.Equal(t, HealthHealthy, state())

	failing.Store(true)
	step()
	require.Equal(t, HealthHealthy, state(), "one failure is below the unhealthy threshold")
	require.Equal(t, []string{"a"}, step())
	require.Equal(t, HealthUnhealthy, state())
	h, _ := tracker.Health("a")
	require.EqualError(t, h.LastFailure, "down")
	require.Equal(t, 503, h.LastStatus)

	failing.Store(false)
	step()
	step()
	require.Equal(t, HealthUnhealthy, state(), "returning to service takes the healthy threshold")
	step()
	require.Equal(t, HealthHealthy, state())

	// A sweep before the interval has passed checks nothing.
	checks := func() int { h, _ := tracker.Health("a"); return h.Checks }
	before := checks()
	tracker.Sweep(context.Background(), now.Add(-policy.Interval/2), targets)
	require.Equal(t, before, checks())

	// A target that leaves the set is forgotten.
	tracker.Sweep(context.Background(), now, nil)
	_, ok := tracker.Health("a")
	require.False(t, ok)
}
