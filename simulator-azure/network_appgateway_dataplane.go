package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// The application gateway data plane. A client reaches a gateway at one of its
// frontend addresses — a referenced public IP's, or a private address from the
// subnet — and the simulator dispatches on that address in the Host header, as
// for the load balancer, Container Apps ingress and storage planes. The
// gateway's configuration executes:
//
//   - the listener is selected by frontend address, port and host name, a
//     multi-site listener winning over a basic one;
//   - the listener's routing rule sends the request to a pool, through a URL
//     path map, or into a redirect the gateway answers itself;
//   - the rule's (or path rule's) rewrite rule set rewrites request headers,
//     response headers and the URL, conditions evaluated against server
//     variables;
//   - the backend settings decide protocol, port, host header, path prefix and
//     timeout;
//   - the pool member is one the probes found Up, using the settings' probe or
//     Azure's default.
//
// BackendHealth answers from those probes, which run on each probe's interval.

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

// A stopped gateway holds no frontend, so its address answers nothing.
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
	// A wrapper ahead of every handler runs this, so match from an index; check
	// operational state on the matched row, so starting or stopping needs no
	// invalidation.
	for _, gw := range azureApplicationGatewaysByFrontend.LookupAll(
		azureApplicationGateways, hostname, applicationGatewayFrontendAddresses) {
		if !strings.EqualFold(gw.Properties.OperationalState, "Running") {
			continue
		}
		for _, fe := range gw.Properties.FrontendIPConfigurations {
			if !strings.EqualFold(applicationGatewayFrontendAddress(fe), hostname) {
				continue
			}
			// Recompute the pool members, so the data plane forwards to the set the
			// gateway reports.
			projectApplicationGateway(&gw)
			return gw, fe, true
		}
	}
	return ApplicationGateway{}, ApplicationGatewayFrontendIPConfiguration{}, false
}

var azureApplicationGatewaysByFrontend sim.GenerationIndex[ApplicationGateway]

// Whatever the gateway's operational state.
func applicationGatewayFrontendAddresses(gw ApplicationGateway) []string {
	addresses := make([]string, 0, len(gw.Properties.FrontendIPConfigurations))
	for _, fe := range gw.Properties.FrontendIPConfigurations {
		addresses = append(addresses, strings.ToLower(applicationGatewayFrontendAddress(fe)))
	}
	return addresses
}

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
		// Application Gateway answers 502 when it cannot reach any backend server.
		http.Error(w, "no healthy backend servers in pool "+target.pool.Name, http.StatusBadGateway)
		return
	}
	_ = applicationGatewayForward(w, r, *target.settings, target.rewrite, server, applicationGatewaySourceAddress(gw))
}

var applicationGatewayAnswers = lbplane.Answers{
	// Application Gateway answers 502 when the backend's network security group
	// drops its flow.
	NotAdmitted: func(w http.ResponseWriter, err error) {
		http.Error(w, "the backend's network security groups do not admit the gateway: "+err.Error(), http.StatusBadGateway)
	},
	Failed: func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadGateway) },
}

// Listeners match on frontend address and port; a listener naming host names
// wins over one naming none, and a literal name over a wildcard.
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

func applicationGatewayListenerHostNames(listener ApplicationGatewayHTTPListener) []string {
	names := append([]string{}, listener.Properties.HostNames...)
	if listener.Properties.HostName != "" {
		names = append(names, listener.Properties.HostName)
	}
	return names
}

// An exact name, or one "*" for any run of characters (Azure allows a leading
// or trailing wildcard).
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

// The lowest priority value wins, as Application Gateway evaluates rules.
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

type applicationGatewayTarget struct {
	pool     *ApplicationGatewayBackendAddressPool
	settings *ApplicationGatewayBackendHTTPSettings
	redirect *ApplicationGatewayRedirectConfiguration
	rewrite  *ApplicationGatewayRewriteRuleSet
}

// A path-based rule consults its URL path map: the first matching path rule
// decides, the map's defaults otherwise.
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

// A pattern is an exact path or a path ending in "*" for that prefix; the first
// match in declared order wins.
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

// The target is another listener of the gateway, whose host and port it
// resolves, or a literal URL; path and query carry over when configured.
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

func applicationGatewayRedirectStatus(redirectType string) int {
	switch strings.ToLower(redirectType) {
	case "permanent":
		return http.StatusMovedPermanently
	case "temporary":
		return http.StatusTemporaryRedirect
	case "seeother":
		return http.StatusSeeOther
	default:
		// Azure applies "Found" when no redirect type is named.
		return http.StatusFound
	}
}

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

type applicationGatewayServer struct {
	// An IP address or an FQDN, as configured.
	address string
	// Set when the member joined through a network interface.
	ipConfiguration *NetworkInterfaceIPConfiguration
}

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

// One the probes found Up or, when the probe sets minServers ("minimum number
// of servers that are always marked healthy"), the pool's first member
// regardless.
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

// How often the prober looks for due probes; each probe's interval sets how
// often one member is probed.
const applicationGatewayProbeSweep = 250 * time.Millisecond

// Keyed by applicationGatewayHealthKey.
var applicationGatewayHealth = lbplane.NewHealthTracker[string]()

func applicationGatewayHealthKey(gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer) string {
	return strings.ToLower(gw.ID) + "|" + strings.ToLower(settings.ID) + "|" + strings.ToLower(server.address)
}

// Only pools a routing rule pairs with backend settings, on running gateways.
// A stopped gateway probes nothing, so it restarts from Unknown.
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
						// A member returns to service on its first successful probe and leaves it
						// after unhealthyThreshold consecutive failures.
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

// Unknown until the probes reach a verdict, then Up or Down.
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

// What an on-demand probe answers with.
func applicationGatewayProbeServer(ctx context.Context, gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer, override *ApplicationGatewayOnDemandProbe) (string, string) {
	spec := applicationGatewayProbeSpec(gw, settings, server, override)
	status, err := applicationGatewayRunProbe(ctx, spec, server)
	return applicationGatewayProbeVerdict(spec, server, status, err)
}

// The backend settings, the referenced probe and any on-demand override,
// folded together.
type applicationGatewayResolvedProbe struct {
	protocol           string
	host               string
	path               string
	port               int32
	timeout            time.Duration
	statusCodes        []string
	body               string
	interval           time.Duration
	unhealthyThreshold int
}

// The settings supply protocol, port and host header; the referenced or
// on-demand probe overrides what it names; the rest falls back to the default
// probe: "/" on the settings' protocol and port, healthy on any 2xx or 3xx.
func applicationGatewayProbeSpec(gw ApplicationGateway, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer, override *ApplicationGatewayOnDemandProbe) applicationGatewayResolvedProbe {
	spec := applicationGatewayResolvedProbe{
		protocol: settings.Properties.Protocol,
		path:     "/",
		port:     settings.Properties.Port,
		timeout:  30 * time.Second,
		host:     settings.Properties.HostName,
		// The default probe runs every 30 seconds and marks a server down after 3
		// consecutive failures.
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

// Tcp and Tls probes test the connection; Http and Https grade the server's
// own status, not following a redirect, and the body, against the match
// criterion.
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
		// A v2 gateway accepts an Https backend only with a trusted certificate.
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

// The private frontend the gateway holds in its subnet, or else an instance
// address in its gatewayIPConfigurations subnet: what the backend's network
// security groups see.
func applicationGatewaySourceAddress(gw ApplicationGateway) netip.Addr {
	for _, fe := range gw.Properties.FrontendIPConfigurations {
		if fe.Properties.Subnet != nil {
			if addr, err := netip.ParseAddr(fe.Properties.PrivateIPAddress); err == nil {
				return addr
			}
		}
	}
	for _, ipcfg := range gw.Properties.GatewayIPConfigurations {
		if ipcfg.Properties.Subnet == nil {
			continue
		}
		subnet, ok := azureSubnetByID(ipcfg.Properties.Subnet.ID)
		if !ok {
			continue
		}
		cidr, err := azureSubnetIPv4CIDR(subnet.Properties)
		if err != nil {
			continue
		}
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			addr := prefix.Masked().Addr()
			for i := 0; i < azureSubnetReservation.First; i++ {
				addr = addr.Next()
			}
			return addr
		}
	}
	return netip.Addr{}
}

// applicationGatewayForward applies the rewrite rule set to the request and
// response and copies the response back.
func applicationGatewayForward(w http.ResponseWriter, r *http.Request, settings ApplicationGatewayBackendHTTPSettings, rewrite *ApplicationGatewayRewriteRuleSet, server applicationGatewayServer, source netip.Addr) error {
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
	// Prefix the settings' path, for a backend serving under its own root.
	if settings.Properties.Path != "" {
		path = strings.TrimSuffix(settings.Properties.Path, "/") + path
	}
	headers := r.Header.Clone()
	lbplane.RemoveHopByHopHeaders(headers)
	applicationGatewayAddForwardedHeaders(r, headers)
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
		// The backend's network security groups see the gateway's own subnet address.
		Admit: func(netip.Addr) error {
			return azureAdmitIngress("Tcp", source, server.address, int(port))
		},
	}
	if rewrite != nil {
		up.ResponseHeader = func(status int, header http.Header) {
			applicationGatewayApplyResponseRewrites(rewrite, applicationGatewayServerVariables(r, host, status), header)
		}
	}
	sim.DeclareWait(r.Context(), up.Timeout)
	return lbplane.Serve(w, r, up, applicationGatewayAnswers)
}

// Application Gateway adds six headers before forwarding, and before a rewrite
// rule set runs, so a rewrite can read or replace them. X-Forwarded-For is a
// comma-separated list of IP:port.
func applicationGatewayAddForwardedHeaders(r *http.Request, headers http.Header) {
	clientIP, clientPort := lbplane.ClientAddress(r)
	lbplane.AppendToHeaderList(headers, "X-Forwarded-For", ", ", net.JoinHostPort(clientIP, clientPort))
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	headers.Set("X-Forwarded-Proto", proto)
	headers.Set("X-Forwarded-Port", strconv.Itoa(int(applicationGatewayRequestPort(r))))
	headers.Set("X-Original-Host", r.Host)
	headers.Set("X-Original-Url", r.URL.RequestURI())
	headers.Set("X-Appgw-Trace-Id", lbplane.RandomHex(16))
}

// The name the settings pin, the pool member's own name, or the client's.
func applicationGatewayBackendHost(r *http.Request, settings ApplicationGatewayBackendHTTPSettings, server applicationGatewayServer) string {
	if settings.Properties.HostName != "" {
		return settings.Properties.HostName
	}
	if settings.Properties.PickHostNameFromBackendAddress {
		return server.address
	}
	return applicationGatewayRequestHostname(r)
}

// From the request and, for a response-scoped condition, the backend's status.
func applicationGatewayServerVariables(r *http.Request, backendHost string, status int) map[string]string {
	clientIP, clientPort := lbplane.ClientAddress(r)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	forwardedFor := http.Header{"X-Forwarded-For": r.Header.Values("X-Forwarded-For")}
	lbplane.AppendToHeaderList(forwardedFor, "X-Forwarded-For", ", ", clientIP)
	vars := map[string]string{
		"add_x_forwarded_for_proxy": forwardedFor.Get("X-Forwarded-For"),
		"client_ip":                 clientIP,
		"client_port":               clientPort,
		"host":                      applicationGatewayRequestHostname(r),
		"http_method":               r.Method,
		"http_version":              r.Proto,
		"query_string":              r.URL.RawQuery,
		"request_query":             r.URL.RawQuery,
		"request_scheme":            scheme,
		"request_uri":               r.URL.RequestURI(),
		"uri_path":                  r.URL.Path,
		"server_port":               strconv.Itoa(int(applicationGatewayRequestPort(r))),
		"ssl_enabled":               strconv.FormatBool(r.TLS != nil),
		"ssl_server_name":           backendHost,
	}
	if status != 0 {
		vars["http_status"] = strconv.Itoa(status)
	}
	for name, values := range r.Header {
		vars["http_req_"+strings.ToLower(name)] = strings.Join(values, ",")
	}
	return vars
}

// Every rule whose conditions hold contributes, in rule-sequence order.
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

// An empty value deletes the header, as the rewrite engine expresses a
// removal.
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

// The rewrite engine treats a pattern as a regular expression.
func applicationGatewayPatternMatches(pattern, value string, ignoreCase bool) bool {
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// An uncompilable pattern matches nothing, so a malformed condition cannot
		// widen a rule's reach.
		return false
	}
	return re.MatchString(value)
}

// Only the settings a routing rule pairs with each pool, as the gateway uses
// them.
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

// Directly or through a URL path map.
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
