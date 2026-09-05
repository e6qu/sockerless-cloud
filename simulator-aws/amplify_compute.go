package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amplify Hosting SSR (WEB_COMPUTE) execution, per the published Amplify
// Hosting deployment specification: a deployed bundle whose root carries
// `deploy-manifest.json` (version 1) routes requests through routes[] to
// "Static" targets (served from the bundle's `static/` directory) or
// "Compute" targets (proxied to the compute resource's HTTP server). Each
// computeResource is `compute/{name}/` in the bundle with a node entrypoint
// that listens on PORT=3000 (the spec's compute port convention).
//
// The sim runs one long-lived compute container per branch active
// deployment, started lazily on the first request that routes to a Compute
// target (deploys that are never browsed start no container). A new
// deployment replaces the container on its next request; branch/app deletes
// stop it. Persistent simulator restarts reclaim the same running compute
// container, published port, and extracted deployment bundle.

type amplifyDeployManifest struct {
	Version          int                           `json:"version"`
	Framework        *amplifyManifestFramework     `json:"framework,omitempty"`
	Routes           []amplifyManifestRoute        `json:"routes"`
	ComputeResources []amplifyManifestComputeEntry `json:"computeResources,omitempty"`
	ImageSettings    json.RawMessage               `json:"imageSettings,omitempty"`
	// routeRegexps caches each route path pattern's compiled matcher, built
	// once when the manifest is parsed instead of per request.
	routeRegexps map[string]*regexp.Regexp
}

type amplifyManifestFramework struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type amplifyManifestRoute struct {
	Path     string                 `json:"path"`
	Target   *amplifyManifestTarget `json:"target"`
	Fallback *amplifyManifestTarget `json:"fallback,omitempty"`
}

type amplifyManifestTarget struct {
	Kind         string `json:"kind"` // Static | Compute | ImageOptimization
	Src          string `json:"src,omitempty"`
	CacheControl string `json:"cacheControl,omitempty"`
}

type amplifyManifestComputeEntry struct {
	Name       string `json:"name"`
	Runtime    string `json:"runtime"` // e.g. nodejs18.x
	Entrypoint string `json:"entrypoint"`
}

func amplifyParseDeployManifest(data []byte) (*amplifyDeployManifest, error) {
	var manifest amplifyDeployManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("invalid deploy-manifest.json: %w", err)
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("deploy-manifest.json version %d not supported (spec version 1)", manifest.Version)
	}
	if len(manifest.Routes) == 0 {
		return nil, fmt.Errorf("deploy-manifest.json has no routes")
	}
	manifest.routeRegexps = map[string]*regexp.Regexp{}
	for _, route := range manifest.Routes {
		manifest.routeRegexps[route.Path] = amplifyCompileRoutePattern(route.Path)
	}
	return &manifest, nil
}

// routeMatches matches a manifest route path pattern against a request path,
// through the matcher compiled at parse time (compiling on the spot for a
// pattern outside the manifest's routes).
func (m *amplifyDeployManifest) routeMatches(pattern, reqPath string) bool {
	if re := m.routeRegexps[pattern]; re != nil {
		return re.MatchString(reqPath)
	}
	return amplifyCompileRoutePattern(pattern).MatchString(reqPath)
}

func (m *amplifyDeployManifest) computeResource(name string) (amplifyManifestComputeEntry, bool) {
	for _, c := range m.ComputeResources {
		if c.Name == name || name == "" {
			return c, true
		}
	}
	return amplifyManifestComputeEntry{}, false
}

// amplifyCompileRoutePattern compiles a route path pattern. The spec's
// patterns are simple globs ("/api/*", "/*.*", "/*") where `*` matches any
// characters, including separators — the catch-all "/*" matches every path.
// Every non-wildcard byte is quoted, so compilation cannot fail.
func amplifyCompileRoutePattern(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i, part := range strings.Split(pattern, "*") {
		if i > 0 {
			b.WriteString(".*")
		}
		b.WriteString(regexp.QuoteMeta(part))
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// amplifyComputePort is the container-side port the deployment spec requires
// compute entrypoints to listen on.
const amplifyComputePort = 3000

type amplifyComputeInstance struct {
	jobID     string
	hostPort  int
	bundleDir string
	handle    *sim.ContainerHandle
}

var (
	amplifyComputeMu        sync.Mutex
	amplifyComputeInstances = map[string]*amplifyComputeInstance{} // appID/branch
)

func recoverAmplifyComputeInstances() error {
	if !sim.HasPersistentWorkloadIdentity() {
		return nil
	}
	existing, err := sim.FindExistingContainers(map[string]string{"sockerless-amplify-compute": ""})
	if err != nil {
		return fmt.Errorf("find AWS Amplify Hosting compute containers: %w", err)
	}
	amplifyComputeMu.Lock()
	defer amplifyComputeMu.Unlock()
	for _, workload := range existing {
		key := workload.Labels["sockerless-amplify-compute"]
		if key == "" {
			return fmt.Errorf("AWS Amplify Hosting compute container %s has no deployment identity", workload.ID)
		}
		if _, duplicate := amplifyComputeInstances[key]; duplicate {
			return fmt.Errorf("AWS Amplify Hosting deployment %s has multiple compute containers", key)
		}
		jobID := workload.Labels["sockerless-amplify-job-id"]
		if jobID == "" {
			parts := strings.SplitN(key, "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("AWS Amplify Hosting compute identity %q is invalid", key)
			}
			job, ok := amplifyLatestSucceededJob(parts[0], parts[1])
			if !ok {
				return fmt.Errorf("AWS Amplify Hosting compute container %s has no active deployment", workload.ID)
			}
			jobID = job.Job.Summary.JobId
		}
		hostPort := workload.PublishedPorts[amplifyComputePort]
		if hostPort == 0 {
			return fmt.Errorf("AWS Amplify Hosting compute container %s has no published port %d", workload.ID, amplifyComputePort)
		}
		if !workload.Running {
			if err := sim.StartExistingContainer(workload.ID); err != nil {
				return fmt.Errorf("restart AWS Amplify Hosting compute container %s: %w", workload.ID, err)
			}
		}
		handle, err := sim.AdoptContainer(workload.ID, sim.ContainerConfig{}, sim.NoopSink{})
		if err != nil {
			return fmt.Errorf("adopt AWS Amplify Hosting compute container %s: %w", workload.ID, err)
		}
		if err := amplifyWaitForPort(hostPort, 60*time.Second); err != nil {
			handle.Cancel()
			return fmt.Errorf("AWS Amplify Hosting compute container %s did not resume: %w", workload.ID, err)
		}
		amplifyComputeInstances[key] = &amplifyComputeInstance{
			jobID: jobID, hostPort: hostPort,
			bundleDir: workload.Labels["sockerless-amplify-bundle-dir"],
			handle:    handle,
		}
	}
	return nil
}

// amplifyStopCompute stops and forgets a branch's compute container. Called
// on branch/app delete; simulator shutdown is covered by CleanupContainers.
func amplifyStopCompute(appID, branch string) {
	amplifyComputeMu.Lock()
	defer amplifyComputeMu.Unlock()
	amplifyStopComputeLocked(appID + "/" + branch)
}

func amplifyStopComputeLocked(key string) {
	instance := amplifyComputeInstances[key]
	if instance == nil {
		return
	}
	delete(amplifyComputeInstances, key)
	if instance.handle != nil {
		instance.handle.Cancel()
	}
	if instance.bundleDir != "" {
		_ = os.RemoveAll(instance.bundleDir)
	}
}

// amplifyComputeRuntimeImage maps the manifest's runtime ("nodejs18.x") to
// the node container image (via the ECR Public Docker Hub mirror — repo
// policy: no direct Docker Hub pulls).
//
// The runtimes are listed rather than derived from the name. AWS Amplify
// Hosting accepts a published set, so composing a tag out of any "nodejsNN.x"
// would answer for versions the service rejects and name an image the mirror
// does not carry. Spelling the images out also lets them be read off the
// source, which is how CI knows what to warm.
func amplifyComputeRuntimeImage(runtimeName string) (string, error) {
	switch runtimeName {
	case "nodejs18.x":
		return "public.ecr.aws/docker/library/node:18-alpine", nil
	case "nodejs20.x":
		return "public.ecr.aws/docker/library/node:20-alpine", nil
	}
	return "", fmt.Errorf("compute runtime %q not supported (expected nodejs18.x or nodejs20.x)", runtimeName)
}

// amplifyEnsureCompute returns the host port of the running compute
// container for the branch's active deployment, starting (or replacing) it
// if needed.
func amplifyEnsureCompute(app AmplifyApp, br AmplifyBranch, content *amplifyHostedContent, compute amplifyManifestComputeEntry) (int, error) {
	key := app.AppId + "/" + br.BranchName
	amplifyComputeMu.Lock()
	defer amplifyComputeMu.Unlock()
	if instance := amplifyComputeInstances[key]; instance != nil {
		if instance.jobID == content.JobID {
			return instance.hostPort, nil
		}
		// New active deployment: replace the container.
		amplifyStopComputeLocked(key)
	}

	image, err := amplifyComputeRuntimeImage(compute.Runtime)
	if err != nil {
		return 0, err
	}
	bundleDir, err := amplifyExtractBundle(content.Files)
	if err != nil {
		return 0, err
	}
	hostPort, err := amplifyFreeLoopbackPort()
	if err != nil {
		_ = os.RemoveAll(bundleDir)
		return 0, err
	}

	env := map[string]string{}
	for k, v := range app.EnvironmentVariables {
		env[k] = v
	}
	for k, v := range br.EnvironmentVariables {
		env[k] = v
	}
	env["PORT"] = strconv.Itoa(amplifyComputePort)

	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        image,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"node", compute.Entrypoint},
		WorkingDir:   "/bundle/compute/" + compute.Name,
		// The deployed bundle is immutable to compute. The shared SELinux
		// relabel lets the confined workload read the real host directory on
		// enforcing hosts and is accepted as a no-op by Docker elsewhere.
		Binds: []string{bundleDir + ":/bundle:ro,z"},
		Env:   env,
		Labels: map[string]string{
			"sockerless-amplify-compute":    key,
			"sockerless-amplify-job-id":     content.JobID,
			"sockerless-amplify-bundle-dir": bundleDir,
		},
		PublishPorts: map[int]int{amplifyComputePort: hostPort},
		Sandbox:      SandboxFargate,
	}, sim.NoopSink{})
	if err != nil {
		_ = os.RemoveAll(bundleDir)
		return 0, fmt.Errorf("start compute container: %w", err)
	}
	if err := amplifyWaitForPort(hostPort, 60*time.Second); err != nil {
		handle.Cancel()
		_ = os.RemoveAll(bundleDir)
		return 0, fmt.Errorf("compute entrypoint %s never listened on PORT %d: %w", compute.Entrypoint, amplifyComputePort, err)
	}
	amplifyComputeInstances[key] = &amplifyComputeInstance{
		jobID:     content.JobID,
		hostPort:  hostPort,
		bundleDir: bundleDir,
		handle:    handle,
	}
	return hostPort, nil
}

// amplifyExtractBundle writes the unpacked deployment to a temp dir so the
// compute container can bind-mount it.
func amplifyExtractBundle(files map[string][]byte) (string, error) {
	dir, err := os.MkdirTemp("", "sockerless-amplify-compute-*")
	if err != nil {
		return "", err
	}
	for name, data := range files {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if !strings.HasPrefix(dest, dir+string(os.PathSeparator)) {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("bundle path escapes extraction dir: %s", name)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func amplifyFreeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return 0, fmt.Errorf("unexpected listener address type %T", ln.Addr())
	}
	port := addr.Port
	_ = ln.Close()
	return port, nil
}

// amplifyWaitForPort blocks until the compute server answers an HTTP
// request (bounded cold-start health wait before the first proxy). A bare
// TCP accept is not enough: Podman's gvproxy binds the published host port
// immediately and resets connections until the container backend listens,
// so the wait requires actual response bytes from the entrypoint.
func amplifyWaitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	probe := func() error {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte("HEAD / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")); err != nil {
			return err
		}
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err != nil {
			return err
		}
		return nil
	}
	for {
		err := probe()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// amplifyServeManifestRoutes serves a WEB_COMPUTE request through the deploy
// manifest's routes[]: first matching route wins; a target that produces a
// 404 falls back to the route's fallback target (the spec supports fallbacks
// for GET requests without a body).
func amplifyServeManifestRoutes(w http.ResponseWriter, r *http.Request, app AmplifyApp, br AmplifyBranch, content *amplifyHostedContent) {
	for _, route := range content.Manifest.Routes {
		if route.Target == nil || !content.Manifest.routeMatches(route.Path, r.URL.Path) {
			continue
		}
		interceptNotFound := route.Fallback != nil && r.Method == http.MethodGet && r.ContentLength <= 0
		if amplifyServeManifestTarget(w, r, app, br, content, route.Target, interceptNotFound) {
			return
		}
		if route.Fallback != nil && amplifyServeManifestTarget(w, r, app, br, content, route.Fallback, false) {
			return
		}
		http.NotFound(w, r)
		return
	}
	http.NotFound(w, r)
}

// amplifyServeManifestTarget attempts one route target; reports whether the
// request was handled. With interceptNotFound, a target whose response would
// be a 404 writes nothing and reports unhandled so the route's fallback
// target applies.
func amplifyServeManifestTarget(w http.ResponseWriter, r *http.Request, app AmplifyApp, br AmplifyBranch, content *amplifyHostedContent, target *amplifyManifestTarget, interceptNotFound bool) bool {
	switch target.Kind {
	case "Static":
		// The spec places compute-bundle static assets under static/.
		key, ok := amplifyResolveFile(content.Files, "static"+path.Clean("/"+r.URL.Path))
		if !ok {
			return false
		}
		if target.CacheControl != "" {
			w.Header().Set("Cache-Control", target.CacheControl)
		}
		amplifyWriteFile(w, key, content.Files[key], http.StatusOK)
		return true
	case "Compute":
		compute, ok := content.Manifest.computeResource(target.Src)
		if !ok {
			http.Error(w, "deploy-manifest names no compute resource "+target.Src, http.StatusBadGateway)
			return true
		}
		port, err := amplifyEnsureCompute(app, br, content, compute)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		served, err := amplifyProxyToCompute(w, r, port, interceptNotFound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		return served
	case "ImageOptimization":
		return amplifyServeImageOptimization(w, r, app, br, content, target, interceptNotFound)
	default:
		http.Error(w, "deploy-manifest target kind "+target.Kind+" not supported", http.StatusBadGateway)
		return true
	}
}

// amplifyProxyToCompute forwards the request to the compute container. With
// interceptNotFound, a 404 from compute is discarded and reported unserved
// (nothing written) so the caller's fallback target applies; every other
// response streams through untouched.
func amplifyProxyToCompute(w http.ResponseWriter, r *http.Request, port int, interceptNotFound bool) (bool, error) {
	upstreamURL := url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
	}
	// An upgraded connection is long-lived, so the request deadline that bounds an
	// ordinary proxied request must not be imposed on it.
	ctx := r.Context()
	upgrade := sim.IsUpgradeRequest(r)
	if !upgrade {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return false, err
	}
	req.Header = r.Header.Clone()
	req.Host = r.Host
	client := http.Client{CheckRedirect: returnRedirectsToClient}
	if !upgrade {
		client.Timeout = 30 * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("forward to compute %s: %w", upstreamURL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return true, sim.TunnelUpgradedResponse(w, resp)
	}
	if interceptNotFound && resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return true, err
}
