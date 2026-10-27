package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// The AWS Amplify Hosting deployment specification: a bundle whose root
// carries deploy-manifest.json (version 1) routes requests to Static targets
// under static/ or to Compute targets, each a node entrypoint in
// compute/{name}/ listening on port 3000.
//
// Run one compute container per branch's active deployment, started lazily on
// the first Compute-routed request and replaced on the first request after a
// new deployment. A persistent simulator restart reclaims the running
// container, its port and its extracted bundle.

type amplifyDeployManifest struct {
	Version          int                           `json:"version"`
	Framework        *amplifyManifestFramework     `json:"framework,omitempty"`
	Routes           []amplifyManifestRoute        `json:"routes"`
	ComputeResources []amplifyManifestComputeEntry `json:"computeResources,omitempty"`
	ImageSettings    json.RawMessage               `json:"imageSettings,omitempty"`
	routeRegexps     map[string]*regexp.Regexp
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
	Runtime    string `json:"runtime"`
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

// routeMatches compiles on the spot a pattern outside the manifest's routes.
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

// In the specification's globs ("/api/*", "/*.*", "/*") `*` also matches
// separators. Quoting every other byte means compilation cannot fail.
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

const amplifyComputePort = 3000

type amplifyComputeInstance struct {
	jobID     string
	hostPort  int
	bundleDir string
	handle    placement.Handle
}

// amplifyComputeBackend forwards the container's loopback, where the
// container credentials endpoint serves the compute role's sessions.
var amplifyComputeBackend placement.Backend = placement.ContainerBackend{}

var (
	amplifyComputeMu        sync.Mutex
	amplifyComputeInstances = map[string]*amplifyComputeInstance{} // appID/branch
)

func recoverAmplifyComputeInstances() error {
	if !sim.HasPersistentWorkloadIdentity() {
		return nil
	}
	existing, err := amplifyComputeBackend.Find(context.Background(), map[string]string{"sockerless-amplify-compute": ""})
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
		handle, err := amplifyComputeBackend.Adopt(context.Background(), workload.ID, sim.ContainerConfig{}, sim.NoopSink{})
		if err != nil {
			return fmt.Errorf("adopt AWS Amplify Hosting compute container %s: %w", workload.ID, err)
		}
		if !workload.Running {
			// A compute that went down restarts on the branch's next request, from the
			// deployment active then.
			handle.Cancel()
			revokeRoleCredentials(roleHolder("amplify", key))
			if dir := workload.Labels["sockerless-amplify-bundle-dir"]; dir != "" {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		hostPort, err := handle.PublishedPort(context.Background(), amplifyComputePort)
		if err != nil {
			handle.Cancel()
			return fmt.Errorf("adopt AWS Amplify Hosting compute container %s: %w", workload.ID, err)
		}
		if err := amplifyWaitForPort(handle, hostPort, 60*time.Second); err != nil {
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

// CleanupContainers covers simulator shutdown.
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
	revokeRoleCredentials(roleHolder("amplify", key))
	if instance.bundleDir != "" {
		_ = os.RemoveAll(instance.bundleDir)
	}
}

// List the runtimes AWS Amplify Hosting accepts rather than composing a tag
// from any "nodejsNN.x": the service rejects other versions, and CI reads
// this list to warm the images. Pull through mirror.gcr.io, never Docker Hub
// directly.
func amplifyComputeRuntimeImage(runtimeName string) (string, error) {
	switch runtimeName {
	case "nodejs18.x":
		return "mirror.gcr.io/library/node:18-alpine", nil
	case "nodejs20.x":
		return "mirror.gcr.io/library/node:20-alpine", nil
	}
	return "", fmt.Errorf("compute runtime %q not supported (expected nodejs18.x or nodejs20.x)", runtimeName)
}

func amplifyEnsureCompute(app AmplifyApp, br AmplifyBranch, content *amplifyHostedContent, compute amplifyManifestComputeEntry) (int, error) {
	key := app.AppId + "/" + br.BranchName
	amplifyComputeMu.Lock()
	defer amplifyComputeMu.Unlock()
	if instance := amplifyComputeInstances[key]; instance != nil {
		if instance.jobID == content.JobID {
			return instance.hostPort, nil
		}
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
	env := map[string]string{}
	for k, v := range app.EnvironmentVariables {
		env[k] = v
	}
	for k, v := range br.EnvironmentVariables {
		env[k] = v
	}
	env["PORT"] = strconv.Itoa(amplifyComputePort)
	// The branch's compute role wins over the app's; the compute's SDKs read the
	// role's sessions from the container credentials endpoint.
	computeRole := firstNonEmpty(br.ComputeRoleArn, app.ComputeRoleArn)
	network, err := awsPlatformNetwork(context.Background())
	if err != nil {
		_ = os.RemoveAll(bundleDir)
		return 0, fmt.Errorf("compute network: %w", err)
	}
	holder := roleHolder("amplify", key)
	credentials := grantContainerCredentials(amplifyComputeBackend, network, holder, computeRole, app.AppId)

	handle, err := credentials.place(context.Background(), amplifyComputeBackend, sim.ContainerConfig{
		Image:        image,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"node", compute.Entrypoint},
		WorkingDir:   "/bundle/compute/" + compute.Name,
		// `:z` lets an SELinux-confined workload read the bundle; Docker ignores it
		// elsewhere.
		Binds:   []string{bundleDir + ":/bundle:ro,z"},
		Env:     workloadhost.MergeEnv(env, credentials.Env),
		Network: network.Name,
		DNS:     network.DNS(),
		Labels: map[string]string{
			"sockerless-amplify-compute":    key,
			"sockerless-amplify-job-id":     content.JobID,
			"sockerless-amplify-bundle-dir": bundleDir,
		},
		PublishPorts: []int{amplifyComputePort},
		Sandbox:      SandboxFargate,
	}, sim.NoopSink{})
	if err != nil {
		revokeRoleCredentials(holder)
		_ = os.RemoveAll(bundleDir)
		return 0, fmt.Errorf("start compute container: %w", err)
	}
	hostPort, err := handle.PublishedPort(context.Background(), amplifyComputePort)
	if err != nil {
		handle.Cancel()
		revokeRoleCredentials(holder)
		_ = os.RemoveAll(bundleDir)
		return 0, fmt.Errorf("start compute container: %w", err)
	}
	if err := amplifyWaitForPort(handle, hostPort, 60*time.Second); err != nil {
		handle.Cancel()
		revokeRoleCredentials(holder)
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

// Require response bytes, not a TCP accept: Podman's gvproxy binds the
// published port at once and resets connections until the workload listens.
// A listening port raises no event, so retry a refused probe every 100 ms.
func amplifyWaitForPort(handle placement.Handle, port int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
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
	retry := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	for {
		err := probe()
		if err == nil {
			return nil
		}
		select {
		case <-handle.Exited():
			return fmt.Errorf("the compute container exited: %w", err)
		case <-deadline.C:
			return err
		case <-retry.C:
		}
	}
}

// The first matching route wins; on a 404 a GET without a body falls back to
// the route's fallback target.
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

// With interceptNotFound, a target that would answer 404 writes nothing and
// reports unhandled so the fallback target applies.
func amplifyServeManifestTarget(w http.ResponseWriter, r *http.Request, app AmplifyApp, br AmplifyBranch, content *amplifyHostedContent, target *amplifyManifestTarget, interceptNotFound bool) bool {
	switch target.Kind {
	case "Static":
		// The specification places compute-bundle static assets under static/.
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
		return amplifyProxyToCompute(w, r, port, interceptNotFound)
	case "ImageOptimization":
		return amplifyServeImageOptimization(w, r, app, br, content, target, interceptNotFound)
	default:
		http.Error(w, "deploy-manifest target kind "+target.Kind+" not supported", http.StatusBadGateway)
		return true
	}
}

// With interceptNotFound, discard a compute 404 and report it unserved so the
// fallback target applies.
func amplifyProxyToCompute(w http.ResponseWriter, r *http.Request, port int, interceptNotFound bool) bool {
	const exchangeTimeout = 30 * time.Second
	sim.DeclareWait(r.Context(), exchangeTimeout)
	err := lbplane.Serve(w, r, lbplane.Upstream{
		Scheme:   "http",
		Address:  net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Timeout:  exchangeTimeout,
		Decline: func(status int) bool {
			return interceptNotFound && status == http.StatusNotFound
		},
	}, lbplane.Answers{
		Failed: func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadGateway) },
	})
	return !errors.Is(err, lbplane.ErrDeclined)
}
