package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The verbs the load-balancing collections carry beyond their lifecycle:
// rewriting a URL map or a health check whole, checking a URL map's tests
// against its own routing, and invalidating cached content behind one.

// computeTypedWrite applies a client's body to a stored resource of a typed
// collection. A patch keeps the members the client left out; an update drops
// them, keeping only the resource's identity — which is the whole difference
// between the two verbs.
func computeTypedWrite[T any](store sim.Store[T], key string, body map[string]any, replace bool) (bool, error) {
	current, ok := store.Get(key)
	if !ok {
		return false, nil
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return false, err
	}
	var merged map[string]any
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return false, err
	}
	if replace {
		identity := map[string]any{}
		for _, field := range []string{"kind", "id", "name", "selfLink", "creationTimestamp", "region"} {
			if v, held := merged[field]; held {
				identity[field] = v
			}
		}
		merged = identity
	}
	for field, value := range body {
		switch field {
		case "kind", "id", "name", "selfLink", "creationTimestamp":
			// Identity is not writable: a rename would strand the key.
		default:
			merged[field] = value
		}
	}
	encoded, err = json.Marshal(merged)
	if err != nil {
		return false, err
	}
	var updated T
	if err := json.Unmarshal(encoded, &updated); err != nil {
		return false, err
	}
	store.Put(key, updated)
	return true, nil
}

// registerComputeTypedWriteVerbs serves the patch and update a typed global
// collection declares. The two share a body reader and differ only in whether
// the members the client left out survive.
func registerComputeTypedWriteVerbs[T any](srv *sim.Server, collection string, store sim.Store[T], verbs map[string]string) {
	base := "/compute/v1/projects/{project}/global/" + collection
	for method, verb := range verbs {
		method, verb := method, verb
		srv.HandleFunc(method+" "+base+"/{name}", func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			key := computeGlobalLink(project, collection, name)
			found, err := computeTypedWrite(store, key, body, verb == "update")
			if err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s: %v", collection, err)
				return
			}
			if !found {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, verb))
		})
	}
}

// registerComputeURLMapVerbs serves validate, which runs a URL map's tests
// through the very resolver the load-balancer data plane routes with, and
// invalidateCache.
func registerComputeURLMapVerbs(srv *sim.Server) {
	validate := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Resource *ComputeURLMap `json:"resource"`
		}
		if err := sim.ReadJSON(r, &req); err != nil || req.Resource == nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"validate needs the URL map to check in its resource member")
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"result": computeValidateURLMap(*req.Resource),
		})
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/urlMaps/{urlMap}/validate", validate)
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/urlMaps/{urlMap}/validate", validate)

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/urlMaps/{urlMap}/invalidateCache", func(w http.ResponseWriter, r *http.Request) {
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "urlMap")
		key := computeGlobalLink(project, "urlMaps", name)
		if _, ok := gcpURLMaps.Get(key); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "urlMaps %q not found", name)
			return
		}
		var req struct {
			Path string `json:"path"`
			Host string `json:"host"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.Path == "" {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"invalidateCache needs the path to invalidate")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "invalidateCache"))
	})
}

// computeValidateURLMap reports whether a URL map loads and whether its tests
// hold. A test holds when the host and path it names resolve, through the map's
// own routing, to the service it expects — the same resolution the data plane
// performs, so validate cannot disagree with what the load balancer does.
func computeValidateURLMap(urlMap ComputeURLMap) map[string]any {
	var loadErrors []string
	matchers := map[string]bool{}
	for _, matcher := range urlMap.PathMatchers {
		matchers[matcher.Name] = true
	}
	for _, hostRule := range urlMap.HostRules {
		if hostRule.PathMatcher == "" {
			loadErrors = append(loadErrors,
				fmt.Sprintf("host rule for %s names no path matcher", strings.Join(hostRule.Hosts, ", ")))
			continue
		}
		if !matchers[hostRule.PathMatcher] {
			loadErrors = append(loadErrors,
				fmt.Sprintf("path matcher %q named by a host rule does not exist", hostRule.PathMatcher))
		}
	}
	if urlMap.DefaultService == "" {
		for _, matcher := range urlMap.PathMatchers {
			if matcher.DefaultService == "" {
				loadErrors = append(loadErrors,
					fmt.Sprintf("path matcher %q has no default service, and neither has the URL map", matcher.Name))
			}
		}
	}

	result := map[string]any{
		"loadSucceeded": len(loadErrors) == 0,
		"testPassed":    len(loadErrors) == 0,
	}
	if len(loadErrors) > 0 {
		result["loadErrors"] = loadErrors
		// A map that does not load is not routed, so its tests are not run.
		return result
	}

	failures := []any{}
	for _, test := range urlMap.Tests {
		actual := gcpURLMapService(urlMap, test.Host, test.Path)
		if computeSameServiceRef(actual, test.Service) {
			continue
		}
		failures = append(failures, map[string]any{
			"host": test.Host, "path": test.Path,
			"expectedService": test.Service, "actualService": actual,
		})
	}
	if len(failures) > 0 {
		result["testPassed"] = false
		result["testFailures"] = failures
	}
	return result
}

// computeSameServiceRef compares two references to a backend service. A client
// may name one by full URL, by the compute-relative path, or by name alone,
// and all three denote the same service.
func computeSameServiceRef(a, b string) bool {
	trim := func(ref string) string {
		ref = strings.TrimPrefix(ref, "https://www.googleapis.com/compute/v1/")
		if i := strings.LastIndex(ref, "/"); i >= 0 {
			return ref[i+1:]
		}
		return ref
	}
	if a == "" || b == "" {
		return a == b
	}
	return trim(a) == trim(b)
}
