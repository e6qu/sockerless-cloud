package lbplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func echoHeadersTarget(t *testing.T) string {
	t.Helper()
	return targetAddress(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
}

func forwardedHeaders(t *testing.T, lbURL string, set func(http.Header)) http.Header {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, lbURL+"/", nil)
	require.NoError(t, err)
	set(req.Header)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var seen http.Header
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&seen))
	return seen
}

func TestForwardAddsTheLoadBalancersRequestHeaders(t *testing.T) {
	lb := frontEnd(t, echoHeadersTarget(t), Upstream{
		Timeout: 5 * time.Second,
		RequestHeader: func(h http.Header) {
			AppendToHeaderList(h, "X-Forwarded-For", ", ", "198.51.100.7")
			h.Set("X-Forwarded-Proto", "http")
		},
	})
	seen := forwardedHeaders(t, lb.URL, func(h http.Header) {
		h.Set("X-Forwarded-For", "203.0.113.1")
		h.Set("X-Forwarded-Proto", "https")
	})
	require.Equal(t, []string{"203.0.113.1, 198.51.100.7"}, seen.Values("X-Forwarded-For"))
	require.Equal(t, []string{"http"}, seen.Values("X-Forwarded-Proto"))
}

// A client that names X-Forwarded-For in Connection strips only its own copy:
// the load balancer adds its header after the hop-by-hop headers are gone.
func TestForwardAddsRequestHeadersAfterRemovingHopByHopHeaders(t *testing.T) {
	lb := frontEnd(t, echoHeadersTarget(t), Upstream{
		Timeout: 5 * time.Second,
		RequestHeader: func(h http.Header) {
			AppendToHeaderList(h, "X-Forwarded-For", ", ", "198.51.100.7")
		},
	})
	seen := forwardedHeaders(t, lb.URL, func(h http.Header) {
		h.Set("Connection", "X-Forwarded-For")
		h.Set("X-Forwarded-For", "203.0.113.1")
	})
	require.Equal(t, []string{"198.51.100.7"}, seen.Values("X-Forwarded-For"))
}

func TestForwardAddsRequestHeadersToAReplacedHeaderSet(t *testing.T) {
	address := echoHeadersTarget(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Clone()
		header.Set("X-Rewritten", "yes")
		err := Forward(w, r, Upstream{
			Scheme: "http", Address: address, Path: "/", Timeout: 5 * time.Second,
			Header:        header,
			RequestHeader: func(h http.Header) { h.Set("X-Forwarded-Proto", "http") },
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	seen := forwardedHeaders(t, srv.URL, func(h http.Header) { h.Set("Connection", "X-Hop"); h.Set("X-Hop", "dropped") })
	require.Equal(t, "yes", seen.Get("X-Rewritten"))
	require.Equal(t, "http", seen.Get("X-Forwarded-Proto"))
	require.Empty(t, seen.Get("X-Hop"))
}

func TestAppendToHeaderListJoinsEveryClientValue(t *testing.T) {
	h := http.Header{}
	AppendToHeaderList(h, "X-Forwarded-For", ",", "198.51.100.7,34.120.0.1")
	require.Equal(t, []string{"198.51.100.7,34.120.0.1"}, h.Values("X-Forwarded-For"))

	h = http.Header{}
	h.Add("Via", "1.0 fred")
	h.Add("Via", "1.1 p.example.net")
	AppendToHeaderList(h, "Via", ", ", "1.1 google")
	require.Equal(t, []string{"1.0 fred, 1.1 p.example.net, 1.1 google"}, h.Values("Via"))
}

func TestClientAddress(t *testing.T) {
	for _, tc := range []struct{ remote, ip, port string }{
		{"192.0.2.10:52100", "192.0.2.10", "52100"},
		{"[2001:db8::1]:443", "2001:db8::1", "443"},
		{"@", "@", ""},
	} {
		ip, port := ClientAddress(&http.Request{RemoteAddr: tc.remote})
		require.Equal(t, tc.ip, ip, tc.remote)
		require.Equal(t, tc.port, port, tc.remote)
	}
}

func TestRandomHex(t *testing.T) {
	first, second := RandomHex(12), RandomHex(12)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{24}$`), first)
	require.NotEqual(t, first, second)
}
