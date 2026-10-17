package lbplane

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// Matches reports whether code is one of the matcher's codes.
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
	Address           string
	// Host is the Host header the probe sends; empty sends Address.
	Host    string
	Path    string
	Timeout time.Duration
	Match   StatusMatcher
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
	if path == "" {
		path = "/"
	}
	ctx, cancel := context.WithTimeout(ctx, probe.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.Scheme+"://"+probe.Address+path, nil)
	if err != nil {
		return 0, err
	}
	if probe.Host != "" {
		req.Host = probe.Host
	}
	client := http.Client{
		Timeout:       probe.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if strings.EqualFold(probe.Scheme, "https") && !probe.VerifyCertificate {
		client.Transport = unverifiedTransport
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
