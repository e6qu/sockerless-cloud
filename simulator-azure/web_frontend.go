package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// A request whose Host is an app's or slot's hostname goes verbatim to that
// app's or slot's container on its port, started first if none runs. A stopped
// site answers App Service's stopped-site page; a site with nothing to run
// answers 503 naming what it lacks.
func registerAppServiceFrontEnd(srv *sim.Server) {
	srv.WrapHandler(lbplane.HostRouter{
		Route: func(_ *http.Request, host string) (http.Handler, bool) {
			site, ok := appServiceSiteByHost(host)
			if !ok {
				return nil, false
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if siteStopped(&site) {
					writeStoppedSitePage(w)
					return
				}
				if !siteRunsContainer(&site) {
					AzureErrorf(w, "ServiceUnavailable", http.StatusServiceUnavailable, "%v", siteImageMissing(&site))
					return
				}
				serveSiteRequest(w, r, &site)
			}), true
		},
	}.Wrap)
}

// A handler wrapper runs this lookup ahead of every request.
var (
	appServiceSitesByHost sim.GenerationIndex[Site]
	appServiceSlotsByHost sim.GenerationIndex[Site]
)

func appServiceSiteByHost(host string) (Site, bool) {
	host = lbplane.Hostname(host)
	if host == "" || azfSites == nil {
		return Site{}, false
	}
	if site, ok := appServiceSitesByHost.Lookup(azfSites, host, siteHostKeys); ok {
		return site, true
	}
	if webSlots == nil {
		return Site{}, false
	}
	return appServiceSlotsByHost.Lookup(webSlots, host, siteHostKeys)
}

func siteHostKeys(s Site) []string {
	keys := []string{strings.ToLower(s.Properties.DefaultHostName)}
	for _, h := range s.Properties.HostNames {
		if h = strings.ToLower(h); h != keys[0] {
			keys = append(keys, h)
		}
	}
	return keys
}

func serveSiteRequest(w http.ResponseWriter, r *http.Request, site *Site) {
	// The Functions host enforces its functions' authLevel itself.
	_, hostRun := siteFunctionsHostStack(site)
	if r.URL.Path == "/api/function" && !hostRun && !azureFunctionInvokeAuthorized(site, r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if appServiceClientCertRequired(r, site) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<html><head><title>403 Forbidden</title></head><body>403 Forbidden: the site requires a client certificate.</body></html>")
		return
	}
	sim.DeclareWait(r.Context(), azureFunctionsHTTPRequestLimit)
	address, err := siteContainerAddress(r.Context(), site)
	if errors.Is(err, errSiteStopped) {
		writeStoppedSitePage(w)
		return
	}
	if err != nil {
		AzureErrorf(w, "ServiceUnavailable", http.StatusServiceUnavailable, "%v", err)
		return
	}
	_ = lbplane.Serve(w, r, lbplane.Upstream{
		Scheme:        "http",
		Address:       address,
		Path:          r.URL.EscapedPath(),
		RawQuery:      r.URL.RawQuery,
		RequestHeader: appServiceForwardedHeaders(r, site),
		Timeout:       azureFunctionsHTTPRequestLimit,
	}, lbplane.Answers{
		Failed: func(w http.ResponseWriter, err error) {
			AzureErrorf(w, "BadGateway", http.StatusBadGateway, "site %q: %v", site.Name, err)
		},
	})
}

// The headers App Service's TLS-terminating front end documents:
// X-Forwarded-Proto and X-AppService-Proto, the client's address and port
// appended to X-Forwarded-For as Application Request Routing writes it, and
// X-ARR-ClientCert when the site asks for one.
func appServiceForwardedHeaders(r *http.Request, site *Site) func(http.Header) {
	client := r.RemoteAddr
	proto := lbplane.ClientProto(r)
	clientCert := appServiceClientCertHeader(r, site)
	return func(h http.Header) {
		lbplane.AppendToHeaderList(h, "X-Forwarded-For", ", ", client)
		h.Set("X-Forwarded-Proto", proto)
		h.Set("X-AppService-Proto", proto)
		h.Del("X-ARR-ClientCert")
		if clientCert != "" {
			h.Set("X-ARR-ClientCert", clientCert)
		}
	}
}

// Like the platform's start-up ping, wait until the port answers HTTP, the
// container exits, or WEBSITES_CONTAINER_START_TIME_LIMIT passes.
func siteContainerAddress(ctx context.Context, site *Site) (string, error) {
	inst := azfInstanceFor(site.Name)
	started, err := inst.start(site)
	if err != nil {
		return "", fmt.Errorf("site %q failed to start: %w", site.Name, err)
	}
	if started.address != "" {
		return started.address, nil
	}
	containerID, candidates, port := started.containerID, started.candidates, started.port
	exit, logsDone := started.exit, started.logsDone

	limit := siteStartTimeLimit(site)
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// A container the engine reports no address for has already stopped.
	if len(candidates) == 0 {
		select {
		case <-exit.done:
			select {
			case <-logsDone:
			case <-ctx.Done():
			}
			return "", exit.exitedBeforeAnswering(site.Name, port)
		case <-waitCtx.Done():
			return "", fmt.Errorf("site %q container has no address to reach port %d at", site.Name, port)
		}
	}
	go func() {
		select {
		case <-exit.done:
			cancel()
		case <-waitCtx.Done():
		}
	}()
	reached, err := appServiceWarmupPing(waitCtx, candidates)
	if err != nil {
		select {
		case <-exit.done:
			// The container's last output reaches its log before the failure is
			// reported.
			select {
			case <-logsDone:
			case <-ctx.Done():
			}
			return "", exit.exitedBeforeAnswering(site.Name, port)
		default:
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("site %q container didn't respond to HTTP pings on port %d within %s, failing site start", site.Name, port, limit)
	}
	parsed, err := url.Parse(reached)
	if err != nil {
		return "", err
	}
	inst.answered(containerID, parsed.Host)
	return parsed.Host, nil
}

// Unless another container replaced it meanwhile.
func (inst *azureFunctionInstance) answered(containerID, address string) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.containerID == containerID {
		inst.address = address
	}
}

// /robots933456.txt is the path the platform pings while a container starts.
// A port that only accepts connections is not ready: a published loopback port
// accepts through the engine's proxy before the workload listens.
func appServiceWarmupPing(ctx context.Context, candidates []string) (string, error) {
	// The platform waits for the warmup answer however slowly it comes, so only
	// the start-time limit bounds a request; the dial stays short so an unbound
	// port is retried.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: time.Second}).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	var lastErr error
	for {
		for _, candidate := range candidates {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate+"/robots933456.txt", nil)
			if err != nil {
				return "", err
			}
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				return candidate, nil
			}
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("%w: %w", ctx.Err(), lastErr)
			}
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

const stoppedSitePage = `<!DOCTYPE html>
<html>
<head>
<title>Web App - Unavailable</title>
<meta http-equiv="Content-Type" content="text/html; charset=utf-8" />
</head>
<body>
<h1>Error 403 - This web app is stopped.</h1>
<p>The web app you have attempted to reach is currently stopped and does not accept any requests. Please try to reload the page or visit it again soon.</p>
<p>If you are the web app administrator, please find the common 403 error scenarios and resolution <a href="https://go.microsoft.com/fwlink/?linkid=2095007" target="_blank">here</a>. For further troubleshooting tools and recommendations, please visit <a href="https://portal.azure.com/" target="_blank">Azure Portal</a>.</p>
</body>
</html>
`

func writeStoppedSitePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, stoppedSitePage)
}

// A stopped site's container and webjobs stop; a started site's continuous
// webjobs start, and its container too when Always On.
func webApplySiteState(site Site) error {
	if siteStopped(&site) {
		inst := azfInstanceFor(site.Name)
		inst.teardown()
		webStopSiteWebJobs(site.ID)
		return nil
	}
	if err := webDiscoverWebJobs(site.ID); err != nil {
		return err
	}
	startAlwaysOnSite(site)
	return nil
}
