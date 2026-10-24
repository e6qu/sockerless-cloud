package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// registerContainerAppsIngress implements Azure Container Apps ingress: an App
// with ingress is reached at its FQDN (`properties.latestRevisionFqdn`), and
// the platform routes that hostname to the App's running replica. The sim
// collapses every App onto its single endpoint, so — exactly like the storage
// data-plane and Functions virtual-host routing — it dispatches by the HTTP
// Host header: a request whose Host matches an App's ingress FQDN is reverse-
// proxied to that App's replica container (on its target port 8080), verbatim.
// A client (the SDK, the CLI, curl, anything else) reaches an App the same way against
// real ACA and the sim, differing only in the endpoint coordinate it points the
// TCP connection at while carrying the App FQDN as the Host header.
func registerContainerAppsIngress(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := acaRequestHost(r.Host)
			if app, ok := acaAppByIngressFqdn(host); ok {
				rev, ok := acaRequestRevision(app, host)
				if !ok {
					AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q has no running replica", app.Name)
					return
				}
				proxyACAIngress(w, r, app, rev)
				return
			}
			if rev, ok := acaRevisionByFqdn(host); ok && rev.Active {
				if app, ok := acaApps.Get(rev.AppID); ok {
					proxyACAIngress(w, r, app, rev)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
}

func acaRequestHost(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// acaIngressServesHost reports whether a request carrying host reaches a
// container app or one of its revisions rather than the simulator's own APIs.
func acaIngressServesHost(host string) bool {
	host = acaRequestHost(host)
	if _, ok := acaAppByIngressFqdn(host); ok {
		return true
	}
	rev, ok := acaRevisionByFqdn(host)
	return ok && rev.Active
}

// acaIngressTargetPort returns the App's configured ingress target port — the
// container port ACA ingress forwards to. Defaults to 8080 (the sockerless
// bootstrap's listener) when ingress declares no explicit port.
func acaIngressTargetPort(app ContainerApp) int32 {
	if app.Properties.Configuration != nil &&
		app.Properties.Configuration.Ingress != nil &&
		app.Properties.Configuration.Ingress.TargetPort != nil &&
		*app.Properties.Configuration.Ingress.TargetPort > 0 {
		return *app.Properties.Configuration.Ingress.TargetPort
	}
	return 8080
}

// acaAppsByIngressFqdn indexes container apps by the FQDN their ingress
// answers on. The lookup below runs in a handler wrapper, so every request into
// the simulator pays it before any handler runs.
var acaAppsByIngressFqdn sim.GenerationIndex[ContainerApp]

// acaAppByIngressFqdn returns the App whose ingress FQDN equals host.
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

// acaLabelFqdn is the FQDN a labelled traffic entry answers on: the app's,
// with three dashes and the label after the app's name.
func acaLabelFqdn(appName, appFqdn, label string) string {
	return appName + "---" + label + strings.TrimPrefix(appFqdn, appName)
}

// acaTrafficLabels maps each label of the app's traffic entries to the
// revision it names.
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

// acaRequestRevision is the revision a request to the app goes to: a
// label's for the label's FQDN, else one by traffic weight.
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

// acaIngressRequestTimeout is the documented limit Container Apps ingress puts
// on an HTTP request: it cancels one not complete within 240 seconds.
const acaIngressRequestTimeout = 240 * time.Second

// acaIngressForwardedHeaders adds the headers the ingress documents: the
// client's IP appended to X-Forwarded-For, X-Forwarded-Proto overwritten
// with the protocol the client used, and the client certificate as
// X-Forwarded-Client-Cert when the ingress accepts or requires one.
func acaIngressForwardedHeaders(r *http.Request, app ContainerApp) func(http.Header) {
	clientIP, _ := lbplane.ClientAddress(r)
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
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

// proxyACAIngress forwards the request to a running replica container of the
// revision's target port (8080), preserving method, path, query, headers and
// body, and copies the response back — the App-ingress equivalent of Azure's
// managed front-end.
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

	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "read request body: %v", err)
		return
	}

	// Route to the App's configured ingress target port — exactly what real
	// ACA ingress forwards to (the container's listening port). Ingress passes
	// the client's Host header and carries WebSocket upgrades through.
	up := lbplane.Upstream{
		Scheme:        "http",
		Address:       net.JoinHostPort(ip, strconv.Itoa(int(acaIngressTargetPort(app)))),
		Path:          r.URL.EscapedPath(),
		RawQuery:      r.URL.RawQuery,
		RequestHeader: acaIngressForwardedHeaders(r, app),
		Timeout:       acaIngressRequestTimeout,
	}
	sim.DeclareWait(r.Context(), up.Timeout)

	up.Body = bytes.NewReader(body)
	err = lbplane.Forward(w, r, up)
	abortStartedForward(w, err)
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	case errors.Is(err, context.DeadlineExceeded) && errors.As(err, new(*lbplane.SendError)):
		// The ingress's Envoy front end answers a route timeout with this local reply.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, "upstream request timeout")
	default:
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q ingress: %v", app.Name, err)
	}
}
