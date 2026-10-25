package lbplane

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostRouterClaimsOnlyItsHostnames(t *testing.T) {
	router := HostRouter{
		Domains: []string{".run.app"},
		Route: func(_ *http.Request, hostname string) (http.Handler, bool) {
			if hostname != "svc-abc-uc.a.run.app" {
				return nil, false
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "workload")
			}), true
		},
		NotFound: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no such workload", http.StatusNotFound)
		}),
	}
	handler := router.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "api")
	}))
	serve := func(host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	require.Equal(t, "workload", serve("SVC-abc-uc.a.run.app.:443").Body.String(), "the hostname is matched without port, case or root dot")
	require.Equal(t, http.StatusNotFound, serve("gone-abc-uc.a.run.app").Code)
	require.Equal(t, "api", serve("run.googleapis.com").Body.String())
}

func TestHostRouterWithoutDomainsPassesUnknownHostsOn(t *testing.T) {
	router := HostRouter{Route: func(*http.Request, string) (http.Handler, bool) { return nil, false }}
	handler := router.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "api")
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "app.example.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, "api", w.Body.String())
}

// serveFrontEnd serves a front end whose every request goes through Serve to
// the target at address.
func serveFrontEnd(t *testing.T, address string, up Upstream, a Answers) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward := up
		forward.Scheme = "http"
		forward.Address = address
		forward.Path = r.URL.EscapedPath()
		_ = Serve(w, r, forward, a)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func answerWith(status int) func(http.ResponseWriter, error) {
	return func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), status) }
}

func TestServeAnswersWhatTheTargetCouldNot(t *testing.T) {
	answers := Answers{
		NotAdmitted: answerWith(http.StatusServiceUnavailable),
		Timeout:     answerWith(http.StatusGatewayTimeout),
		Failed:      answerWith(http.StatusBadGateway),
	}
	release := make(chan struct{})
	slow := targetAddress(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release) })
	ok := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "short and stout")
	}))
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	unreachable := closed.Addr().String()
	require.NoError(t, closed.Close())

	get := func(srv *httptest.Server) (int, string) {
		resp, err := http.Get(srv.URL)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	status, body := get(serveFrontEnd(t, ok, Upstream{Timeout: 5 * time.Second}, answers))
	require.Equal(t, http.StatusTeapot, status)
	require.Equal(t, "short and stout", body)

	status, _ = get(serveFrontEnd(t, slow, Upstream{Timeout: 50 * time.Millisecond}, answers))
	require.Equal(t, http.StatusGatewayTimeout, status)

	status, _ = get(serveFrontEnd(t, unreachable, Upstream{Timeout: 5 * time.Second}, answers))
	require.Equal(t, http.StatusBadGateway, status)

	refuse := func(netip.Addr) error { return errors.New("denied") }
	status, _ = get(serveFrontEnd(t, ok, Upstream{Timeout: 5 * time.Second, Admit: refuse}, answers))
	require.Equal(t, http.StatusServiceUnavailable, status, "a refused flow is answered, not cut off")

	status, _ = get(serveFrontEnd(t, slow, Upstream{Timeout: 50 * time.Millisecond}, Answers{Failed: answerWith(http.StatusBadGateway)}))
	require.Equal(t, http.StatusBadGateway, status, "without a Timeout answer a timeout is a failure")
}

func TestServeCutsOffAnAnswerItLosesMidStream(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial")
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	}))
	lb := serveFrontEnd(t, address, Upstream{Timeout: 5 * time.Second}, Answers{Failed: answerWith(http.StatusBadGateway)})

	resp, err := http.Get(lb.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the target's own status reaches the client")
	body, err := io.ReadAll(resp.Body)
	require.Error(t, err, "the client sees the stream end early rather than an error page appended to it")
	require.True(t, strings.HasPrefix(string(body), "partial"))
	require.NotContains(t, string(body), "Bad Gateway")
}

func TestServeRecordsAClientThatWentAway(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	address := targetAddress(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(arrived)
		<-release
	}))
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	go func() {
		<-arrived
		cancel()
	}()
	err := Serve(w, r, Upstream{Scheme: "http", Address: address, Path: "/", Timeout: time.Minute},
		Answers{Failed: answerWith(http.StatusBadGateway)})
	require.ErrorIs(t, err, ErrClientWentAway)
	require.Equal(t, StatusClientClosedRequest, w.Code)
}

func TestServeLeavesADeclinedAnswerToTheCaller(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	err := Serve(w, r, Upstream{Scheme: "http", Address: address, Path: "/", Timeout: 5 * time.Second,
		Decline: func(status int) bool { return status == http.StatusNotFound }},
		Answers{Failed: answerWith(http.StatusBadGateway)})
	require.ErrorIs(t, err, ErrDeclined)
	require.False(t, w.Flushed)
	require.Empty(t, w.Body.String())
}
