package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

// registerGCPComputeLoadBalancerDataPlane mounts the front end a forwarding
// rule's address answers on. It claims every path, so it is addressed by Host
// rather than by path, and it carries no Google access token — a client reaching
// a load balancer is reaching the workload behind it, not a Google API. A Host
// that names no forwarding rule is not found.
func registerGCPComputeLoadBalancerDataPlane(srv *sim.Server) {
	srv.HandleFunc("/{path...}", func(w http.ResponseWriter, r *http.Request) {
		fr, ok := gcpForwardingRuleForRequest(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		handleGCPComputeLoadBalancerDataPlane(w, r, fr)
	})
}

// startGCPHealthChecker runs the backend health checker for the lifetime of
// the simulator.
func startGCPHealthChecker(srv *sim.Server) {
	srv.StartBackground("Compute Engine health checker", func(ctx context.Context) {
		lbplane.SweepEvery(ctx, gcpHealthCheckSweep, gcpSweepBackendHealth)
	})
}

// gcpForwardingRulesByAddress indexes forwarding rules by the IP address they
// answer on. The data plane claims every path, so this lookup runs for every
// request no other handler matched.
var gcpForwardingRulesByAddress sim.GenerationIndex[ComputeForwardingRule]

// gcpForwardingRuleForRequest finds the forwarding rule whose address and port
// range the request was sent to. Several rules may share one address on
// different ports, so the port picks among them.
func gcpForwardingRuleForRequest(r *http.Request) (ComputeForwardingRule, bool) {
	if gcpForwardingRules == nil {
		return ComputeForwardingRule{}, false
	}
	hostname := lbplane.Hostname(r.Host)
	if hostname == "" {
		return ComputeForwardingRule{}, false
	}
	for _, fr := range gcpForwardingRulesByAddress.LookupAll(gcpForwardingRules, hostname,
		func(fr ComputeForwardingRule) []string { return []string{lbplane.Hostname(fr.IPAddress)} }) {
		if gcpForwardingRuleMatchesRequest(fr, r) {
			return fr, true
		}
	}
	return ComputeForwardingRule{}, false
}

func handleGCPComputeLoadBalancerDataPlane(w http.ResponseWriter, r *http.Request, fr ComputeForwardingRule) {
	bs, ok := gcpBackendServiceForForwardingRule(fr, r)
	if !ok {
		http.Error(w, "no backend service", http.StatusServiceUnavailable)
		return
	}
	target, ok := gcpHealthyBackendTarget(bs)
	if !ok {
		http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
		return
	}
	scheme := "http"
	if strings.EqualFold(bs.Protocol, "HTTPS") || strings.EqualFold(bs.Protocol, "HTTP2") {
		scheme = "https"
	}
	// The load balancer sends the client's own Host header, and does not
	// validate the certificate an HTTPS backend presents.
	err := lbplane.Forward(w, r, lbplane.Upstream{
		Scheme:                 scheme,
		Address:                target.Address,
		Path:                   r.URL.EscapedPath(),
		RawQuery:               r.URL.RawQuery,
		Timeout:                time.Duration(gcpDefaultBackendTimeout(bs.TimeoutSec)) * time.Second,
		SkipTargetVerification: true,
	})
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func gcpForwardingRuleMatchesRequest(fr ComputeForwardingRule, r *http.Request) bool {
	port := 80
	if _, p, err := net.SplitHostPort(r.Host); err == nil {
		if parsed, perr := strconv.Atoi(p); perr == nil {
			port = parsed
		}
	}
	if fr.PortRange == "" {
		return port == 80
	}
	from, to, err := realexec.PortRange(fr.PortRange)
	if err != nil {
		return false
	}
	if from == 0 && to == 0 {
		return true
	}
	if to == 0 {
		to = from
	}
	return port >= from && port <= to
}

func gcpBackendServiceForForwardingRule(fr ComputeForwardingRule, r *http.Request) (ComputeBackendService, bool) {
	if gcpTargetHTTPProxies == nil || gcpURLMaps == nil || gcpBackendServices == nil {
		return ComputeBackendService{}, false
	}
	proxy, ok := gcpTargetHTTPProxies.Get(strings.TrimPrefix(fr.Target, "https://www.googleapis.com/compute/v1/"))
	if !ok {
		return ComputeBackendService{}, false
	}
	urlMap, ok := gcpURLMaps.Get(strings.TrimPrefix(proxy.UrlMap, "https://www.googleapis.com/compute/v1/"))
	if !ok {
		return ComputeBackendService{}, false
	}
	service := gcpURLMapServiceForRequest(urlMap, r)
	service = strings.TrimPrefix(service, "https://www.googleapis.com/compute/v1/")
	return gcpBackendServices.Get(service)
}

func gcpURLMapServiceForRequest(urlMap ComputeURLMap, r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return gcpURLMapService(urlMap, host, r.URL.Path)
}

// gcpURLMapService resolves a host and path through a URL map to the backend
// service that serves it. The data plane routes with it and urlMaps.validate
// checks a map's tests with it, so a test passes exactly when the request it
// describes would reach the service it names.
func gcpURLMapService(urlMap ComputeURLMap, host, path string) string {
	for _, hostRule := range urlMap.HostRules {
		if !gcpURLMapHostMatches(hostRule.Hosts, host) {
			continue
		}
		for _, matcher := range urlMap.PathMatchers {
			if matcher.Name != hostRule.PathMatcher {
				continue
			}
			for _, pathRule := range matcher.PathRules {
				if gcpURLMapPathMatches(pathRule.Paths, path) && pathRule.Service != "" {
					return pathRule.Service
				}
			}
			if matcher.DefaultService != "" {
				return matcher.DefaultService
			}
		}
	}
	return urlMap.DefaultService
}

func gcpURLMapHostMatches(patterns []string, host string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || strings.EqualFold(pattern, host) {
			return true
		}
		if strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, strings.TrimPrefix(pattern, "*")) {
			return true
		}
	}
	return false
}

func gcpURLMapPathMatches(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if pattern == path {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(path, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

type gcpLBTarget struct {
	Instance ComputeInstance
	Group    string
	Address  string
	Port     int64
}

func gcpBackendTargets(bs ComputeBackendService) []gcpLBTarget {
	if gcpInstanceGroups == nil || gcpInstances == nil {
		return nil
	}
	var targets []gcpLBTarget
	for _, backend := range bs.Backends {
		group, ok := gcpInstanceGroups.Get(strings.TrimPrefix(backend.Group, "https://www.googleapis.com/compute/v1/"))
		if !ok {
			continue
		}
		port := gcpInstanceGroupNamedPort(group, bs.PortName)
		if port == 0 {
			port = 80
		}
		for _, member := range group.Instances {
			inst, ok := gcpInstances.Get(strings.TrimPrefix(member.Instance, "https://www.googleapis.com/compute/v1/"))
			if !ok || len(inst.NetworkInterfaces) == 0 {
				continue
			}
			ip := inst.NetworkInterfaces[0].NetworkIP
			if ip == "" {
				continue
			}
			targets = append(targets, gcpLBTarget{
				Instance: inst,
				Group:    group.SelfLink,
				Address:  net.JoinHostPort(ip, strconv.FormatInt(port, 10)),
				Port:     port,
			})
		}
	}
	return targets
}

func gcpInstanceGroupNamedPort(group storedComputeInstanceGroup, name string) int64 {
	for _, port := range group.NamedPorts {
		if port.Name == name {
			return port.Port
		}
	}
	return 0
}

// gcpHealthCheckSweep is how often the checker looks for backends whose next
// probe has come due; each health check's own checkIntervalSec sets how often
// one backend is probed.
const gcpHealthCheckSweep = 250 * time.Millisecond

// gcpBackendHealth holds what the health checker last recorded for each
// backend of each backend service, keyed by gcpBackendHealthKey.
var gcpBackendHealth = lbplane.NewHealthTracker[string]()

func gcpBackendHealthKey(bs ComputeBackendService, target gcpLBTarget) string {
	return bs.SelfLink + "|" + target.Instance.SelfLink + "|" + strconv.FormatInt(target.Port, 10)
}

// gcpBackendServiceHealthChecks resolves the health checks a backend service
// names. A reference to a health check that no longer exists resolves to
// nothing, which leaves the backend without a check that could pass.
func gcpBackendServiceHealthChecks(bs ComputeBackendService) ([]ComputeHealthCheck, bool) {
	if gcpHealthChecks == nil {
		return nil, false
	}
	checks := make([]ComputeHealthCheck, 0, len(bs.HealthChecks))
	for _, ref := range bs.HealthChecks {
		hc, ok := gcpHealthChecks.Get(strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/"))
		if !ok {
			return nil, false
		}
		checks = append(checks, hc)
	}
	return checks, true
}

// gcpHealthPolicy is a health check's schedule in its own terms: a backend is
// probed every checkIntervalSec, enters service after healthyThreshold
// consecutive successes, and leaves it after unhealthyThreshold consecutive
// failures.
func gcpHealthPolicy(hc ComputeHealthCheck) lbplane.HealthPolicy {
	interval := time.Duration(hc.CheckIntervalSec) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	healthy := int(hc.HealthyThreshold)
	if healthy <= 0 {
		healthy = 2
	}
	unhealthy := int(hc.UnhealthyThreshold)
	if unhealthy <= 0 {
		unhealthy = 2
	}
	return lbplane.HealthPolicy{
		Interval:                interval,
		InitialHealthyThreshold: healthy,
		HealthyThreshold:        healthy,
		UnhealthyThreshold:      unhealthy,
	}
}

// gcpSweepBackendHealth probes every backend of every backend service with a
// health check whose next probe has come due at now.
func gcpSweepBackendHealth(ctx context.Context, now time.Time) {
	if gcpBackendServices == nil {
		return
	}
	var targets []lbplane.HealthTarget[string]
	for _, bs := range gcpBackendServices.List() {
		checks, ok := gcpBackendServiceHealthChecks(bs)
		if !ok || len(checks) == 0 {
			continue
		}
		policy := gcpHealthPolicy(checks[0])
		for _, target := range gcpBackendTargets(bs) {
			targets = append(targets, lbplane.HealthTarget[string]{
				Key:    gcpBackendHealthKey(bs, target),
				Policy: policy,
				Probe: func(ctx context.Context) (int, error) {
					var status int
					for _, hc := range checks {
						var err error
						if status, err = gcpProbeBackend(ctx, hc, target); err != nil {
							return status, err
						}
					}
					return status, nil
				},
			})
		}
	}
	gcpBackendHealth.Sweep(ctx, now, targets)
}

// gcpProbeBackend runs one probe of a health check against one backend. An
// HTTP, HTTPS or HTTP/2 health check succeeds on a 200 response alone — the
// prober does not follow a redirect — whose first 1,024 bytes contain the
// configured response, if one is set; a TCP health check succeeds when the
// backend accepts the connection.
func gcpProbeBackend(ctx context.Context, hc ComputeHealthCheck, target gcpLBTarget) (int, error) {
	timeout := time.Duration(hc.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ip := target.Instance.NetworkInterfaces[0].NetworkIP
	// "USE_FIXED_PORT: The port number in port is used", "USE_NAMED_PORT: The
	// portName is used", "USE_SERVING_PORT: For NetworkEndpointGroup, the port
	// specified for each network endpoint is used. For InstanceGroup, the
	// portName specified in the BackendService is used." Without a
	// specification, port wins over portName.
	port := func(fixed int64, name, specification string) string {
		named := func() int64 {
			if group, ok := gcpInstanceGroups.Get(strings.TrimPrefix(target.Group, "https://www.googleapis.com/compute/v1/")); ok {
				return gcpInstanceGroupNamedPort(group, name)
			}
			return 0
		}
		chosen := fixed
		switch {
		case specification == "USE_SERVING_PORT":
			chosen = target.Port
		case specification == "USE_NAMED_PORT", specification == "" && fixed == 0 && name != "":
			chosen = named()
		}
		if chosen == 0 {
			chosen = target.Port
		}
		return strconv.FormatInt(chosen, 10)
	}
	var scheme string
	var check *ComputeHTTPHealthCheck
	switch strings.ToUpper(hc.Type) {
	case "TCP":
		tcp := hc.TcpHealthCheck
		if tcp == nil {
			tcp = &ComputeTCPHealthCheck{}
		}
		return 0, lbplane.ProbeTCP(ctx, net.JoinHostPort(ip, port(tcp.Port, tcp.PortName, tcp.PortSpecification)), timeout)
	case "HTTPS":
		scheme, check = "https", hc.HttpsHealthCheck
	case "HTTP2":
		scheme, check = "https", hc.Http2HealthCheck
	case "HTTP", "":
		scheme, check = "http", hc.HttpHealthCheck
	default:
		return 0, fmt.Errorf("health check type %s is not probed by this load balancer", hc.Type)
	}
	if check == nil {
		check = &ComputeHTTPHealthCheck{}
	}
	// With no host set, the health check sends the IP address of the backend
	// it probes.
	return lbplane.ProbeHTTP(ctx, lbplane.HTTPProbe{
		Scheme:       scheme,
		Address:      net.JoinHostPort(ip, port(check.Port, check.PortName, check.PortSpecification)),
		Host:         check.Host,
		Path:         check.RequestPath,
		Timeout:      timeout,
		Match:        lbplane.StatusRange(http.StatusOK, http.StatusOK),
		BodyContains: check.Response,
		BodyLimit:    1024,
	})
}

// gcpBackendReceivesTraffic reports whether the load balancer may send the
// backend requests: one its health checker has found healthy, or any backend
// of a backend service that names no health check and so has no checker.
func gcpBackendReceivesTraffic(bs ComputeBackendService, target gcpLBTarget) bool {
	if len(bs.HealthChecks) == 0 {
		return true
	}
	health, _ := gcpBackendHealth.Health(gcpBackendHealthKey(bs, target))
	return health.State == lbplane.HealthHealthy
}

func gcpHealthyBackendTarget(bs ComputeBackendService) (gcpLBTarget, bool) {
	for _, target := range gcpBackendTargets(bs) {
		if gcpBackendReceivesTraffic(bs, target) {
			return target, true
		}
	}
	return gcpLBTarget{}, false
}

// gcpBackendServiceHealth reports the health the checker last recorded for the
// backends of one instance group. A backend service without a health check has
// no checker, so its backends carry no health state.
func gcpBackendServiceHealth(bs ComputeBackendService, groupRef string) []map[string]any {
	var out []map[string]any
	for _, target := range gcpBackendTargets(bs) {
		if groupRef != "" && strings.TrimPrefix(target.Group, "https://www.googleapis.com/compute/v1/") != strings.TrimPrefix(groupRef, "https://www.googleapis.com/compute/v1/") {
			continue
		}
		entry := map[string]any{
			"ipAddress": target.Instance.NetworkInterfaces[0].NetworkIP,
			"port":      target.Port,
			"instance":  target.Instance.SelfLink,
		}
		if len(bs.HealthChecks) > 0 {
			state := "UNHEALTHY"
			if gcpBackendReceivesTraffic(bs, target) {
				state = "HEALTHY"
			}
			entry["healthState"] = state
		}
		out = append(out, entry)
	}
	if out == nil {
		return []map[string]any{}
	}
	return out
}

func gcpDefaultBackendTimeout(timeout int64) int64 {
	if timeout <= 0 {
		return 30
	}
	return timeout
}
