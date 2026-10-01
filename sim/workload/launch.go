// Package workload holds what every simulator does to launch and reach a
// workload container: reading the platform off the image, picking a host
// port, waiting for the workload's listener, posting to its bootstrap,
// starting a main container with sidecars in its network namespace, and
// building images with the host's docker CLI.
package workload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// LocalImagePlatform reports the "os/architecture" of image, pulling it with
// registryAuth — the credential the workload host holds for the image's
// registry, empty for an anonymous pull — when the engine does not hold it yet.
func LocalImagePlatform(ctx context.Context, image, registryAuth string) (string, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	inspect, err := cli.ImageInspect(ctx, image)
	if err != nil {
		if pullErr := sim.PullImageWithCredential(ctx, image, "", registryAuth); pullErr != nil {
			return "", fmt.Errorf("inspect image %q platform: %w; pull image: %w", image, err, pullErr)
		}
		inspect, err = cli.ImageInspect(ctx, image)
		if err != nil {
			return "", fmt.Errorf("inspect pulled image %q platform: %w", image, err)
		}
	}
	if inspect.Os == "" || inspect.Architecture == "" {
		return "", fmt.Errorf("inspect image %q platform: missing os/architecture", image)
	}
	return inspect.Os + "/" + inspect.Architecture, nil
}

// FirstReachable polls the candidate URLs, each round in order, and returns
// the first whose host accepts a TCP connection before timeout. A URL without
// a port is dialled on its scheme's default port.
func FirstReachable(ctx context.Context, cands []string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		for _, cand := range cands {
			addr, err := dialAddress(cand)
			if err != nil {
				lastErr = err
				continue
			}
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				_ = conn.Close()
				return cand, nil
			}
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout after %s", timeout)
	}
	return "", lastErr
}

func dialAddress(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("url %q has no host", raw)
	}
	if parsed.Port() != "" {
		return parsed.Host, nil
	}
	port := "80"
	if parsed.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(parsed.Hostname(), port), nil
}

// ExitCodeHeader carries the exit status of the handler process a bootstrap
// ran for one invocation.
const ExitCodeHeader = "X-Sockerless-Exit-Code"

// bootstrapConnectWindow bounds how long PostBootstrap keeps retrying a
// bootstrap whose listener refuses connections while it finishes starting.
const bootstrapConnectWindow = 30 * time.Second

// PostBootstrap POSTs body to a workload's bootstrap URL, retrying while the
// connection fails, and returns the response body with the invocation's exit
// code: ExitCodeHeader when the bootstrap set it, otherwise 1 for an HTTP
// error status and 0 for success. timeout bounds each attempt.
func PostBootstrap(ctx context.Context, bootstrapURL string, body io.Reader, contentType string, timeout time.Duration) ([]byte, int, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, -1, fmt.Errorf("read invoke body: %w", err)
		}
	}
	if contentType == "" {
		contentType = "application/json"
	}
	httpClient := &http.Client{Timeout: timeout}
	deadline := time.Now().Add(bootstrapConnectWindow)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, bootstrapURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, -1, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := httpClient.Do(req)
		if err == nil {
			respBytes, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, -1, fmt.Errorf("read bootstrap response: %w", readErr)
			}
			return respBytes, exitCode(resp), nil
		}
		if time.Now().After(deadline) {
			return nil, -1, fmt.Errorf("invoke bootstrap: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func exitCode(resp *http.Response) int {
	if hdr := resp.Header.Get(ExitCodeHeader); hdr != "" {
		if n, err := strconv.Atoi(hdr); err == nil {
			return n
		}
	}
	if resp.StatusCode >= 400 {
		return 1
	}
	return 0
}
