package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

// registerAppServiceFrontEnd implements the App Service front end for a site
// that runs a container: a request whose Host is one of the site's hostnames
// goes to the site's container on its port, verbatim, after the container is
// started if none is running. The simulator serves every site on its one
// endpoint, so it dispatches on the Host header, as Container Apps ingress
// does.
func registerAppServiceFrontEnd(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			site, ok := appServiceSiteByHost(r.Host)
			if !ok || !siteRunsContainer(&site) {
				next.ServeHTTP(w, r)
				return
			}
			serveSiteRequest(w, r, &site)
		})
	})
}

// appServiceSitesByHost indexes sites by the hostnames they answer on. The
// lookup runs in a handler wrapper, so every request into the simulator pays
// it before any handler runs.
var appServiceSitesByHost sim.GenerationIndex[Site]

// appServiceSiteByHost returns the site a request's Host header addresses.
func appServiceSiteByHost(host string) (Site, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || azfSites == nil {
		return Site{}, false
	}
	return appServiceSitesByHost.Lookup(azfSites, host, func(s Site) []string {
		keys := []string{strings.ToLower(s.Properties.DefaultHostName)}
		for _, h := range s.Properties.HostNames {
			if h = strings.ToLower(h); h != keys[0] {
				keys = append(keys, h)
			}
		}
		return keys
	})
}

func serveSiteRequest(w http.ResponseWriter, r *http.Request, site *Site) {
	if r.URL.Path == "/api/function" && !azureFunctionInvokeAuthorized(site, r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	sim.DeclareWait(r.Context(), azureFunctionsHTTPRequestLimit)
	address, err := siteContainerAddress(r.Context(), site)
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
