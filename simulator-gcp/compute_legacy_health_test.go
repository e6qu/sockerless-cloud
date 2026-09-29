package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// gcpHealthBackend is an instance's HTTP server whose health path answers 200
// while healthy is set, and which records the Host each probe sent.
type gcpHealthBackend struct {
	healthy atomic.Bool
	mu      sync.Mutex
	hosts   []string
	ip      string
	port    int
}

func newGCPHealthBackend(t *testing.T) *gcpHealthBackend {
	t.Helper()
	b := &gcpHealthBackend{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		b.mu.Lock()
		b.hosts = append(b.hosts, r.Host)
		b.mu.Unlock()
		if !b.healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	b.ip = host
	if b.port, err = strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *gcpHealthBackend) lastHost() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.hosts) == 0 {
		return ""
	}
	return b.hosts[len(b.hosts)-1]
}

type gcpHealthHarness struct {
	t   *testing.T
	srv *sim.Server
	now time.Time
}

// newGCPHealthHarness builds the simulator with its background health checker
// stopped, so the test decides when each probe interval elapses.
func newGCPHealthHarness(t *testing.T) *gcpHealthHarness {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	srv.StopBackground()
	gcpBackendHealth.Reset()
	t.Cleanup(gcpBackendHealth.Reset)
	return &gcpHealthHarness{t: t, srv: srv, now: time.Now()}
}

func (h *gcpHealthHarness) do(method, path string, body any) map[string]any {
	h.t.Helper()
	var payload string
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		payload = string(raw)
	}
	req := httptest.NewRequest(method, "http://compute.googleapis.com"+path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return out
}

// sweep runs the health checker once per checkIntervalSec, n times.
func (h *gcpHealthHarness) sweep(n int) {
	for range n {
		gcpSweepBackendHealth(context.Background(), h.now)
		h.now = h.now.Add(5 * time.Second)
	}
}

func (h *gcpHealthHarness) instance(project, zone, name, ip string) string {
	selfLink := fmt.Sprintf("projects/%s/zones/%s/instances/%s", project, zone, name)
	gcpInstances.Put(selfLink, ComputeInstance{
		Name:              name,
		SelfLink:          selfLink,
		NetworkInterfaces: []ComputeNetworkInterface{{Name: "nic0", NetworkIP: ip}},
	})
	h.t.Cleanup(func() { gcpInstances.Delete(selfLink) })
	return "https://www.googleapis.com/compute/v1/" + selfLink
}

func healthStates(t *testing.T, resp map[string]any) []string {
	t.Helper()
	statuses, _ := resp["healthStatus"].([]any)
	states := make([]string, 0, len(statuses))
	for _, raw := range statuses {
		entry, _ := raw.(map[string]any)
		state, _ := entry["healthState"].(string)
		states = append(states, state)
	}
	return states
}

// targetPools.getHealth reports what the pool's legacy HTTP health check found:
// a member enters service after healthyThreshold consecutive 200s, leaves it
// after unhealthyThreshold failures, and the check sends the address of the
// forwarding rule it checks on behalf of as its Host. A pool naming no health
// check holds its members healthy.
func TestComputeTargetPoolHealthFollowsItsLegacyHTTPHealthCheck(t *testing.T) {
	h := newGCPHealthHarness(t)
	backend := newGCPHealthBackend(t)
	const project, region = "pool-health", "us-central1"
	instance := h.instance(project, "us-central1-a", "web-1", backend.ip)

	h.do(http.MethodPost, "/compute/v1/projects/"+project+"/global/httpHealthChecks", map[string]any{
		"name": "legacy", "port": backend.port, "requestPath": "/healthz",
		"checkIntervalSec": 5, "timeoutSec": 2, "healthyThreshold": 2, "unhealthyThreshold": 2,
	})
	h.do(http.MethodPost, "/compute/v1/projects/"+project+"/regions/"+region+"/targetPools", map[string]any{
		"name":         "web",
		"instances":    []string{instance},
		"healthChecks": []string{"https://www.googleapis.com/compute/v1/projects/" + project + "/global/httpHealthChecks/legacy"},
	})
	h.do(http.MethodPost, "/compute/v1/projects/"+project+"/regions/"+region+"/targetPools", map[string]any{
		"name": "unchecked", "instances": []string{instance},
	})
	h.do(http.MethodPost, "/compute/v1/projects/"+project+"/regions/"+region+"/forwardingRules", map[string]any{
		"name": "web-rule", "IPAddress": "203.0.113.7",
		"target": "https://www.googleapis.com/compute/v1/projects/" + project + "/regions/" + region + "/targetPools/web",
	})

	getHealth := func(pool string) map[string]any {
		return h.do(http.MethodPost, "/compute/v1/projects/"+project+"/regions/"+region+"/targetPools/"+pool+"/getHealth",
			map[string]string{"instance": instance})
	}
	state := func() string {
		states := healthStates(t, getHealth("web"))
		if len(states) != 1 {
			t.Fatalf("health states = %v, want one", states)
		}
		return states[0]
	}

	backend.healthy.Store(true)
	h.sweep(1)
	if got := state(); got != "UNHEALTHY" {
		t.Fatalf("after one passing probe of two: %s", got)
	}
	h.sweep(1)
	resp := getHealth("web")
	if got := healthStates(t, resp); len(got) != 1 || got[0] != "HEALTHY" {
		t.Fatalf("after healthyThreshold passing probes: %v", got)
	}
	entry := resp["healthStatus"].([]any)[0].(map[string]any)
	if entry["instance"] != instance || entry["ipAddress"] != backend.ip {
		t.Fatalf("health status = %v, want instance %s at %s", entry, instance, backend.ip)
	}
	if host := backend.lastHost(); host != "203.0.113.7" {
		t.Fatalf("the legacy check sent Host %q, want the forwarding rule's address", host)
	}

	backend.healthy.Store(false)
	h.sweep(1)
	if got := state(); got != "HEALTHY" {
		t.Fatalf("one failure below unhealthyThreshold took the member out: %s", got)
	}
	h.sweep(1)
	if got := state(); got != "UNHEALTHY" {
		t.Fatalf("after unhealthyThreshold failures: %s", got)
	}

	if got := healthStates(t, getHealth("unchecked")); len(got) != 1 || got[0] != "HEALTHY" {
		t.Fatalf("a pool naming no health check reports %v", got)
	}
}

// regionBackendServices.getHealth reports what the service's regional health
// check found for the backends of the named group, the same way the global
// backend services report.
func TestComputeRegionBackendServiceHealthFollowsItsHealthCheck(t *testing.T) {
	h := newGCPHealthHarness(t)
	backend := newGCPHealthBackend(t)
	const project, region, zone = "region-health", "us-central1", "us-central1-a"
	instance := h.instance(project, zone, "api-1", backend.ip)
	groupPath := "projects/" + project + "/zones/" + zone + "/instanceGroups/api"
	gcpInstanceGroups.Put(groupPath, storedComputeInstanceGroup{
		ComputeInstanceGroup: ComputeInstanceGroup{
			Name:       "api",
			SelfLink:   groupPath,
			NamedPorts: []ComputeInstanceGroupNamedPort{{Name: "http", Port: int64(backend.port)}},
		},
		Instances: []ComputeInstanceGroupInstance{{Instance: strings.TrimPrefix(instance, "https://www.googleapis.com/compute/v1/")}},
	})
	t.Cleanup(func() { gcpInstanceGroups.Delete(groupPath) })

	h.do(http.MethodPost, "/compute/v1/projects/"+project+"/regions/"+region+"/healthChecks", map[string]any{
		"name": "api-check", "type": "HTTP", "checkIntervalSec": 5, "timeoutSec": 2,
		"healthyThreshold": 2, "unhealthyThreshold": 2,
		"httpHealthCheck": map[string]any{"portSpecification": "USE_SERVING_PORT", "requestPath": "/healthz"},
	})
	h.do(http.MethodPost, "/compute/v1/projects/"+project+"/regions/"+region+"/backendServices", map[string]any{
		"name": "api", "protocol": "HTTP", "portName": "http", "loadBalancingScheme": "INTERNAL_MANAGED",
		"healthChecks": []string{"https://www.googleapis.com/compute/v1/projects/" + project + "/regions/" + region + "/healthChecks/api-check"},
		"backends":     []map[string]any{{"group": "https://www.googleapis.com/compute/v1/" + groupPath}},
	})
	getHealth := func() []string {
		return healthStates(t, h.do(http.MethodPost,
			"/compute/v1/projects/"+project+"/regions/"+region+"/backendServices/api/getHealth",
			map[string]string{"group": "https://www.googleapis.com/compute/v1/" + groupPath}))
	}

	backend.healthy.Store(true)
	h.sweep(1)
	if got := getHealth(); len(got) != 1 || got[0] != "UNHEALTHY" {
		t.Fatalf("after one passing probe of two: %v", got)
	}
	h.sweep(1)
	if got := getHealth(); len(got) != 1 || got[0] != "HEALTHY" {
		t.Fatalf("after healthyThreshold passing probes: %v", got)
	}
	backend.healthy.Store(false)
	h.sweep(2)
	if got := getHealth(); len(got) != 1 || got[0] != "UNHEALTHY" {
		t.Fatalf("after unhealthyThreshold failures: %v", got)
	}
}
