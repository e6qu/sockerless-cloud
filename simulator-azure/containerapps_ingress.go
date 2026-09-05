package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
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
			host := r.Host
			if i := strings.LastIndex(host, ":"); i >= 0 {
				host = host[:i]
			}
			app, ok := acaAppByIngressFqdn(host)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			proxyACAIngress(w, r, app)
		})
	})
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
		return []string{a.Properties.LatestRevisionFqdn}
	})
}

// proxyACAIngress forwards the request to the App's running replica container's
// target port (8080), preserving method, path, query, headers and body, and
// copies the response back — the App-ingress equivalent of Azure's managed
// front-end.
func proxyACAIngress(w http.ResponseWriter, r *http.Request, app ContainerApp) {
	v, ok := acaAppReplicaHandles.Load(app.ID)
	if !ok {
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q has no running replica", app.Name)
		return
	}
	handles, _ := v.([]*sim.ContainerHandle)
	if len(handles) == 0 || handles[0] == nil {
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q has no running replica", app.Name)
		return
	}
	ip := sim.ContainerIPv4(handles[0].ContainerID)
	if ip == "" {
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
	// ACA ingress forwards to (the container's listening port).
	target := fmt.Sprintf("http://%s:%d%s", ip, acaIngressTargetPort(app), r.URL.RequestURI())
	// An upgraded connection outlives any request deadline, so it gets none.
	ctx := r.Context()
	upgrade := sim.IsUpgradeRequest(r)
	client := &http.Client{CheckRedirect: returnRedirectsToClient}
	if !upgrade {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		client.Timeout = 10 * time.Minute
	}

	// The replica's HTTP listener binds a moment after the container starts;
	// retry the connection briefly so an invoke racing replica startup doesn't
	// surface as a transient 502 (the managed front-end likewise buffers).
	var resp *http.Response
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, rerr := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
		if rerr != nil {
			AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "build proxy request: %v", rerr)
			return
		}
		copyProxyHeaders(req.Header, r.Header)
		resp, err = client.Do(req)
		if err == nil || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q ingress: %v", app.Name, ctx.Err())
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q ingress: %v", app.Name, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSwitchingProtocols {
		if terr := sim.TunnelUpgradedResponse(w, resp); terr != nil {
			AzureErrorf(w, "BadGateway", http.StatusBadGateway, "container app %q ingress: %v", app.Name, terr)
		}
		return
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyProxyHeaders(dst, src http.Header) {
	for k, vs := range src {
		// Hop-by-hop / host headers are not forwarded to the upstream.
		switch http.CanonicalHeaderKey(k) {
		case "Host", "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
