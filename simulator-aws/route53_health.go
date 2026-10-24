package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/lbplane"
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
	if strings.EqualFold(cfg.Type, "CLOUDWATCH_METRIC") {
		return r53AlarmHealthy(id, cfg)
	}
	state := r53HealthStateOf(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.healthy
}

// r53AlarmHealthy is a CLOUDWATCH_METRIC check's status: healthy while its
// alarm is OK, unhealthy while it is in ALARM, and InsufficientDataHealthStatus
// otherwise, where LastKnownStatus is the status the alarm last decided and
// healthy for a check it never has. A missing alarm has insufficient data.
func r53AlarmHealthy(id string, cfg R53HealthCheckConfig) bool {
	state := "INSUFFICIENT_DATA"
	if cfg.AlarmIdentifier != nil && (cfg.AlarmIdentifier.Region == "" || cfg.AlarmIdentifier.Region == awsRegion()) {
		if alarm, ok := cwAlarms.Get(cfg.AlarmIdentifier.Name); ok {
			state, _ = cwAlarmEffectiveState(alarm)
		}
	}
	known := r53HealthStateOf(id)
	known.mu.Lock()
	var healthy bool
	switch state {
	case "OK":
		healthy, known.healthy = true, true
	case "ALARM":
		healthy, known.healthy = false, false
	default:
		switch cfg.InsufficientDataHealthStatus {
		case "Unhealthy":
			healthy = false
		case "LastKnownStatus":
			healthy = known.healthy
		default:
			healthy = true
		}
	}
	known.mu.Unlock()
	if cfg.Inverted != nil && *cfg.Inverted {
		return !healthy
	}
	return healthy
}

// r53AlarmConfiguration describes the alarm a CLOUDWATCH_METRIC check names,
// or nothing when the check names none that exists.
func r53AlarmConfiguration(cfg R53HealthCheckConfig) *R53CloudWatchAlarmConfiguration {
	if !strings.EqualFold(cfg.Type, "CLOUDWATCH_METRIC") || cfg.AlarmIdentifier == nil {
		return nil
	}
	alarm, ok := cwAlarms.Get(cfg.AlarmIdentifier.Name)
	if !ok {
		return nil
	}
	out := &R53CloudWatchAlarmConfiguration{
		EvaluationPeriods:  alarm.EvaluationPeriods,
		Threshold:          alarm.Threshold,
		ComparisonOperator: alarm.ComparisonOperator,
		Period:             alarm.Period,
		MetricName:         alarm.MetricName,
		Namespace:          alarm.Namespace,
		Statistic:          alarm.Statistic,
	}
	for _, d := range alarm.Dimensions {
		out.Dimensions = append(out.Dimensions, R53Dimension(d))
	}
	return out
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
		if err := lbplane.ProbeTCP(ctx, address, r53TCPTimeout); err != nil {
			return r53ProbeResult{status: "Failure: " + r53ProbeError(err)}
		}
		return r53ProbeResult{ok: true, status: "Success: TCP connection established"}
	}

	scheme := "http"
	if secure {
		scheme = "https"
	}
	probe := lbplane.HTTPProbe{
		Scheme:          scheme,
		Address:         address,
		Host:            cfg.FullyQualifiedDomainName,
		Path:            cfg.ResourcePath,
		Timeout:         r53ConnectTimeout + r53ResponseTimeout,
		ConnectTimeout:  r53ConnectTimeout,
		ResponseTimeout: r53ResponseTimeout,
		Match:           lbplane.StatusRange(200, 399),
	}
	if cfg.EnableSNI != nil && *cfg.EnableSNI {
		probe.ServerName = cfg.FullyQualifiedDomainName
	}
	if strings.HasSuffix(kind, "_STR_MATCH") {
		probe.BodyContains, probe.BodyLimit = cfg.SearchString, r53SearchWindow
	}
	status, err := lbplane.ProbeHTTP(ctx, probe)
	code := fmt.Sprintf("HTTP Status Code %d, %s", status, http.StatusText(status))
	switch {
	case errors.As(err, new(*lbplane.StatusMismatchError)):
		return r53ProbeResult{status: "Failure: " + code}
	case errors.As(err, new(*lbplane.BodyMismatchError)):
		return r53ProbeResult{status: "Failure: the response body did not contain the search string"}
	case err != nil:
		return r53ProbeResult{status: "Failure: " + r53ProbeError(err)}
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
