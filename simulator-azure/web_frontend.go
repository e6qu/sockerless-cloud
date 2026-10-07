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

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

// registerAppServiceFrontEnd implements the App Service front end: a request
// whose Host is one of an app's or a deployment slot's hostnames goes to that
// app's or slot's own container on its port, verbatim, after the container is
// started if none is running. The simulator serves every site on its one
// endpoint, so it dispatches on the Host header, as Container Apps ingress
// does. A stopped site answers App Service's stopped-site page, and a site the
// simulator has nothing to run for answers 503 naming what it lacks.
func registerAppServiceFrontEnd(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			site, ok := appServiceSiteByHost(r.Host)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if siteStopped(&site) {
				writeStoppedSitePage(w)
				return
			}
			if !siteRunsContainer(&site) {
				AzureErrorf(w, "ServiceUnavailable", http.StatusServiceUnavailable, "%v", siteImageMissing(&site))
				return
			}
			serveSiteRequest(w, r, &site)
		})
	})
}

// appServiceSitesByHost and appServiceSlotsByHost index apps and deployment
// slots by the hostnames they answer on. The lookup runs in a handler
// wrapper, so every request into the simulator pays it before any handler
// runs.
var (
	appServiceSitesByHost sim.GenerationIndex[Site]
	appServiceSlotsByHost sim.GenerationIndex[Site]
)

// appServiceSiteByHost returns the app or deployment slot a request's Host
// header addresses.
func appServiceSiteByHost(host string) (Site, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
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

// siteHostKeys are the hostnames an app or slot answers on.
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
	err = lbplane.Forward(w, r, lbplane.Upstream{
		Scheme:   "http",
		Address:  address,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Timeout:  azureFunctionsHTTPRequestLimit,
	})
	abortStartedForward(w, err)
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	default:
		AzureErrorf(w, "BadGateway", http.StatusBadGateway, "site %q: %v", site.Name, err)
	}
}

// siteContainerAddress starts the site's container if none is running and
// returns the address its port answers on. Like the platform's start-up ping,
// it waits until the port answers an HTTP request, the container exits, or
// WEBSITES_CONTAINER_START_TIME_LIMIT passes.
func siteContainerAddress(ctx context.Context, site *Site) (string, error) {
	inst := azfInstanceFor(site.Name)
	inst.mu.Lock()
	if err := inst.ensureStarted(site); err != nil {
		inst.mu.Unlock()
		return "", fmt.Errorf("site %q failed to start: %w", site.Name, err)
	}
	if inst.address != "" {
		address := inst.address
		inst.mu.Unlock()
		return address, nil
	}
	containerID, candidates, port := inst.containerID, inst.candidates, inst.port
	exited, logsDone := inst.exited, inst.logsDone
	inst.mu.Unlock()

	limit := siteStartTimeLimit(site)
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// A container the engine reports no address for has already stopped.
	if len(candidates) == 0 {
		select {
		case <-exited:
			select {
			case <-logsDone:
			case <-ctx.Done():
			}
			return "", fmt.Errorf("site %q container exited before it answered on port %d", site.Name, port)
		case <-waitCtx.Done():
			return "", fmt.Errorf("site %q container has no address to reach port %d at", site.Name, port)
		}
	}
	go func() {
		select {
		case <-exited:
			cancel()
		case <-waitCtx.Done():
		}
	}()
	reached, err := appServiceWarmupPing(waitCtx, candidates)
	if err != nil {
		select {
		case <-exited:
			// The container's last output reaches its log before the
			// failure is reported.
			select {
			case <-logsDone:
			case <-ctx.Done():
			}
			return "", fmt.Errorf("site %q container exited before it answered on port %d", site.Name, port)
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
	inst.mu.Lock()
	if inst.containerID == containerID {
		inst.address = parsed.Host
	}
	inst.mu.Unlock()
	return parsed.Host, nil
}

// appServiceWarmupPing asks each candidate for /robots933456.txt, the path the
// platform pings while a container starts, and returns the first that answers
// with any HTTP response. A port that only accepts connections is not ready: a
// published loopback port accepts through the engine's proxy before the
// workload listens.
func appServiceWarmupPing(ctx context.Context, candidates []string) (string, error) {
	// The platform waits for the warmup request's answer, however slowly the
	// workload gives it, so only the start-time limit in ctx bounds a request;
	// the dial stays short so a port nothing listens on yet is retried.
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

// stoppedSitePage is the page App Service's front end answers a stopped app's
// hostname with.
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

// webApplySiteState makes what runs for a site match its state: a stopped
// site's container and webjobs stop; a started site's continuous webjobs
// start, and its container too when it is Always On.
func webApplySiteState(site Site) error {
	if siteStopped(&site) {
		inst := azfInstanceFor(site.Name)
		inst.mu.Lock()
		inst.teardownLocked()
		inst.mu.Unlock()
		webStopSiteWebJobs(site.ID)
		return nil
	}
	if err := webDiscoverWebJobs(site.ID); err != nil {
		return err
	}
	startAlwaysOnSite(site)
	return nil
}
