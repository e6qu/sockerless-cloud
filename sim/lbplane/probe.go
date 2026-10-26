package lbplane

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// StatusMatcher is a set of HTTP status codes a health check counts as a
// success, written as codes and inclusive ranges ("200", "200,202",
// "200-299").
type StatusMatcher struct {
	ranges [][2]int
}

// ParseStatusMatcher reads each entry as a comma-separated list of codes and
// "low-high" ranges.
func ParseStatusMatcher(entries ...string) (StatusMatcher, error) {
	var m StatusMatcher
	for _, entry := range entries {
		for _, value := range strings.Split(entry, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			low, high, isRange := strings.Cut(value, "-")
			first, err := strconv.Atoi(strings.TrimSpace(low))
			if err != nil {
				return StatusMatcher{}, fmt.Errorf("status code %q is not a number", value)
			}
			last := first
			if isRange {
				if last, err = strconv.Atoi(strings.TrimSpace(high)); err != nil {
					return StatusMatcher{}, fmt.Errorf("status code range %q does not end in a number", value)
				}
			}
			if first > last {
				return StatusMatcher{}, fmt.Errorf("status code range %q ends before it starts", value)
			}
			m.ranges = append(m.ranges, [2]int{first, last})
		}
	}
	if len(m.ranges) == 0 {
		return StatusMatcher{}, fmt.Errorf("no status code to match")
	}
	return m, nil
}

// StatusRange matches every code from low to high inclusive.
func StatusRange(low, high int) StatusMatcher {
	return StatusMatcher{ranges: [][2]int{{low, high}}}
}

func (m StatusMatcher) Matches(code int) bool {
	for _, r := range m.ranges {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

// HTTPProbe is one HTTP or HTTPS health check request.
type HTTPProbe struct {
	// Scheme is "http" or "https".
	Scheme string
	// VerifyCertificate validates an https target's certificate against the
	// host's roots; without it the probe accepts any certificate, as a health
	// checker that does not validate its targets does.
	VerifyCertificate bool
	// ServerName is the TLS server name an https probe sends; empty derives it
	// from Address, as an https client does.
	ServerName string
	Address    string
	// Host is the Host header the probe sends; empty sends Address.
	Host string
	Path string
	// Header holds the further request headers the probe sends.
	Header http.Header
	// Timeout bounds the whole check. ConnectTimeout, when set, bounds opening
	// the connection, and ResponseTimeout, when set, bounds the wait for the
	// response's headers once the request is sent.
	Timeout         time.Duration
	ConnectTimeout  time.Duration
	ResponseTimeout time.Duration
	Match           StatusMatcher
	// BodyContains, when set, must appear within the first BodyLimit bytes of
	// the response.
	BodyContains string
	BodyLimit    int64
}

// StatusMismatchError is a probe that reached the target and read a status
// code the probe's matcher excludes.
type StatusMismatchError struct {
	StatusCode int
}

func (e *StatusMismatchError) Error() string {
	return fmt.Sprintf("health check returned HTTP %d, which its success codes exclude", e.StatusCode)
}

// BodyMismatchError is a probe whose response did not contain the text the
// probe expects.
type BodyMismatchError struct {
	StatusCode int
	Want       string
}

func (e *BodyMismatchError) Error() string {
	return fmt.Sprintf("health check response did not contain %q", e.Want)
}

// ProbeHTTP issues one GET and grades the status the target itself returned: a
// health checker never follows a redirect, so a 3xx is judged by its own code.
// It returns the status code read, or 0 when the target gave no answer.
func ProbeHTTP(ctx context.Context, probe HTTPProbe) (int, error) {
	if probe.Timeout <= 0 {
		return 0, fmt.Errorf("a health check needs a timeout")
	}
	path := probe.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	ctx, cancel := context.WithTimeout(ctx, probe.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.Scheme+"://"+probe.Address+path, nil)
	if err != nil {
		return 0, err
	}
	for name, values := range probe.Header {
		req.Header[name] = append([]string(nil), values...)
	}
	if probe.Host != "" {
		req.Host = probe.Host
	}
	connect := probe.ConnectTimeout
	if connect <= 0 {
		connect = probe.Timeout
	}
	// A health checker opens its own connection for each check, straight to
	// the target: never through a proxy, never reusing one a request opened.
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: connect}).DialContext,
		TLSHandshakeTimeout:   probe.Timeout,
		ResponseHeaderTimeout: probe.ResponseTimeout,
		DisableKeepAlives:     true,
		TLSClientConfig: &tls.Config{
			ServerName:         probe.ServerName,
			InsecureSkipVerify: !probe.VerifyCertificate, //nolint:gosec // a health checker that does not validate its targets accepts any certificate
		},
	}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Transport:     transport,
		Timeout:       probe.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if !probe.Match.Matches(resp.StatusCode) {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, &StatusMismatchError{StatusCode: resp.StatusCode}
	}
	if probe.BodyContains == "" {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, probe.BodyLimit))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read health check response: %w", err)
	}
	if !bytes.Contains(payload, []byte(probe.BodyContains)) {
		return resp.StatusCode, &BodyMismatchError{StatusCode: resp.StatusCode, Want: probe.BodyContains}
	}
	return resp.StatusCode, nil
}

// ProbeTCP is a connection health check: it succeeds when the target accepts
// a connection within timeout.
func ProbeTCP(ctx context.Context, address string, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("a health check needs a timeout")
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// ProbeGRPC is a gRPC health check: it calls the target's
// grpc.health.v1.Health/Check for service, the empty name standing for the
// server as a whole, and succeeds when the target reports it SERVING.
func ProbeGRPC(ctx context.Context, address, service string, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("a health check needs a timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		return fmt.Errorf("gRPC health check: %w", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("gRPC health check: service %q is %s", service, resp.GetStatus())
	}
	return nil
}
