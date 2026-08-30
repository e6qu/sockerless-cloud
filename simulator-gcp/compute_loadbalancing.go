package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// returnRedirectsToClient stops a forwarding client following a backend's
// redirect. A load balancer hands the 3xx back to the caller; it never chases
// one itself. Following it fetches the redirect TARGET and answers with that
// instead, and because the forwarding client keeps no cookie jar, any
// Set-Cookie the redirect carried is discarded on the way. That silently breaks
// every OpenID Connect sign-in behind the data plane: the browser gets a 200 at
// the callback URL with no session and no error to explain it.
//
// Go replays a redirected request only when it can rewind the body. A request
// forwarded from a server has no rewindable body, so in production 307 and 308
// happen to survive while 301, 302 and 303 — the set every OpenID Connect
// library uses — are followed. That accident is why the live symptom looked
// selective; the defect itself is not status-specific.
func returnRedirectsToClient(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func registerComputeLoadBalancing(srv *sim.Server) {
	healthChecks := sim.MakeStore[ComputeHealthCheck](srv.DB(), "compute_health_checks")
	backendServices := sim.MakeStore[ComputeBackendService](srv.DB(), "compute_backend_services")
	urlMaps := sim.MakeStore[ComputeURLMap](srv.DB(), "compute_url_maps")
	targetHTTPProxies := sim.MakeStore[ComputeTargetHTTPProxy](srv.DB(), "compute_target_http_proxies")
	forwardingRules := sim.MakeStore[ComputeForwardingRule](srv.DB(), "compute_global_forwarding_rules")
	gcpHealthChecks = healthChecks
	gcpBackendServices = backendServices
	gcpURLMaps = urlMaps
	gcpTargetHTTPProxies = targetHTTPProxies
	gcpForwardingRules = forwardingRules

	registerGCPComputeLoadBalancerDataPlane(srv)

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/healthChecks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var hc ComputeHealthCheck
		if err := sim.ReadJSON(r, &hc); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if hc.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		hc.Kind = "compute#healthCheck"
		hc.Id = computeNumericID()
		hc.SelfLink = computeGlobalLink(project, "healthChecks", hc.Name)
		hc.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if hc.Type == "" {
			if hc.TcpHealthCheck != nil {
				hc.Type = "TCP"
			} else {
				hc.Type = "HTTP"
			}
		}
		if hc.CheckIntervalSec == 0 {
			hc.CheckIntervalSec = 5
		}
		if hc.TimeoutSec == 0 {
			hc.TimeoutSec = 5
		}
		if hc.HealthyThreshold == 0 {
			hc.HealthyThreshold = 2
		}
		if hc.UnhealthyThreshold == 0 {
			hc.UnhealthyThreshold = 2
		}
		if hc.Type == "HTTP" && hc.HttpHealthCheck == nil {
			hc.HttpHealthCheck = &ComputeHTTPHealthCheck{Port: 80, RequestPath: "/", ProxyHeader: "NONE"}
		}
		if _, exists := healthChecks.Get(hc.SelfLink); computeConflict(w, exists, "healthChecks", hc.Name) {
			return
		}
		healthChecks.Put(hc.SelfLink, hc)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, hc.SelfLink, "insert"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/healthChecks/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalResource(w, r, healthChecks, "healthChecks", "health check")
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/healthChecks", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalList(w, r, healthChecks, "compute#healthCheckList")
	})
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/healthChecks/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeDeleteGlobalResource(w, r, healthChecks, "healthChecks")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var bs ComputeBackendService
		if err := sim.ReadJSON(r, &bs); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if bs.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		bs.Kind = "compute#backendService"
		bs.Id = computeNumericID()
		bs.SelfLink = computeGlobalLink(project, "backendServices", bs.Name)
		bs.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if bs.Protocol == "" {
			bs.Protocol = "HTTP"
		}
		if bs.LoadBalancingScheme == "" {
			bs.LoadBalancingScheme = "EXTERNAL"
		}
		if bs.TimeoutSec == 0 {
			bs.TimeoutSec = 30
		}
		bs.Fingerprint = computeFingerprint()
		if _, exists := backendServices.Get(bs.SelfLink); computeConflict(w, exists, "backendServices", bs.Name) {
			return
		}
		backendServices.Put(bs.SelfLink, bs)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, bs.SelfLink, "insert"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/backendServices/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalResource(w, r, backendServices, "backendServices", "backend service")
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/backendServices", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalList(w, r, backendServices, "compute#backendServiceList")
	})
	// The subset the caller may attach to a load balancer, which for the
	// project's holder is the project's own backend services. The literal
	// segment wins over the `{name}` get that would otherwise answer for it.
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/backendServices/listUsable", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalList(w, r, backendServices, "compute#usableBackendServiceList")
	})
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/backendServices/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeGlobalLink(project, "backendServices", name)
		var patch ComputeBackendService
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !backendServices.Update(selfLink, func(bs *ComputeBackendService) {
			if patch.Description != "" {
				bs.Description = patch.Description
			}
			if patch.Protocol != "" {
				bs.Protocol = patch.Protocol
			}
			if patch.PortName != "" {
				bs.PortName = patch.PortName
			}
			if patch.TimeoutSec != 0 {
				bs.TimeoutSec = patch.TimeoutSec
			}
			if patch.LoadBalancingScheme != "" {
				bs.LoadBalancingScheme = patch.LoadBalancingScheme
			}
			if patch.HealthChecks != nil {
				bs.HealthChecks = patch.HealthChecks
			}
			if patch.Backends != nil {
				bs.Backends = patch.Backends
			}
		}) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backend service %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, selfLink, "patch"))
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices/{name}/getHealth", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeGlobalLink(project, "backendServices", name)
		bs, ok := backendServices.Get(selfLink)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backend service %q not found", name)
			return
		}
		var req struct {
			Group string `json:"group"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":         "compute#backendServiceGroupHealth",
			"healthStatus": gcpBackendServiceHealth(r.Context(), bs, req.Group),
		})
	})
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/backendServices/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeDeleteGlobalResource(w, r, backendServices, "backendServices")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/urlMaps", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var um ComputeURLMap
		if err := sim.ReadJSON(r, &um); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if um.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		um.Kind = "compute#urlMap"
		um.Id = computeNumericID()
		um.SelfLink = computeGlobalLink(project, "urlMaps", um.Name)
		um.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		um.Fingerprint = computeFingerprint()
		if _, exists := urlMaps.Get(um.SelfLink); computeConflict(w, exists, "urlMaps", um.Name) {
			return
		}
		urlMaps.Put(um.SelfLink, um)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, um.SelfLink, "insert"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/urlMaps/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalResource(w, r, urlMaps, "urlMaps", "URL map")
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/urlMaps", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalList(w, r, urlMaps, "compute#urlMapList")
	})
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/urlMaps/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeDeleteGlobalResource(w, r, urlMaps, "urlMaps")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/targetHttpProxies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var proxy ComputeTargetHTTPProxy
		if err := sim.ReadJSON(r, &proxy); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if proxy.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		proxy.Kind = "compute#targetHttpProxy"
		proxy.Id = computeNumericID()
		proxy.SelfLink = computeGlobalLink(project, "targetHttpProxies", proxy.Name)
		proxy.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if _, exists := targetHTTPProxies.Get(proxy.SelfLink); computeConflict(w, exists, "targetHttpProxies", proxy.Name) {
			return
		}
		targetHTTPProxies.Put(proxy.SelfLink, proxy)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, proxy.SelfLink, "insert"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/targetHttpProxies/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalResource(w, r, targetHTTPProxies, "targetHttpProxies", "target HTTP proxy")
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/targetHttpProxies", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalList(w, r, targetHTTPProxies, "compute#targetHttpProxyList")
	})
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/targetHttpProxies/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeDeleteGlobalResource(w, r, targetHTTPProxies, "targetHttpProxies")
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/forwardingRules", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var fr ComputeForwardingRule
		if err := sim.ReadJSON(r, &fr); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if fr.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		fr.Kind = "compute#forwardingRule"
		fr.Id = computeNumericID()
		fr.SelfLink = computeGlobalLink(project, "forwardingRules", fr.Name)
		fr.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if _, exists := forwardingRules.Get(fr.SelfLink); computeConflict(w, exists, "forwardingRules", fr.Name) {
			return
		}
		if fr.IPAddress == "" {
			ip, err := realexec.ReserveGCPPublicIPv4(fr.SelfLink, nil)
			if err != nil {
				sim.GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to reserve real GCP public IPv4 lease: %v", err)
				return
			}
			fr.IPAddress = ip.String()
		}
		if fr.IPProtocol == "" {
			fr.IPProtocol = "TCP"
		}
		if fr.LoadBalancingScheme == "" {
			fr.LoadBalancingScheme = "EXTERNAL"
		}
		if fr.NetworkTier == "" {
			fr.NetworkTier = "PREMIUM"
		}
		forwardingRules.Put(fr.SelfLink, fr)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, fr.SelfLink, "insert"))
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/forwardingRules/{name}", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalResource(w, r, forwardingRules, "forwardingRules", "forwarding rule")
	})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/forwardingRules", func(w http.ResponseWriter, r *http.Request) {
		computeWriteGlobalList(w, r, forwardingRules, "compute#forwardingRuleList")
	})
	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/forwardingRules/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeGlobalLink(project, "forwardingRules", name)
		fr, ok := forwardingRules.Get(selfLink)
		if computeNotFound(w, ok, "forwardingRules", name) {
			return
		}
		realexec.ReleasePublicIPv4(net.ParseIP(fr.IPAddress))
		forwardingRules.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, selfLink, "delete"))
	})
}

type computeNamedResource interface {
	ComputeHealthCheck | ComputeBackendService | ComputeURLMap | ComputeTargetHTTPProxy | ComputeForwardingRule
}

func computeGlobalLink(project, collection, name string) string {
	return fmt.Sprintf("projects/%s/global/%s/%s", project, collection, name)
}

func computeGlobalOp(project, target, opType string) map[string]any {
	op := newComputeOp(project, "global", target)
	op["operationType"] = opType
	return op
}

func computeWriteGlobalResource[T computeNamedResource](w http.ResponseWriter, r *http.Request, store sim.Store[T], collection, label string) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "name")
	selfLink := computeGlobalLink(project, collection, name)
	resource, ok := store.Get(selfLink)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", label, name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, resource)
}

func computeWriteGlobalList[T computeNamedResource](w http.ResponseWriter, r *http.Request, store sim.Store[T], kind string) {
	project := sim.PathParam(r, "project")
	prefix := fmt.Sprintf("projects/%s/global/", project)
	items := store.Filter(func(resource T) bool {
		return strings.HasPrefix(computeResourceSelfLink(resource), prefix)
	})
	if items == nil {
		items = []T{}
	}
	sort.Slice(items, func(i, j int) bool {
		return computeResourceSelfLink(items[i]) < computeResourceSelfLink(items[j])
	})
	page, next, ok := paginateListCompute(w, r, items)
	if !ok {
		return
	}
	resp := map[string]any{"kind": kind, "items": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func computeResourceSelfLink[T computeNamedResource](resource T) string {
	switch v := any(resource).(type) {
	case ComputeHealthCheck:
		return v.SelfLink
	case ComputeBackendService:
		return v.SelfLink
	case ComputeURLMap:
		return v.SelfLink
	case ComputeTargetHTTPProxy:
		return v.SelfLink
	case ComputeForwardingRule:
		return v.SelfLink
	default:
		return ""
	}
}

func computeDeleteGlobalResource[T computeNamedResource](w http.ResponseWriter, r *http.Request, store sim.Store[T], collection string) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, "name")
	selfLink := computeGlobalLink(project, collection, name)
	if computeNotFound(w, store.Delete(selfLink), collection, name) {
		return
	}
	sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, selfLink, "delete"))
}

// computeFingerprint returns a fresh opaque optimistic-concurrency token. Real
// GCP fingerprints change whenever the resource is mutated, so callers set a new
// one on every write; a client that PATCHes with a stale fingerprint then fails
// the precondition (see fingerprintMatches). Generated per call (not a
// constant) so each mutation invalidates the prior token.
func computeFingerprint() string {
	return strings.ReplaceAll(generateUUID(), "-", "")[:16]
}

// fingerprintMatches reports whether a client-supplied fingerprint may proceed
// against the resource's current fingerprint. An empty client fingerprint is
// permitted (the SDK omits it on first write / when the caller opts out of the
// optimistic-concurrency check); a non-empty one must equal the current value,
// exactly as real GCP gates setLabels/setMetadata/setTags/PATCH.
func fingerprintMatches(current, supplied string) bool {
	return supplied == "" || supplied == current
}

// registerGCPComputeLoadBalancerDataPlane mounts the front end a forwarding
// rule's address answers on. It claims every path, so it is addressed by Host
// rather than by path, and it carries no Google access token — a client reaching
// a load balancer is reaching the workload behind it, not a Google API. A Host
// that names no forwarding rule is not found.
func registerGCPComputeLoadBalancerDataPlane(srv *sim.Server) {
	srv.HandleFunc("/{path...}", func(w http.ResponseWriter, r *http.Request) {
		fr, ok := gcpForwardingRuleFromDataPlaneHost(r.Host)
		if !ok {
			http.NotFound(w, r)
			return
		}
		handleGCPComputeLoadBalancerDataPlane(w, r, fr)
	})
}

func gcpForwardingRuleFromDataPlaneHost(host string) (ComputeForwardingRule, bool) {
	if gcpForwardingRules == nil {
		return ComputeForwardingRule{}, false
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	for _, fr := range gcpForwardingRules.List() {
		if strings.EqualFold(fr.IPAddress, hostname) {
			return fr, true
		}
	}
	return ComputeForwardingRule{}, false
}

func handleGCPComputeLoadBalancerDataPlane(w http.ResponseWriter, r *http.Request, fr ComputeForwardingRule) {
	if !gcpForwardingRuleMatchesRequest(fr, r) {
		http.Error(w, "no matching forwarding rule port", http.StatusNotFound)
		return
	}
	bs, ok := gcpBackendServiceForForwardingRule(fr, r)
	if !ok {
		http.Error(w, "no backend service", http.StatusServiceUnavailable)
		return
	}
	target, ok := gcpHealthyBackendTarget(r.Context(), bs)
	if !ok {
		http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
		return
	}
	if err := gcpProxyHTTPRequest(w, r, bs, target); err != nil {
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
	from, to := parsePortRange(fr.PortRange)
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

func gcpHealthyBackendTarget(ctx context.Context, bs ComputeBackendService) (gcpLBTarget, bool) {
	for _, target := range gcpBackendTargets(bs) {
		if gcpProbeBackendTarget(ctx, bs, target) {
			return target, true
		}
	}
	return gcpLBTarget{}, false
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

func gcpProbeBackendTarget(ctx context.Context, bs ComputeBackendService, target gcpLBTarget) bool {
	if gcpHealthChecks == nil || len(bs.HealthChecks) == 0 {
		conn, err := net.DialTimeout("tcp", target.Address, 2*time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	for _, ref := range bs.HealthChecks {
		hc, ok := gcpHealthChecks.Get(strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/"))
		if !ok {
			return false
		}
		spec := gcpProbeSpec(hc, target)
		if err := realexec.ProbeTarget(ctx, spec); err != nil {
			return false
		}
	}
	return true
}

func gcpProbeSpec(hc ComputeHealthCheck, target gcpLBTarget) realexec.ProbeSpec {
	timeout := time.Duration(hc.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	switch strings.ToUpper(hc.Type) {
	case "TCP":
		port := target.Port
		if hc.TcpHealthCheck != nil && hc.TcpHealthCheck.Port != 0 {
			port = hc.TcpHealthCheck.Port
		}
		return realexec.ProbeSpec{Protocol: "TCP", Address: net.JoinHostPort(target.Instance.NetworkInterfaces[0].NetworkIP, strconv.FormatInt(port, 10)), Timeout: timeout}
	default:
		port := target.Port
		path := "/"
		if hc.HttpHealthCheck != nil {
			if hc.HttpHealthCheck.Port != 0 {
				port = hc.HttpHealthCheck.Port
			}
			if hc.HttpHealthCheck.RequestPath != "" {
				path = hc.HttpHealthCheck.RequestPath
			}
		}
		return realexec.ProbeSpec{Protocol: "HTTP", Address: net.JoinHostPort(target.Instance.NetworkInterfaces[0].NetworkIP, strconv.FormatInt(port, 10)), Path: path, Timeout: timeout}
	}
}

func gcpBackendServiceHealth(ctx context.Context, bs ComputeBackendService, groupRef string) []map[string]any {
	var out []map[string]any
	for _, target := range gcpBackendTargets(bs) {
		if groupRef != "" && strings.TrimPrefix(target.Group, "https://www.googleapis.com/compute/v1/") != strings.TrimPrefix(groupRef, "https://www.googleapis.com/compute/v1/") {
			continue
		}
		state := "UNHEALTHY"
		if gcpProbeBackendTarget(ctx, bs, target) {
			state = "HEALTHY"
		}
		out = append(out, map[string]any{
			"ipAddress":   target.Instance.NetworkInterfaces[0].NetworkIP,
			"port":        target.Port,
			"instance":    target.Instance.SelfLink,
			"healthState": state,
		})
	}
	if out == nil {
		return []map[string]any{}
	}
	return out
}

func gcpProxyHTTPRequest(w http.ResponseWriter, r *http.Request, bs ComputeBackendService, target gcpLBTarget) error {
	scheme := "http"
	if strings.EqualFold(bs.Protocol, "HTTPS") {
		scheme = "https"
	}
	upstreamURL := url.URL{
		Scheme:   scheme,
		Host:     target.Address,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
	}
	// The backend service timeout bounds a request/response exchange. An upgraded
	// connection is not one, and applying it there would cut every long-lived
	// session at the timeout.
	ctx := r.Context()
	upgrade := sim.IsUpgradeRequest(r)
	if !upgrade {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(gcpDefaultBackendTimeout(bs.TimeoutSec))*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return err
	}
	req.Header = r.Header.Clone()
	client := http.Client{CheckRedirect: returnRedirectsToClient}
	if !upgrade {
		client.Timeout = time.Duration(gcpDefaultBackendTimeout(bs.TimeoutSec)) * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("forward to backend %s: %w", target.Address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return sim.TunnelUpgradedResponse(w, resp)
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func gcpDefaultBackendTimeout(timeout int64) int64 {
	if timeout <= 0 {
		return 30
	}
	return timeout
}
