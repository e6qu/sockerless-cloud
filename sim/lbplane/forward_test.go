package lbplane

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// frontEnd serves a load balancer whose every request goes to the target at
// address with up's settings.
func frontEnd(t *testing.T, address string, up Upstream) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forward := up
		forward.Scheme = "http"
		forward.Address = address
		forward.Path = r.URL.EscapedPath()
		forward.RawQuery = r.URL.RawQuery
		if err := Forward(w, r, forward); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func targetAddress(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func noRedirects() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestForwardHandsARedirectBackWithItsCookies(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "signed-in"})
			http.Redirect(w, r, "/home", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "home without a session")
	}))
	lb := frontEnd(t, address, Upstream{Timeout: 5 * time.Second})

	resp, err := noRedirects().Get(lb.URL + "/callback")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/home", resp.Header.Get("Location"))
	require.Contains(t, resp.Header.Get("Set-Cookie"), "session=signed-in")
}

func TestForwardTunnelsAWebSocketUpgrade(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsUpgradeRequest(r) {
			http.Error(w, "the upgrade headers did not reach the target", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buf.Flush()
		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte("echo " + line))
	}))
	// The request timeout must not apply to an upgraded connection.
	lb := frontEnd(t, address, Upstream{Timeout: 100 * time.Millisecond})

	conn, err := net.Dial("tcp", strings.TrimPrefix(lb.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /socket HTTP/1.1\r\nHost: lb.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	time.Sleep(300 * time.Millisecond)
	_, err = io.WriteString(conn, "hello\n")
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	reply, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo hello\n", reply)
}

func TestForwardKeepsTheEscapedPathAndQuery(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.RequestURI())
	}))
	lb := frontEnd(t, address, Upstream{Timeout: 5 * time.Second})

	resp, err := http.Get(lb.URL + "/files/a%20b%2Fc?q=1%202")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "/files/a%20b%2Fc?q=1%202", string(body))
}

func TestForwardSendsTheClientsHostUnlessTold(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Host)
	}))
	for _, tc := range []struct {
		name string
		host string
		want string
	}{
		{name: "client host", want: "app.example"},
		{name: "pinned host", host: "backend.internal", want: "backend.internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lb := frontEnd(t, address, Upstream{Timeout: 5 * time.Second, Host: tc.host})
			req, err := http.NewRequest(http.MethodGet, lb.URL+"/", nil)
			require.NoError(t, err)
			req.Host = "app.example"
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(body))
		})
	}
}

func TestForwardDropsHopByHopHeaders(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("X-Hop"), "a header the client's Connection names belongs to that connection")
		require.Equal(t, "kept", r.Header.Get("X-End-To-End"))
		w.Header().Set("Connection", "X-Target-Hop")
		w.Header().Set("X-Target-Hop", "dropped")
		w.Header().Set("X-Answer", "kept")
	}))
	lb := frontEnd(t, address, Upstream{Timeout: 5 * time.Second})
	req, err := http.NewRequest(http.MethodGet, lb.URL+"/", nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "X-Hop")
	req.Header.Set("X-Hop", "dropped")
	req.Header.Set("X-End-To-End", "kept")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("X-Target-Hop"))
	require.Equal(t, "kept", resp.Header.Get("X-Answer"))
}

func TestForwardEditsResponseHeadersAndHonoursDecline(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Target", "yes")
	}))
	up := Upstream{
		Scheme:  "http",
		Address: address,
		Timeout: 5 * time.Second,
		ResponseHeader: func(status int, header http.Header) {
			header.Set("X-Stamped", http.StatusText(status))
		},
		Decline: func(status int) bool { return status == http.StatusNotFound },
	}

	rr := httptest.NewRecorder()
	up.Path = "/found"
	require.NoError(t, Forward(rr, httptest.NewRequest(http.MethodGet, "/found", nil), up))
	require.Equal(t, "OK", rr.Header().Get("X-Stamped"))
	require.Equal(t, "yes", rr.Header().Get("X-Target"))

	rr = httptest.NewRecorder()
	up.Path = "/missing"
	err := Forward(rr, httptest.NewRequest(http.MethodGet, "/missing", nil), up)
	require.ErrorIs(t, err, ErrDeclined)
	require.Empty(t, rr.Header())
	require.Zero(t, rr.Body.Len())
}

func TestForwardTellsAnUnreachableTargetFromAClientThatLeft(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := ln.Addr().String()
	require.NoError(t, ln.Close())

	err = Forward(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
		Upstream{Scheme: "http", Address: closed, Path: "/", Timeout: 5 * time.Second})
	var sendErr *SendError
	require.ErrorAs(t, err, &sendErr)
	require.Equal(t, closed, sendErr.Address)

	hang := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	err = Forward(httptest.NewRecorder(), req, Upstream{Scheme: "http", Address: hang, Path: "/", Timeout: 5 * time.Second})
	require.True(t, errors.Is(err, ErrClientWentAway), "got %v", err)
}

func TestHostname(t *testing.T) {
	require.Equal(t, "lb.example.com", Hostname("LB.Example.com.:8080"))
	require.Equal(t, "10.0.0.1", Hostname("10.0.0.1"))
	require.Equal(t, "::1", Hostname("[::1]:80"))
}

func TestForwardIdleTimeoutAnswersNothingFromASilentTarget(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	silent := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	err := Forward(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil),
		Upstream{Scheme: "http", Address: silent, Path: "/", IdleTimeout: 100 * time.Millisecond})
	var sendErr *SendError
	require.ErrorAs(t, err, &sendErr)
	require.ErrorIs(t, err, ErrIdleTimeout)
}

// An idle timeout is not a deadline: an answer whose bytes keep arriving
// outlasts it many times over.
func TestForwardIdleTimeoutSparesAnAnswerThatKeepsFlowing(t *testing.T) {
	const idle = 200 * time.Millisecond
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticker := time.NewTicker(idle / 4)
		defer ticker.Stop()
		for range 12 {
			_, _ = io.WriteString(w, ".")
			w.(http.Flusher).Flush()
			<-ticker.C
		}
	}))
	var activity int
	rr := httptest.NewRecorder()
	err := Forward(rr, httptest.NewRequest(http.MethodGet, "/", nil),
		Upstream{Scheme: "http", Address: address, Path: "/", IdleTimeout: idle, Activity: func() { activity++ }})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat(".", 12), rr.Body.String())
	require.Positive(t, activity)
}

func TestForwardIdleTimeoutCutsAnAnswerThatStalls(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	rr := httptest.NewRecorder()
	err := Forward(rr, httptest.NewRequest(http.MethodGet, "/", nil),
		Upstream{Scheme: "http", Address: address, Path: "/", IdleTimeout: 100 * time.Millisecond})
	require.ErrorIs(t, err, ErrIdleTimeout)
	var sendErr *SendError
	require.False(t, errors.As(err, &sendErr), "the answer had begun, so this is no send error")
	require.Equal(t, "partial", rr.Body.String())
}

func TestForwardIdleTimeoutClosesAQuietTunnel(t *testing.T) {
	address := targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buf.Flush()
		_, _ = io.Copy(io.Discard, buf)
	}))
	lb := frontEnd(t, address, Upstream{IdleTimeout: 100 * time.Millisecond})

	conn, err := net.Dial("tcp", strings.TrimPrefix(lb.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /socket HTTP/1.1\r\nHost: lb.example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF, "the load balancer closes a tunnel no byte crossed for the idle timeout")
}
