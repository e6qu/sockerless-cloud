package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Route 53 health checkers probe an endpoint every RequestInterval seconds and
// change its status once FailureThreshold consecutive probes disagree with
// it. The simulator probes from where it runs, so each health check reports
// one observation, from its own Region.

const (
	r53DefaultRequestInterval  = 30
	r53DefaultFailureThreshold = 3
	// r53SearchWindow is how much of a response body a string-match check
	// searches.
	r53SearchWindow = 5120
	// r53ConnectTimeout and r53ResponseTimeout bound a probe as Route 53's
	// checkers do: a connection within four seconds, then a response within
	// two.
	r53ConnectTimeout  = 4 * time.Second
	r53ResponseTimeout = 2 * time.Second
	r53TCPTimeout      = 10 * time.Second
)

type r53HealthState struct {
	mu          sync.Mutex
	healthy     bool
	streak      int
	nextProbe   time.Time
	last        R53StatusReport
	lastFailure *R53StatusReport
}

var r53Health sync.Map // health check id -> *r53HealthState

func r53HealthStateOf(id string) *r53HealthState {
	v, _ := r53Health.LoadOrStore(id, &r53HealthState{healthy: true})
	state, _ := v.(*r53HealthState)
	return state
}

// r53HealthCheckHealthy is the status a record set's health check has now. A
// health check not yet probed counts as healthy, as a new Route 53 health
// check does until enough probes fail.
func r53HealthCheckHealthy(id string) bool {
	if id == "" {
		return true
	}
	stored, ok := r53HealthChecks.Get(id)
	if !ok {
		return true
	}
	cfg := stored.HealthCheck.HealthCheckConfig
	if cfg.Disabled != nil && *cfg.Disabled {
		return true
	}
	if strings.EqualFold(cfg.Type, "CALCULATED") {
		return r53CalculatedHealthy(cfg)
	}
	state := r53HealthStateOf(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.healthy
}

func r53CalculatedHealthy(cfg R53HealthCheckConfig) bool {
	healthy := 0
	for _, child := range cfg.ChildHealthChecks {
		if r53HealthCheckHealthy(child) {
			healthy++
		}
	}
	threshold := len(cfg.ChildHealthChecks)
	if cfg.HealthThreshold != nil {
		threshold = *cfg.HealthThreshold
	}
	result := healthy >= threshold
	if cfg.Inverted != nil && *cfg.Inverted {
		return !result
	}
	return result
}

// r53RunHealthChecks probes every endpoint health check when it falls due,
// until ctx ends.
func r53RunHealthChecks(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var probes sync.WaitGroup
	defer probes.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, stored := range r53HealthChecks.List() {
				hc := stored.HealthCheck
				if !r53ProbedType(hc.HealthCheckConfig.Type) {
					continue
				}
				state := r53HealthStateOf(hc.Id)
				state.mu.Lock()
				due := !now.Before(state.nextProbe)
				if due {
					state.nextProbe = now.Add(time.Duration(r53RequestInterval(hc.HealthCheckConfig)) * time.Second)
				}
				state.mu.Unlock()
				if !due {
					continue
				}
				probes.Add(1)
				go func() {
					defer probes.Done()
					r53Observe(hc, r53Probe(ctx, hc.HealthCheckConfig), time.Now())
				}()
			}
		}
	}
}

func r53ProbedType(t string) bool {
	switch strings.ToUpper(t) {
	case "HTTP", "HTTPS", "HTTP_STR_MATCH", "HTTPS_STR_MATCH", "TCP":
		return true
	}
	return false
}

func r53RequestInterval(cfg R53HealthCheckConfig) int {
	if cfg.RequestInterval != nil && *cfg.RequestInterval > 0 {
		return *cfg.RequestInterval
	}
	return r53DefaultRequestInterval
}

type r53ProbeResult struct {
	ok     bool
	status string
}

// r53Observe records a probe and moves the status once FailureThreshold
// consecutive probes disagree with it.
func r53Observe(hc R53HealthCheck, result r53ProbeResult, at time.Time) {
	cfg := hc.HealthCheckConfig
	passed := result.ok
	if cfg.Inverted != nil && *cfg.Inverted {
		passed = !passed
	}
	threshold := r53DefaultFailureThreshold
	if cfg.FailureThreshold != nil && *cfg.FailureThreshold > 0 {
		threshold = *cfg.FailureThreshold
	}
	report := R53StatusReport{Status: result.status, CheckedTime: at.UTC().Format(time.RFC3339)}
	state := r53HealthStateOf(hc.Id)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.last = report
	if !result.ok {
		failure := report
		state.lastFailure = &failure
	}
	if passed == state.healthy {
		state.streak = 0
		return
	}
	state.streak++
	if state.streak >= threshold {
		state.healthy = passed
		state.streak = 0
	}
}

func r53Probe(ctx context.Context, cfg R53HealthCheckConfig) r53ProbeResult {
	host := cfg.IPAddress
	if host == "" {
		host = cfg.FullyQualifiedDomainName
	}
	kind := strings.ToUpper(cfg.Type)
	secure := strings.HasPrefix(kind, "HTTPS")
	port := 80
	if secure {
		port = 443
	}
	if cfg.Port != nil {
		port = *cfg.Port
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	if kind == "TCP" {
		conn, err := (&net.Dialer{Timeout: r53TCPTimeout}).DialContext(ctx, "tcp", address)
		if err != nil {
			return r53ProbeResult{status: "Failure: " + r53ProbeError(err)}
		}
		_ = conn.Close()
		return r53ProbeResult{ok: true, status: "Success: TCP connection established"}
	}

	scheme := "http"
	if secure {
		scheme = "https"
	}
	path := cfg.ResourcePath
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+address+path, nil)
	if err != nil {
		return r53ProbeResult{status: "Failure: " + err.Error()}
	}
	if cfg.FullyQualifiedDomainName != "" {
		req.Host = cfg.FullyQualifiedDomainName
	}
	serverName := ""
	if cfg.EnableSNI != nil && *cfg.EnableSNI {
		serverName = cfg.FullyQualifiedDomainName
	}
	client := &http.Client{
		Timeout: r53ConnectTimeout + r53ResponseTimeout,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: r53ConnectTimeout}).DialContext,
			ResponseHeaderTimeout: r53ResponseTimeout,
			// Route 53 checkers do not validate the endpoint's certificate.
			TLSClientConfig: &tls.Config{ServerName: serverName, InsecureSkipVerify: true}, //nolint:gosec
			Proxy:           nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return r53ProbeResult{status: "Failure: " + r53ProbeError(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	code := fmt.Sprintf("HTTP Status Code %d, %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return r53ProbeResult{status: "Failure: " + code}
	}
	if strings.HasSuffix(kind, "_STR_MATCH") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, r53SearchWindow))
		if err != nil {
			return r53ProbeResult{status: "Failure: " + r53ProbeError(err)}
		}
		if !bytes.Contains(body, []byte(cfg.SearchString)) {
			return r53ProbeResult{status: "Failure: the response body did not contain the search string"}
		}
	}
	return r53ProbeResult{ok: true, status: "Success: " + code}
}

func r53ProbeError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "Connection timed out"
	}
	return err.Error()
}

// r53HealthObservations is what GetHealthCheckStatus reports: the latest
// probe, from the Region the simulator probes from.
func r53HealthObservations(id string, failuresOnly bool) []R53HealthCheckObservation {
	state := r53HealthStateOf(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	report := state.last
	if failuresOnly {
		if state.lastFailure == nil {
			return []R53HealthCheckObservation{}
		}
		report = *state.lastFailure
	}
	if report.CheckedTime == "" {
		return []R53HealthCheckObservation{}
	}
	return []R53HealthCheckObservation{{Region: awsRegion(), StatusReport: report}}
}
