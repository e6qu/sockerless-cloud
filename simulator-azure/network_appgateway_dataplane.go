package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

// The application gateway data plane. A client reaches an application gateway
// at the address of one of its frontend IP configurations — the address of the
// public IP the frontend references, or the private address the frontend took
// out of its subnet — exactly as it reaches a real one, and the simulator
// dispatches on that address in the HTTP Host header the same way it dispatches
// the load balancer, Container Apps ingress and storage data planes. The
// coordinate a client points its TCP connection at is the simulator's endpoint;
// everything above it is the gateway's own contract.
//
// What runs here is the gateway's configuration, executed:
//
//   - the HTTP listener is selected by frontend address, port and host name,
//     with a multi-site listener winning over a basic one;
//   - the request routing rule bound to that listener decides where the request
//     goes — straight at a backend address pool, through a URL path map whose
//     path rules pick a different pool per path, or into a redirect
//     configuration that answers the client itself;
//   - the rewrite rule set attached to the rule (or to the matched path rule)
//     rewrites request headers, response headers and the request URL, with each
//     rule's conditions evaluated against the request's own server variables;
//   - the backend settings decide the backend protocol, port, host header, path
//     prefix and request timeout;
//   - and the pool member the request is forwarded to is one the gateway's
//     probes have found Up, using the probe the settings reference or the
//     default probe Azure applies when they reference none.
//
// BackendHealth answers from those same probes, which run on each probe's own
// interval; an on-demand probe runs when it is asked for.

func registerApplicationGatewayDataPlane(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gw, frontend, ok := applicationGatewayFromDataPlaneHost(r.Host)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			handleApplicationGatewayDataPlane(w, r, gw, frontend)
		})
	})
}

// applicationGatewayFromDataPlaneHost finds the running application gateway
// whose frontend holds the address the request was sent to. A stopped gateway
// holds no frontend: its data plane is torn down until it is started again, so
// the address answers nothing.
func applicationGatewayFromDataPlaneHost(host string) (ApplicationGateway, ApplicationGatewayFrontendIPConfiguration, bool) {
	if azureApplicationGateways == nil {
		return ApplicationGateway{}, ApplicationGatewayFrontendIPConfiguration{}, false
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if hostname == "" {
		return ApplicationGateway{}, ApplicationGatewayFrontendIPConfiguration{}, false
	}
	// Every request into the simulator reaches this wrapper before any
	// handler, so the address match answers from an index rather than by
	// reading and decoding every stored gateway. Operational state is checked
	// on the matched row rather than in the index, so a gateway that starts or
	// stops needs no invalidation beyond the store's own generation.
	for _, gw := range azureApplicationGatewaysByFrontend.LookupAll(
		azureApplicationGateways, hostname, applicationGatewayFrontendAddresses) {
		if !strings.EqualFold(gw.Properties.OperationalState, "Running") {
			continue
		}
		for _, fe := range gw.Properties.FrontendIPConfigurations {
			if !strings.EqualFold(applicationGatewayFrontendAddress(fe), hostname) {
				continue
			}
			// The pool members an interface declared are recomputed on read, so
			// the data plane forwards to the same set the gateway reports.
			projectApplicationGateway(&gw)
			return gw, fe, true
		}
	}
	return ApplicationGateway{}, ApplicationGatewayFrontendIPConfiguration{}, false
}

// azureApplicationGatewaysByFrontend indexes gateways by the addresses their
// frontends answer on.
var azureApplicationGatewaysByFrontend sim.GenerationIndex[ApplicationGateway]

// applicationGatewayFrontendAddresses returns every address a gateway's
// frontends answer on, whatever its operational state.
func applicationGatewayFrontendAddresses(gw ApplicationGateway) []string {
	addresses := make([]string, 0, len(gw.Properties.FrontendIPConfigurations))
	for _, fe := range gw.Properties.FrontendIPConfigurations {
		addresses = append(addresses, strings.ToLower(applicationGatewayFrontendAddress(fe)))
	}
	return addresses
}

// applicationGatewayFrontendAddress reports the address clients reach a
// frontend at: the address of the public IP it references, or the private
// address it holds.
func applicationGatewayFrontendAddress(fe ApplicationGatewayFrontendIPConfiguration) string {
	if fe.Properties.PublicIPAddress != nil && fe.Properties.PublicIPAddress.ID != "" {
		if azurePublicIPs == nil {
			return ""
		}
		pip, ok := azurePublicIPs.Get(fe.Properties.PublicIPAddress.ID)
		if !ok {
			return ""
		}
		return pip.Properties.PublicIPAddress
	}
	return fe.Properties.PrivateIPAddress
}

func handleApplicationGatewayDataPlane(w http.ResponseWriter, r *http.Request, gw ApplicationGateway, frontend ApplicationGatewayFrontendIPConfiguration) {
	listener, ok := applicationGatewayListenerForRequest(gw, frontend, r)
	if !ok {
		http.Error(w, "no matching application gateway listener", http.StatusNotFound)
		return
	}
	rule, ok := applicationGatewayRuleForListener(gw, listener)
	if !ok {
		http.Error(w, "no request routing rule is bound to this listener", http.StatusNotFound)
		return
	}
	target := applicationGatewayRouteRequest(gw, rule, r)
	if target.redirect != nil {
		applicationGatewayRedirect(w, r, gw, *target.redirect)
		return
	}
	if target.pool == nil || target.settings == nil {
		http.Error(w, "the matched routing rule names no backend", http.StatusBadGateway)
		return
	}
	server, ok := applicationGatewayHealthyServer(gw, *target.pool, *target.settings)
	if !ok {
		// A gateway with no healthy pool member answers 502, the status a real
		// Application Gateway returns when it cannot reach any backend server.
		http.Error(w, "no healthy backend servers in pool "+target.pool.Name, http.StatusBadGateway)
		return
	}
	err := applicationGatewayForward(w, r, *target.settings, target.rewrite, server)
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

// applicationGatewayListenerForRequest picks the HTTP listener the request
// arrived on. Listeners are matched on the frontend address and port; among
// those, a listener that names host names wins over one that names none — the
// multi-site precedence a real gateway applies — and a literal host name wins
// over a wildcard.
func applicationGatewayListenerForRequest(gw ApplicationGateway, frontend ApplicationGatewayFrontendIPConfiguration, r *http.Request) (ApplicationGatewayHTTPListener, bool) {
	port := applicationGatewayRequestPort(r)
	requestHost := applicationGatewayRequestHostname(r)
	var basic *ApplicationGatewayHTTPListener
	var wildcard *ApplicationGatewayHTTPListener
	for i := range gw.Properties.HTTPListeners {
		listener := &gw.Properties.HTTPListeners[i]
		if listener.Properties.FrontendIPConfiguration == nil ||
			!strings.EqualFold(listener.Properties.FrontendIPConfiguration.ID, frontend.ID) {
			continue
		}
		if applicationGatewayFrontendPortValue(gw, listener.Properties.FrontendPort) != port {
			continue
		}
		names := applicationGatewayListenerHostNames(*listener)
		if len(names) == 0 {
			if basic == nil {
				basic = listener
			}
			continue
		}
		for _, name := range names {
			if strings.EqualFold(name, requestHost) {
				return *listener, true
			}
			if applicationGatewayHostMatches(name, requestHost) && wildcard == nil {
				wildcard = listener
			}
		}
	}
	if wildcard != nil {
		return *wildcard, true
	}
	if basic != nil {
		return *basic, true
	}
	return ApplicationGatewayHTTPListener{}, false
}

// applicationGatewayListenerHostNames merges the singular hostName and the
// hostNames list a listener may carry.
func applicationGatewayListenerHostNames(listener ApplicationGatewayHTTPListener) []string {
	names := append([]string{}, listener.Properties.HostNames...)
	if listener.Properties.HostName != "" {
		names = append(names, listener.Properties.HostName)
	}
	return names
}

// applicationGatewayHostMatches applies the host-name matching a listener
// performs: an exact name, or a name with a single "*" standing for any run of
// characters (Azure allows a leading or trailing wildcard).
func applicationGatewayHostMatches(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	if !strings.Contains(pattern, "*") {
		return pattern == host
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	return len(host) >= len(prefix)+len(suffix) &&
		strings.HasPrefix(host, prefix) && strings.HasSuffix(host, suffix)
}

func applicationGatewayRequestPort(r *http.Request) int32 {
	if _, p, err := net.SplitHostPort(r.Host); err == nil {
		if parsed, perr := strconv.Atoi(p); perr == nil {
			return int32(parsed)
		}
	}
	if r.TLS != nil {
		return 443
	}
	return 80
}

func applicationGatewayRequestHostname(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

func applicationGatewayFrontendPortValue(gw ApplicationGateway, ref *SubResource) int32 {
	if ref == nil {
		return 0
	}
	for _, fp := range gw.Properties.FrontendPorts {
		if strings.EqualFold(fp.ID, ref.ID) {
			return fp.Properties.Port
		}
	}
	return 0
}

// applicationGatewayRuleForListener returns the request routing rule bound to
// the listener. When several are, the lowest priority value wins — the order a
// real gateway evaluates rules in.
func applicationGatewayRuleForListener(gw ApplicationGateway, listener ApplicationGatewayHTTPListener) (ApplicationGatewayRequestRoutingRule, bool) {
	var bound []ApplicationGatewayRequestRoutingRule
	for _, rule := range gw.Properties.RequestRoutingRules {
		if rule.Properties.HTTPListener != nil && strings.EqualFold(rule.Properties.HTTPListener.ID, listener.ID) {
			bound = append(bound, rule)
		}
	}
	if len(bound) == 0 {
		return ApplicationGatewayRequestRoutingRule{}, false
	}
	sort.SliceStable(bound, func(i, j int) bool {
		return bound[i].Properties.Priority < bound[j].Properties.Priority
	})
	return bound[0], true
}

// applicationGatewayTarget is where one request ends up: a redirect the gateway
// answers itself, or a pool plus the settings that describe how to reach it,
// together with the rewrite rule set that applies.
type applicationGatewayTarget struct {
	pool     *ApplicationGatewayBackendAddressPool
	settings *ApplicationGatewayBackendHTTPSettings
	redirect *ApplicationGatewayRedirectConfiguration
	rewrite  *ApplicationGatewayRewriteRuleSet
}

// applicationGatewayRouteRequest evaluates one routing rule against the
// request. A path-based rule consults its URL path map first: the first path
// rule whose patterns match the request path decides, and the map's defaults
// apply when none does.
func applicationGatewayRouteRequest(gw ApplicationGateway, rule ApplicationGatewayRequestRoutingRule, r *http.Request) applicationGatewayTarget {
	target := applicationGatewayTarget{
		pool:     applicationGatewayPool(gw, rule.Properties.BackendAddressPool),
		settings: applicationGatewaySettings(gw, rule.Properties.BackendHTTPSettings),
		redirect: applicationGatewayRedirectConfig(gw, rule.Properties.RedirectConfiguration),
		rewrite:  applicationGatewayRewriteSet(gw, rule.Properties.RewriteRuleSet),
	}
	if !strings.EqualFold(rule.Properties.RuleType, "PathBasedRouting") || rule.Properties.URLPathMap == nil {
		return target
	}
	pathMap, ok := applicationGatewayPathMap(gw, rule.Properties.URLPathMap)
	if !ok {
		return target
	}
	if matched, ok := applicationGatewayMatchPathRule(pathMap, r.URL.Path); ok {
		return applicationGatewayTarget{
			pool:     applicationGatewayPool(gw, matched.Properties.BackendAddressPool),
			settings: applicationGatewaySettings(gw, matched.Properties.BackendHTTPSettings),
			redirect: applicationGatewayRedirectConfig(gw, matched.Properties.RedirectConfiguration),
			rewrite:  applicationGatewayRewriteSet(gw, matched.Properties.RewriteRuleSet),
		}
	}
	return applicationGatewayTarget{
		pool:     applicationGatewayPool(gw, pathMap.Properties.DefaultBackendAddressPool),
		settings: applicationGatewaySettings(gw, pathMap.Properties.DefaultBackendHTTPSettings),
		redirect: applicationGatewayRedirectConfig(gw, pathMap.Properties.DefaultRedirectConfiguration),
		rewrite:  applicationGatewayRewriteSet(gw, pathMap.Properties.DefaultRewriteRuleSet),
	}
}

// applicationGatewayMatchPathRule applies the path patterns of a URL path map.
// A pattern is either an exact path or a path ending in "*", which matches
// every path with that prefix; the rules are evaluated in their declared order
// and the first match wins.
func applicationGatewayMatchPathRule(pathMap ApplicationGatewayURLPathMap, path string) (ApplicationGatewayPathRule, bool) {
	for _, rule := range pathMap.Properties.PathRules {
		for _, pattern := range rule.Properties.Paths {
			if applicationGatewayPathMatches(pattern, path) {
				return rule, true
			}
		}
	}
	return ApplicationGatewayPathRule{}, false
}

func applicationGatewayPathMatches(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == path
}

func applicationGatewayPool(gw ApplicationGateway, ref *SubResource) *ApplicationGatewayBackendAddressPool {
	if ref == nil {
		return nil
	}
	for i := range gw.Properties.BackendAddressPools {
		if strings.EqualFold(gw.Properties.BackendAddressPools[i].ID, ref.ID) {
			return &gw.Properties.BackendAddressPools[i]
		}
	}
	return nil
}

func applicationGatewaySettings(gw ApplicationGateway, ref *SubResource) *ApplicationGatewayBackendHTTPSettings {
	if ref == nil {
		return nil
	}
	for i := range gw.Properties.BackendHTTPSettingsCollection {
		if strings.EqualFold(gw.Properties.BackendHTTPSettingsCollection[i].ID, ref.ID) {
			return &gw.Properties.BackendHTTPSettingsCollection[i]
		}
	}
	return nil
}

func applicationGatewayRedirectConfig(gw ApplicationGateway, ref *SubResource) *ApplicationGatewayRedirectConfiguration {
	if ref == nil {
		return nil
	}
	for i := range gw.Properties.RedirectConfigurations {
		if strings.EqualFold(gw.Properties.RedirectConfigurations[i].ID, ref.ID) {
			return &gw.Properties.RedirectConfigurations[i]
		}
	}
	return nil
}

func applicationGatewayRewriteSet(gw ApplicationGateway, ref *SubResource) *ApplicationGatewayRewriteRuleSet {
	if ref == nil {
		return nil
	}
	for i := range gw.Properties.RewriteRuleSets {
		if strings.EqualFold(gw.Properties.RewriteRuleSets[i].ID, ref.ID) {
			return &gw.Properties.RewriteRuleSets[i]
		}
	}
	return nil
}

func applicationGatewayPathMap(gw ApplicationGateway, ref *SubResource) (ApplicationGatewayURLPathMap, bool) {
	for _, m := range gw.Properties.URLPathMaps {
		if strings.EqualFold(m.ID, ref.ID) {
			return m, true
		}
	}
	return ApplicationGatewayURLPathMap{}, false
}

func applicationGatewayProbe(gw ApplicationGateway, ref *SubResource) *ApplicationGatewayProbe {
	if ref == nil {
		return nil
	}
	for i := range gw.Properties.Probes {
		if strings.EqualFold(gw.Properties.Probes[i].ID, ref.ID) {
			return &gw.Properties.Probes[i]
		}
	}
	return nil
}

// applicationGatewayRedirect answers the client from a redirect configuration.
// The target is either another listener of the same gateway — whose host name
// and port the gateway resolves — or a literal URL, and the incoming path and
// query are carried over when the configuration says so.
func applicationGatewayRedirect(w http.ResponseWriter, r *http.Request, gw ApplicationGateway, cfg ApplicationGatewayRedirectConfiguration) {
	target := cfg.Properties.TargetURL
	if target == "" && cfg.Properties.TargetListener != nil {
		target = applicationGatewayListenerURL(gw, cfg.Properties.TargetListener.ID)
	}
	if target == "" {
		http.Error(w, "redirect configuration "+cfg.Name+" names no target", http.StatusBadGateway)
		return
	}
	if cfg.Properties.IncludePath == nil || *cfg.Properties.IncludePath {
		target = strings.TrimSuffix(target, "/") + r.URL.EscapedPath()
	}
	if (cfg.Properties.IncludeQueryString == nil || *cfg.Properties.IncludeQueryString) && r.URL.RawQuery != "" {
		separator := "?"
		if strings.Contains(target, "?") {
			separator = "&"
		}
		target += separator + r.URL.RawQuery
	}
	w.Header().Set("Location", target)
	w.WriteHeader(applicationGatewayRedirectStatus(cfg.Properties.RedirectType))
}

// applicationGatewayRedirectStatus maps a redirect type to its status code.
func applicationGatewayRedirectStatus(redirectType string) int {
	switch strings.ToLower(redirectType) {
	case "permanent":
		return http.StatusMovedPermanently
	case "temporary":
		return http.StatusTemporaryRedirect
	case "seeother":
		return http.StatusSeeOther
	default:
		// "Found" is the redirect type Azure applies when none is named.
		return http.StatusFound
	}
}

// applicationGatewayListenerURL builds the base URL a redirect to another
// listener of the same gateway points at.
func applicationGatewayListenerURL(gw ApplicationGateway, listenerID string) string {
	for _, listener := range gw.Properties.HTTPListeners {
		if !strings.EqualFold(listener.ID, listenerID) {
			continue
		}
		scheme := "http"
		if strings.EqualFold(listener.Properties.Protocol, "Https") {
			scheme = "https"
		}
		host := ""
		if names := applicationGatewayListenerHostNames(listener); len(names) > 0 {
			host = names[0]
		}
		if host == "" {
			for _, fe := range gw.Properties.FrontendIPConfigurations {
				if listener.Properties.FrontendIPConfiguration != nil &&
					strings.EqualFold(fe.ID, listener.Properties.FrontendIPConfiguration.ID) {
					host = applicationGatewayFrontendAddress(fe)
				}
			}
		}
		if host == "" {
			return ""
		}
		port := applicationGatewayFrontendPortValue(gw, listener.Properties.FrontendPort)
		if (scheme == "http" && port != 80) || (scheme == "https" && port != 443) {
			host = net.JoinHostPort(host, strconv.Itoa(int(port)))
		}
		return scheme + "://" + host
	}
	return ""
}

// applicationGatewayServer is one backend server of a pool, as the gateway
// addresses it.
type applicationGatewayServer struct {
	// address is the pool member as configured — an IP address or an FQDN.
	address string
	// ipConfiguration is the interface IP configuration the member came from,
	// when the member joined the pool through a network interface.
	ipConfiguration *NetworkInterfaceIPConfiguration
}

// applicationGatewayServers lists a pool's members: the addresses configured on
// the pool and the interface IP configurations that joined it.
func applicationGatewayServers(pool ApplicationGatewayBackendAddressPool) []applicationGatewayServer {
	var servers []applicationGatewayServer
	for _, address := range pool.Properties.BackendAddresses {
		switch {
		case address.IPAddress != "":
			servers = append(servers, applicationGatewayServer{address: address.IPAddress})
		case address.Fqdn != "":
			servers = append(servers, applicationGatewayServer{address: address.Fqdn})
		}
	}
	for i := range pool.Properties.BackendIPConfigurations {
		ipcfg := &pool.Properties.BackendIPConfigurations[i]
		if ipcfg.Properties.PrivateIPAddress == "" {
			continue
		}
		servers = append(servers, applicationGatewayServer{
			address:         ipcfg.Properties.PrivateIPAddress,
			ipConfiguration: ipcfg,
		})
	}
	return servers
}

// applicationGatewayHealthyServer returns the first pool member the gateway
// routes to: one its health probes have found Up, or else — when the settings'
// probe sets minServers, the "minimum number of servers that are always marked
// healthy" — the first of the pool's members, which count as healthy however
// their probes went.
func applicationGatewayHealthyServer(gw ApplicationGateway, pool ApplicationGatewayBackendAddressPool, settings ApplicationGatewayBackendHTTPSettings) (applicationGatewayServer, bool) {
	servers := applicationGatewayServers(pool)
	for _, server := range servers {
		if health, _ := applicationGatewayRecordedHealth(gw, settings, server); health == "Up" {
			return server, true
		}
	}
	if probe := applicationGatewayProbe(gw, settings.Properties.Probe); probe != nil && probe.Properties.MinServers > 0 && len(servers) > 0 {
		return servers[0], true
	}
	return applicationGatewayServer{}, false
}

// applicationGatewayProbeSweep is how often the prober looks for pool members
// whose next probe has come due; each probe's own interval sets how often one
// member is probed.
const applicationGatewayProbeSweep = 250 * time.Millisecond

// applicationGatewayHealth holds what the health probes last recorded for each
// pool member under each backend settings member, keyed by
// applicationGatewayHealthKey.
var applicationGatewayHealth = lbplane.NewHealthTracker[string]()

func applicationGatewayHealthKey(gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer) string {
	return strings.ToLower(gw.ID) + "|" + strings.ToLower(settings.ID) + "|" + strings.ToLower(server.address)
}

// applicationGatewaySweepProbes probes every member of every pool a routing
// rule pairs with backend settings, on every running gateway, whose next probe
// has come due at now. A stopped gateway probes nothing, so it starts again
// from Unknown.
func applicationGatewaySweepProbes(ctx context.Context, now time.Time) {
	if azureApplicationGateways == nil {
		return
	}
	var targets []lbplane.HealthTarget[string]
	for _, gw := range azureApplicationGateways.List() {
		if !strings.EqualFold(gw.Properties.OperationalState, "Running") {
			continue
		}
		projectApplicationGateway(&gw)
		for _, pool := range gw.Properties.BackendAddressPools {
			for _, settings := range applicationGatewaySettingsForPool(gw, pool) {
				for _, server := range applicationGatewayServers(pool) {
					spec := applicationGatewayProbeSpec(gw, settings, server, nil)
					targets = append(targets, lbplane.HealthTarget[string]{
						Key: applicationGatewayHealthKey(gw, settings, server),
						// A member returns to service on its first successful
						// probe and leaves it after unhealthyThreshold
						// consecutive failures.
						Policy: lbplane.HealthPolicy{
							Interval:                spec.interval,
							InitialHealthyThreshold: 1,
							HealthyThreshold:        1,
							UnhealthyThreshold:      spec.unhealthyThreshold,
						},
						Probe: func(ctx context.Context) (int, error) {
							return applicationGatewayRunProbe(ctx, spec, server)
						},
					})
				}
			}
		}
	}
	applicationGatewayHealth.Sweep(ctx, now, targets)
}

// applicationGatewayRecordedHealth reports the health and probe log the
// gateway's probes last recorded for a pool member: Unknown until the probes
// reach a verdict, then Up or Down.
func applicationGatewayRecordedHealth(gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer) (string, string) {
	record, ok := applicationGatewayHealth.Health(applicationGatewayHealthKey(gw, settings, server))
	if !ok || record.Checks == 0 {
		return "Unknown", ""
	}
	spec := applicationGatewayProbeSpec(gw, settings, server, nil)
	_, log := applicationGatewayProbeVerdict(spec, server, record.LastStatus, record.LastErr)
	switch record.State {
	case lbplane.HealthHealthy:
		return "Up", log
	case lbplane.HealthUnhealthy:
		return "Down", log
	default:
		return "Unknown", log
	}
}

// applicationGatewayProbeServer runs one probe against a pool member now and
// reports the health and the probe log a real gateway records, which is what an
// on-demand probe answers with.
func applicationGatewayProbeServer(ctx context.Context, gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer, override *ApplicationGatewayOnDemandProbe) (string, string) {
	spec := applicationGatewayProbeSpec(gw, settings, server, override)
	status, err := applicationGatewayRunProbe(ctx, spec, server)
	return applicationGatewayProbeVerdict(spec, server, status, err)
}

// applicationGatewayResolvedProbe is the probe one pool member is checked with,
// after the backend settings, the referenced probe and any on-demand override
// have been folded together.
type applicationGatewayResolvedProbe struct {
	protocol    string
	host        string
	path        string
	port        int32
	timeout     time.Duration
	statusCodes []string
	body        string
	// interval and unhealthyThreshold schedule the gateway's own probes.
	interval           time.Duration
	unhealthyThreshold int
}

// applicationGatewayProbeSpec resolves the probe for one pool member. The
// backend settings supply the protocol, port and host header; the probe they
// reference — or the on-demand probe the caller described — overrides those it
// names; and what neither supplies falls back to the gateway's default probe:
// "/" on the settings' own protocol and port, healthy on any 2xx or 3xx.
func applicationGatewayProbeSpec(gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer, override *ApplicationGatewayOnDemandProbe) applicationGatewayResolvedProbe {
	spec := applicationGatewayResolvedProbe{
		protocol: settings.Properties.Protocol,
		path:     "/",
		port:     settings.Properties.Port,
		timeout:  30 * time.Second,
		host:     settings.Properties.HostName,
		// The default probe runs every 30 seconds and marks a server down
		// after 3 consecutive failures.
		interval:           30 * time.Second,
		unhealthyThreshold: 3,
	}
	if spec.protocol == "" {
		spec.protocol = "Http"
	}
	if spec.port == 0 {
		spec.port = 80
	}
	if settings.Properties.PickHostNameFromBackendAddress {
		spec.host = server.address
	}
	apply := func(protocol, host, path string, timeout, port int32, pickFromSettings bool, match *ApplicationGatewayProbeHealthResponseMatch) {
		if protocol != "" {
			spec.protocol = protocol
		}
		if path != "" {
			spec.path = path
		}
		if port != 0 {
			spec.port = port
		}
		if timeout > 0 {
			spec.timeout = time.Duration(timeout) * time.Second
		}
		switch {
		case pickFromSettings:
			if settings.Properties.HostName != "" {
				spec.host = settings.Properties.HostName
			}
		case host != "":
			spec.host = host
		}
		if match != nil {
			spec.statusCodes = match.StatusCodes
			spec.body = match.Body
		}
	}
	if probe := applicationGatewayProbe(gw, settings.Properties.Probe); probe != nil {
		p := probe.Properties
		apply(p.Protocol, p.Host, p.Path, p.Timeout, p.Port,
			p.PickHostNameFromBackendHTTPSettings || p.PickHostNameFromBackendSettings, p.Match)
		if p.Interval > 0 {
			spec.interval = time.Duration(p.Interval) * time.Second
		}
		if p.UnhealthyThreshold > 0 {
			spec.unhealthyThreshold = int(p.UnhealthyThreshold)
		}
	}
	if override != nil {
		apply(override.Protocol, override.Host, override.Path, override.Timeout, 0,
			override.PickHostNameFromBackendHTTPSettings, override.Match)
	}
	return spec
}

// applicationGatewayRunProbe issues one probe. A Tcp or Tls probe is a
// connection test; an Http or Https probe reads the status the server itself
// returned — the gateway does not follow a redirect — and grades it, and the
// body, against the probe's match criterion.
func applicationGatewayRunProbe(ctx context.Context, spec applicationGatewayResolvedProbe, server applicationGatewayServer) (int, error) {
	address := net.JoinHostPort(server.address, strconv.Itoa(int(spec.port)))
	if strings.EqualFold(spec.protocol, "Tcp") || strings.EqualFold(spec.protocol, "Tls") {
		return 0, lbplane.ProbeTCP(ctx, address, spec.timeout)
	}
	match := lbplane.StatusRange(200, 399)
	if len(spec.statusCodes) > 0 {
		parsed, err := lbplane.ParseStatusMatcher(spec.statusCodes...)
		if err != nil {
			return 0, fmt.Errorf("probe match status codes: %w", err)
		}
		match = parsed
	}
	scheme := "http"
	if strings.EqualFold(spec.protocol, "Https") {
		scheme = "https"
	}
	return lbplane.ProbeHTTP(ctx, lbplane.HTTPProbe{
		Scheme: scheme,
		// A v2 gateway accepts an Https backend only when its certificate is
		// trusted.
		VerifyCertificate: true,
		Address:           address,
		Host:              spec.host,
		Path:              spec.path,
		Timeout:           spec.timeout,
		Match:             match,
		BodyContains:      spec.body,
		BodyLimit:         64 * 1024,
	})
}

// applicationGatewayProbeVerdict turns one probe's result into the health and
// the probe log the gateway reports for it.
func applicationGatewayProbeVerdict(spec applicationGatewayResolvedProbe, server applicationGatewayServer, status int, err error) (string, string) {
	if strings.EqualFold(spec.protocol, "Tcp") || strings.EqualFold(spec.protocol, "Tls") {
		if err != nil {
			return "Down", fmt.Sprintf("TCP connect to %s:%d failed: %v", server.address, spec.port, err)
		}
		return "Up", fmt.Sprintf("TCP connect to %s:%d succeeded", server.address, spec.port)
	}
	scheme := "http"
	if strings.EqualFold(spec.protocol, "Https") {
		scheme = "https"
	}
	path := spec.path
	if path == "" {
		path = "/"
	}
	probeURL := scheme + "://" + net.JoinHostPort(server.address, strconv.Itoa(int(spec.port))) + path
	var mismatch *lbplane.StatusMismatchError
	var body *lbplane.BodyMismatchError
	switch {
	case err == nil:
		return "Up", fmt.Sprintf("Received status code %d while probing %s", status, probeURL)
	case errors.As(err, &mismatch):
		return "Down", fmt.Sprintf("Received status code %d while probing %s", mismatch.StatusCode, probeURL)
	case errors.As(err, &body):
		return "Down", fmt.Sprintf("Probe response body from %s did not contain %q", probeURL, body.Want)
	default:
		return "Down", fmt.Sprintf("Received %v while probing %s", err, probeURL)
	}
}

// applicationGatewayForward sends the request to the chosen pool member the way
// the backend settings describe, applies the rewrite rule set to the request and
// the response, and copies the response back to the client.
func applicationGatewayForward(w http.ResponseWriter, r *http.Request, settings ApplicationGatewayBackendHTTPSettings, rewrite *ApplicationGatewayRewriteRuleSet, server applicationGatewayServer) error {
	scheme := "http"
	if strings.EqualFold(settings.Properties.Protocol, "Https") {
		scheme = "https"
	}
	port := settings.Properties.Port
	if port == 0 {
		port = 80
	}
	path := r.URL.EscapedPath()
	query := r.URL.RawQuery
	// The settings' path is prefixed to every request forwarded through them,
	// so a backend that serves the application under its own root prefix sees
	// the path it expects.
	if settings.Properties.Path != "" {
		path = strings.TrimSuffix(settings.Properties.Path, "/") + path
	}
	headers := r.Header.Clone()
	host := applicationGatewayBackendHost(r, settings, server)
	if rewrite != nil {
		vars := applicationGatewayServerVariables(r, host, 0)
		path, query = applicationGatewayApplyRequestRewrites(rewrite, vars, headers, path, query)
	}
	timeout := time.Duration(settings.Properties.RequestTimeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	up := lbplane.Upstream{
		Scheme:   scheme,
		Address:  net.JoinHostPort(server.address, strconv.Itoa(int(port))),
		Path:     path,
		RawQuery: query,
		Host:     host,
		Header:   headers,
		Timeout:  timeout,
	}
	if rewrite != nil {
		up.ResponseHeader = func(status int, header http.Header) {
			applicationGatewayApplyResponseRewrites(rewrite, applicationGatewayServerVariables(r, host, status), header)
		}
	}
	sim.DeclareWait(r.Context(), up.Timeout)
	return lbplane.Forward(w, r, up)
}

// applicationGatewayBackendHost decides the Host header the gateway sends to
// the backend: the name the settings pin, the pool member's own name, or the
// name the client used.
func applicationGatewayBackendHost(r *http.Request, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer) string {
	if settings.Properties.HostName != "" {
		return settings.Properties.HostName
	}
	if settings.Properties.PickHostNameFromBackendAddress {
		return server.address
	}
	return applicationGatewayRequestHostname(r)
}

// applicationGatewayServerVariables builds the server variables a rewrite rule
// condition can be written against, from the request the gateway is handling
// and (for a response-scoped condition) the status the backend returned.
func applicationGatewayServerVariables(r *http.Request, backendHost string, status int) map[string]string {
	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = host
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	vars := map[string]string{
		"client_ip":       clientIP,
		"host":            applicationGatewayRequestHostname(r),
		"http_method":     r.Method,
		"http_version":    r.Proto,
		"query_string":    r.URL.RawQuery,
		"request_query":   r.URL.RawQuery,
		"request_scheme":  scheme,
		"request_uri":     r.URL.RequestURI(),
		"uri_path":        r.URL.Path,
		"server_port":     strconv.Itoa(int(applicationGatewayRequestPort(r))),
		"ssl_enabled":     strconv.FormatBool(r.TLS != nil),
		"ssl_server_name": backendHost,
	}
	if status != 0 {
		vars["http_status"] = strconv.Itoa(status)
	}
	for name, values := range r.Header {
		vars["http_req_"+strings.ToLower(name)] = strings.Join(values, ",")
	}
	return vars
}

// applicationGatewayApplyRequestRewrites runs the rule set against the request:
// every rule whose conditions hold contributes its request-header changes and
// its URL rewrite, in rule-sequence order.
func applicationGatewayApplyRequestRewrites(set *ApplicationGatewayRewriteRuleSet, vars map[string]string, headers http.Header, path, query string) (string, string) {
	for _, rule := range applicationGatewayOrderedRules(set) {
		if rule.ActionSet == nil || !applicationGatewayConditionsHold(rule.Conditions, vars) {
			continue
		}
		applicationGatewayApplyHeaderActions(rule.ActionSet.RequestHeaderConfigurations, headers)
		if cfg := rule.ActionSet.URLConfiguration; cfg != nil {
			if cfg.ModifiedPath != "" {
				path = cfg.ModifiedPath
			}
			if cfg.ModifiedQueryString != "" {
				query = cfg.ModifiedQueryString
			}
		}
	}
	return path, query
}

// applicationGatewayApplyResponseRewrites runs the rule set's response-header
// actions against the backend's response.
func applicationGatewayApplyResponseRewrites(set *ApplicationGatewayRewriteRuleSet, vars map[string]string, headers http.Header) {
	for _, rule := range applicationGatewayOrderedRules(set) {
		if rule.ActionSet == nil || !applicationGatewayConditionsHold(rule.Conditions, vars) {
			continue
		}
		applicationGatewayApplyHeaderActions(rule.ActionSet.ResponseHeaderConfigurations, headers)
	}
}

func applicationGatewayOrderedRules(set *ApplicationGatewayRewriteRuleSet) []ApplicationGatewayRewriteRule {
	rules := append([]ApplicationGatewayRewriteRule{}, set.Properties.RewriteRules...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].RuleSequence < rules[j].RuleSequence })
	return rules
}

// applicationGatewayApplyHeaderActions sets each named header to its configured
// value; an empty value deletes the header, which is how the rewrite engine
// expresses a removal.
func applicationGatewayApplyHeaderActions(actions []ApplicationGatewayHeaderConfiguration, headers http.Header) {
	for _, action := range actions {
		if action.HeaderName == "" {
			continue
		}
		if action.HeaderValueMatcher != nil && !applicationGatewayMatcherHolds(*action.HeaderValueMatcher, headers.Get(action.HeaderName)) {
			continue
		}
		if action.HeaderValue == "" {
			headers.Del(action.HeaderName)
			continue
		}
		headers.Set(action.HeaderName, action.HeaderValue)
	}
}

func applicationGatewayMatcherHolds(matcher ApplicationGatewayHeaderValueMatcher, value string) bool {
	if matcher.Pattern == "" {
		return true
	}
	matched := applicationGatewayPatternMatches(matcher.Pattern, value, matcher.IgnoreCase)
	if matcher.Negate {
		return !matched
	}
	return matched
}

// applicationGatewayConditionsHold evaluates a rewrite rule's conditions: every
// one must hold for the rule to fire.
func applicationGatewayConditionsHold(conditions []ApplicationGatewayRewriteRuleCondition, vars map[string]string) bool {
	for _, condition := range conditions {
		value := vars[strings.ToLower(strings.TrimPrefix(condition.Variable, "var_"))]
		matched := applicationGatewayPatternMatches(condition.Pattern, value, condition.IgnoreCase)
		if condition.Negate {
			matched = !matched
		}
		if !matched {
			return false
		}
	}
	return true
}

// applicationGatewayPatternMatches applies a rewrite pattern, which the rewrite
// engine treats as a regular expression.
func applicationGatewayPatternMatches(pattern, value string, ignoreCase bool) bool {
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// An uncompilable pattern matches nothing rather than everything, so a
		// malformed condition can never widen a rule's reach.
		return false
	}
	return re.MatchString(value)
}

// applicationGatewayBackendHealth reports what the gateway's probes last
// recorded for every pool member under every set of backend settings a routing
// rule pairs the pool with, which is the pairing the gateway actually uses.
func applicationGatewayBackendHealth(ctx context.Context, gw ApplicationGateway) map[string]any {
	pools := make([]map[string]any, 0, len(gw.Properties.BackendAddressPools))
	for _, pool := range gw.Properties.BackendAddressPools {
		var settingsHealth []map[string]any
		for _, settings := range applicationGatewaySettingsForPool(gw, pool) {
			settingsHealth = append(settingsHealth, map[string]any{
				"backendHttpSettings": settings,
				"servers":             applicationGatewayServerHealth(ctx, gw, settings, pool, nil),
			})
		}
		pools = append(pools, map[string]any{
			"backendAddressPool":            pool,
			"backendHttpSettingsCollection": settingsHealth,
		})
	}
	return map[string]any{"backendAddressPools": pools}
}

// applicationGatewaySettingsForPool lists the backend settings that any routing
// rule — directly, or through a URL path map — pairs with the pool.
func applicationGatewaySettingsForPool(gw ApplicationGateway, pool ApplicationGatewayBackendAddressPool) []ApplicationGatewayBackendHTTPSettings {
	seen := map[string]bool{}
	var out []ApplicationGatewayBackendHTTPSettings
	add := func(poolRef, settingsRef *SubResource) {
		if poolRef == nil || !strings.EqualFold(poolRef.ID, pool.ID) {
			return
		}
		settings := applicationGatewaySettings(gw, settingsRef)
		if settings == nil || seen[settings.ID] {
			return
		}
		seen[settings.ID] = true
		out = append(out, *settings)
	}
	for _, rule := range gw.Properties.RequestRoutingRules {
		add(rule.Properties.BackendAddressPool, rule.Properties.BackendHTTPSettings)
	}
	for _, pathMap := range gw.Properties.URLPathMaps {
		add(pathMap.Properties.DefaultBackendAddressPool, pathMap.Properties.DefaultBackendHTTPSettings)
		for _, rule := range pathMap.Properties.PathRules {
			add(rule.Properties.BackendAddressPool, rule.Properties.BackendHTTPSettings)
		}
	}
	return out
}

// applicationGatewayServerHealth reports each pool member's health: what the
// gateway's own probes recorded, or what an on-demand probe finds now.
func applicationGatewayServerHealth(ctx context.Context, gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, pool ApplicationGatewayBackendAddressPool, override *ApplicationGatewayOnDemandProbe) []map[string]any {
	servers := make([]map[string]any, 0)
	for _, server := range applicationGatewayServers(pool) {
		health, log := applicationGatewayRecordedHealth(gw, settings, server)
		if override != nil {
			health, log = applicationGatewayProbeServer(ctx, gw, settings, server, override)
		}
		entry := map[string]any{
			"address":        server.address,
			"health":         health,
			"healthProbeLog": log,
		}
		if server.ipConfiguration != nil {
			entry["ipConfiguration"] = *server.ipConfiguration
		}
		servers = append(servers, entry)
	}
	return servers
}

// applicationGatewayOnDemandHealth runs the caller's probe against the pool and
// backend settings the request names, instead of the probes the gateway is
// configured with.
func applicationGatewayOnDemandHealth(ctx context.Context, gw ApplicationGateway, probe ApplicationGatewayOnDemandProbe) (map[string]any, error) {
	if probe.BackendAddressPool == nil || probe.BackendHTTPSettings == nil {
		return nil, fmt.Errorf("an on-demand probe requires both a backendAddressPool and a backendHttpSettings reference")
	}
	pool := applicationGatewayPool(gw, probe.BackendAddressPool)
	if pool == nil {
		return nil, fmt.Errorf("backend address pool %q does not belong to application gateway %q", probe.BackendAddressPool.ID, gw.Name)
	}
	settings := applicationGatewaySettings(gw, probe.BackendHTTPSettings)
	if settings == nil {
		return nil, fmt.Errorf("backend HTTP settings %q do not belong to application gateway %q", probe.BackendHTTPSettings.ID, gw.Name)
	}
	return map[string]any{
		"backendAddressPool": *pool,
		"backendHealthHttpSettings": map[string]any{
			"backendHttpSettings": *settings,
			"servers":             applicationGatewayServerHealth(ctx, gw, *settings, *pool, &probe),
		},
	}, nil
}
