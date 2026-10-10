package lbplane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrClientWentAway marks a forward that failed because the client
// disconnected, not because the target did.
var ErrClientWentAway = errors.New("client closed the request before the target answered")

// StatusClientClosedRequest is the non-standard status nginx and the cloud
// load-balancer access logs record for a request its client abandoned.
const StatusClientClosedRequest = 499

// ErrDeclined reports that Upstream.Decline refused the target's answer, so
// Forward wrote nothing and the caller may still answer the client.
var ErrDeclined = errors.New("the target's answer was declined")

// SendError is a forward that reached no answer from the target. Nothing has
// been written to the client when Forward returns one.
type SendError struct {
	Address string
	Err     error
}

func (e *SendError) Error() string { return fmt.Sprintf("forward to %s: %v", e.Address, e.Err) }

func (e *SendError) Unwrap() error { return e.Err }

// Upstream describes where and how a load balancer forwards one request.
type Upstream struct {
	// Scheme is "http" or "https".
	Scheme string
	// Address is the target's host:port.
	Address string
	// Path is the escaped path and RawQuery the query the target receives.
	Path     string
	RawQuery string
	// Host is the Host header the target receives; empty keeps the client's.
	Host string
	// Header replaces the client's request headers when set. Forward still
	// removes the hop-by-hop headers from it.
	Header http.Header
	// RequestHeader, when set, edits the headers the target receives once the
	// client's hop-by-hop headers are gone, so a header the client nominated in
	// Connection cannot strip one the load balancer adds.
	RequestHeader func(header http.Header)
	// Body replaces the client's request body when set.
	Body io.Reader
	// Timeout bounds a request/response exchange from start to finish. An
	// upgraded connection is not one — a WebSocket is meant to last for hours —
	// so it never applies there.
	Timeout time.Duration
	// IdleTimeout bounds how long the forward may pass with no byte moving in
	// either direction; every byte read from the client or the target restarts
	// it, so it never cuts off an answer that keeps flowing. It covers an
	// upgraded connection too.
	IdleTimeout time.Duration
	// Activity, when set, runs whenever bytes restart the IdleTimeout.
	Activity func()
	// SkipTargetVerification accepts any certificate an https target
	// presents, as a load balancer that does not validate its targets does.
	SkipTargetVerification bool
	// ResponseHeader, when set, edits the target's response headers before
	// they are written to the client.
	ResponseHeader func(status int, header http.Header)
	// Decline, when set, may refuse the target's answer by status; Forward then
	// discards it and returns ErrDeclined.
	Decline func(status int) bool
}

// hopByHopHeaders belong to one connection and are never forwarded (RFC 9110
// section 7.6.1).
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// RemoveHopByHopHeaders removes the headers that belong to one connection,
// including each one the Connection header nominates.
func RemoveHopByHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				header.Del(name)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		header.Del(name)
	}
}

// Forward sends r to the target up names and relays its answer: headers,
// status and body, or — for a 101 Switching Protocols — the upgraded
// connection itself.
//
// A load balancer hands a target's redirect back to its client and never
// follows it. Following one fetches the redirect's destination instead and,
// since the forwarding client keeps no cookie jar, drops every Set-Cookie the
// redirect carried, which breaks each OpenID Connect sign-in behind the load
// balancer without an error to explain it.
func Forward(w http.ResponseWriter, r *http.Request, up Upstream) error {
	ctx := r.Context()
	upgrade := IsUpgradeRequest(r)
	if !upgrade && up.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, up.Timeout)
		defer cancel()
	}
	ctx, idle, cancelIdle := watchExchange(ctx, up.IdleTimeout, up.Activity)
	defer cancelIdle(nil)
	defer idle.stop()
	target := up.Scheme + "://" + up.Address + up.Path
	if up.RawQuery != "" {
		target += "?" + up.RawQuery
	}
	body := up.Body
	if body == nil {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return err
	}
	if up.Body == nil {
		req.ContentLength = r.ContentLength
	}
	if idle != nil && req.Body != nil && req.Body != http.NoBody {
		req.Body = idleReadCloser{Reader: idle.reader(req.Body), Closer: req.Body}
	}
	header := up.Header
	if header == nil {
		header = r.Header.Clone()
	}
	RemoveHopByHopHeaders(header)
	if up.RequestHeader != nil {
		up.RequestHeader(header)
	}
	if upgrade {
		header.Set("Connection", "Upgrade")
		header.Set("Upgrade", r.Header.Get("Upgrade"))
	}
	req.Header = header
	req.Host = up.Host
	if req.Host == "" {
		req.Host = r.Host
	}
	client := http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if up.SkipTargetVerification {
		client.Transport = unverifiedTransport
	}
	resp, err := client.Do(req)
	if err != nil {
		// A browser abandons in-flight requests whenever it navigates, and the
		// forward inherits the inbound context, so a client hanging up surfaces
		// here. That is not a failed target, and must not read as one.
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			return ErrClientWentAway
		}
		if errors.Is(context.Cause(ctx), ErrIdleTimeout) {
			return &SendError{Address: up.Address, Err: ErrIdleTimeout}
		}
		return &SendError{Address: up.Address, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		idle.stop()
		return tunnelUpgradedResponse(w, resp, up.IdleTimeout, up.Activity)
	}
	if up.Decline != nil && up.Decline(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrDeclined
	}
	responseHeader := resp.Header.Clone()
	RemoveHopByHopHeaders(responseHeader)
	if up.ResponseHeader != nil {
		up.ResponseHeader(resp.StatusCode, responseHeader)
	}
	for key, values := range responseHeader {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err = io.Copy(w, idle.reader(resp.Body)); err != nil && errors.Is(context.Cause(ctx), ErrIdleTimeout) {
		return ErrIdleTimeout
	}
	return err
}

// Hostname reduces a Host header to the name a data plane is addressed by: no
// port, lower case, no trailing root dot.
func Hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// unverifiedTransport is shared so connections to targets are pooled across
// requests the way the default transport pools them.
var unverifiedTransport = newUnverifiedTransport()

func newUnverifiedTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the load balancers this serves do not validate target certificates
	}
}
