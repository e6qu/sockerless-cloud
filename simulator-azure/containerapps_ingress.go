package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// registerContainerAppsIngress routes an app's FQDN
// (`properties.latestRevisionFqdn`), a traffic label's FQDN and an active
// revision's own to one of the revision's ready replicas.
func registerContainerAppsIngress(srv *sim.Server) {
	srv.WrapHandler(lbplane.HostRouter{Route: acaIngressRoute}.Wrap)
}

func acaIngressRoute(_ *http.Request, host string) (http.Handler, bool) {
	if app, ok := acaAppByIngressFqdn(host); ok {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rev, ok := acaRequestRevision(app, host)
			if !ok {
				AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q has no running replica", app.Name)
				return
			}
			proxyACAIngress(w, r, app, rev)
		}), true
	}
	if rev, ok := acaRevisionByFqdn(host); ok && rev.Active {
		if app, ok := acaApps.Get(rev.AppID); ok {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyACAIngress(w, r, app, rev) }), true
		}
	}
	return nil, false
}

// acaIngressServesHost reports a host that reaches a container app or revision
// rather than the simulator's own APIs.
func acaIngressServesHost(host string) bool {
	host = lbplane.Hostname(host)
	if _, ok := acaAppByIngressFqdn(host); ok {
		return true
	}
	rev, ok := acaRevisionByFqdn(host)
	return ok && rev.Active
}

// The target port ingress declares; unset, Container Apps detects it.
func acaIngressTargetPort(app ContainerApp) (int32, bool) {
	if app.Properties.Configuration != nil &&
		app.Properties.Configuration.Ingress != nil &&
		app.Properties.Configuration.Ingress.TargetPort != nil &&
		*app.Properties.Configuration.Ingress.TargetPort > 0 {
		return *app.Properties.Configuration.Ingress.TargetPort, true
	}
	return 0, false
}

// A handler wrapper runs this lookup ahead of every request.
var acaAppsByIngressFqdn sim.GenerationIndex[ContainerApp]

func acaAppByIngressFqdn(host string) (ContainerApp, bool) {
	if host == "" {
		return ContainerApp{}, false
	}
	return acaAppsByIngressFqdn.Lookup(acaApps, host, func(a ContainerApp) []string {
		fqdn := acaAppFqdn(a)
		hosts := []string{fqdn}
		for label := range acaTrafficLabels(a) {
			hosts = append(hosts, acaLabelFqdn(a.Name, fqdn, label))
		}
		return hosts
	})
}

// The app's FQDN with three dashes and the label after the app's name.
func acaLabelFqdn(appName, appFqdn, label string) string {
	return appName + "---" + label + strings.TrimPrefix(appFqdn, appName)
}

func acaTrafficLabels(app ContainerApp) map[string]string {
	labels := map[string]string{}
	if app.Properties.Configuration == nil || app.Properties.Configuration.Ingress == nil {
		return labels
	}
	for _, entry := range app.Properties.Configuration.Ingress.Traffic {
		if entry.Label == "" {
			continue
		}
		revision := entry.RevisionName
		if entry.LatestRevision != nil && *entry.LatestRevision {
			revision = app.Properties.LatestReadyRevisionName
		}
		labels[entry.Label] = revision
	}
	return labels
}

// A label's FQDN goes to its revision, anything else by traffic weight.
func acaRequestRevision(app ContainerApp, host string) (acaRevision, bool) {
	fqdn := acaAppFqdn(app)
	for label, name := range acaTrafficLabels(app) {
		if host != acaLabelFqdn(app.Name, fqdn, label) {
			continue
		}
		rev, ok := acaAppRevision(app.ID, name)
		if !ok || !rev.Active || !acaRevisionServes(rev.ID) {
			return acaRevision{}, false
		}
		return rev, true
	}
	return acaPickRevision(app)
}

// Container Apps ingress cancels a request not complete within 240 seconds.
const acaIngressRequestTimeout = 240 * time.Second

// The ingress appends the client's IP to X-Forwarded-For, overwrites
// X-Forwarded-Proto with the client's protocol, and passes the client
// certificate as X-Forwarded-Client-Cert when it accepts or requires one.
func acaIngressForwardedHeaders(r *http.Request, app ContainerApp) func(http.Header) {
	clientIP, _ := lbplane.ClientAddress(r)
	proto := lbplane.ClientProto(r)
	clientCert := acaClientCertHeader(r, app)
	return func(h http.Header) {
		lbplane.AppendToHeaderList(h, "X-Forwarded-For", ", ", clientIP)
		h.Set("X-Forwarded-Proto", proto)
		h.Del("X-Forwarded-Client-Cert")
		if clientCert != "" {
			h.Set("X-Forwarded-Client-Cert", clientCert)
		}
	}
}

// proxyACAIngress forwards to a running replica of the revision and copies the
// response back, as Azure's managed front end does.
func proxyACAIngress(w http.ResponseWriter, r *http.Request, app ContainerApp, rev acaRevision) {
	pickCtx, cancel := context.WithTimeout(r.Context(), acaIngressRequestTimeout)
	defer cancel()
	group, ok := acaAcquireReplica(pickCtx, rev.ID)
	if !ok {
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q has no ready replica", app.Name)
		return
	}
	ip, err := group.Main.Address(r.Context())
	if err != nil || ip == "" {
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q replica has no reachable IP", app.Name)
		return
	}

	port, err := acaReplicaTargetPort(r.Context(), app, ip)
	if err != nil {
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q: %v", app.Name, err)
		return
	}

	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "read request body: %v", err)
		return
	}

	// Ingress passes the client's Host header and carries WebSocket upgrades.
	up := lbplane.Upstream{
		Scheme:        "http",
		Address:       net.JoinHostPort(ip, strconv.Itoa(int(port))),
		Path:          r.URL.EscapedPath(),
		RawQuery:      r.URL.RawQuery,
		RequestHeader: acaIngressForwardedHeaders(r, app),
		Timeout:       acaIngressRequestTimeout,
	}
	sim.DeclareWait(r.Context(), up.Timeout)

	up.Body = bytes.NewReader(body)
	_ = lbplane.Serve(w, r, up, lbplane.Answers{
		// The ingress's Envoy front end answers a route timeout with this local reply.
		Timeout: func(w http.ResponseWriter, _ error) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = io.WriteString(w, "upstream request timeout")
		},
		Failed: func(w http.ResponseWriter, err error) {
			AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q ingress: %v", app.Name, err)
		},
	})
}
