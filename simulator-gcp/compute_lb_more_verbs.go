package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The global load-balancing verbs beyond compute_lb_verbs.go: a backend
// service's Cloud Armor policies, a forwarding rule's target and labels, and a
// proxy's URL map. Each goes through computeTypedWrite, as patch and update do.

// registerComputeGlobalLBVerbs writes every route literally, since the surface
// tables cannot see a route assembled from a variable.
func registerComputeGlobalLBVerbs(srv *sim.Server) {
	// setVerb applies one member of the request body to a stored resource.
	setVerb := func(collection, verb, member, into string, apply func(string, map[string]any) (bool, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			value, sent := body[member]
			if !sent {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"%s needs %s in its request body", verb, member)
				return
			}
			key := computeGlobalLink(project, collection, name)
			found, err := apply(key, map[string]any{into: value})
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s: %v", collection, err)
				return
			}
			if !found {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, verb))
		}
	}

	backend := func(key string, patch map[string]any) (bool, error) {
		return computeTypedWrite(gcpBackendServices, key, patch, false)
	}
	rule := func(key string, patch map[string]any) (bool, error) {
		return computeTypedWrite(gcpForwardingRules, key, patch, false)
	}
	httpProxy := func(key string, patch map[string]any) (bool, error) {
		return computeTypedWrite(gcpTargetHTTPProxies, key, patch, false)
	}

	// A backend service sits behind a Cloud Armor policy at the origin and, for
	// cached content, one at the edge; both arrive as a SecurityPolicyReference.
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices/{name}/setSecurityPolicy",
		setVerb("backendServices", "setSecurityPolicy", "securityPolicy", "securityPolicy", backend))
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices/{name}/setEdgeSecurityPolicy",
		setVerb("backendServices", "setEdgeSecurityPolicy", "securityPolicy", "edgeSecurityPolicy", backend))

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/forwardingRules/{name}/setTarget",
		setVerb("forwardingRules", "setTarget", "target", "target", rule))
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/forwardingRules/{name}/setLabels",
		setVerb("forwardingRules", "setLabels", "labels", "labels", rule))

	// The URL map a proxy routes through. Compute Engine declares this one
	// without a scope segment — unlike the proxy's own lifecycle, which is
	// under global — so that is the only spelling mounted for it.
	srv.HandleFunc("POST /compute/v1/projects/{project}/targetHttpProxies/{name}/setUrlMap",
		setVerb("targetHttpProxies", "setUrlMap", "urlMap", "urlMap", httpProxy))

	// The meta-resource registrar serves an HTTPS proxy's scoped spellings; these
	// are the unscoped ones the document also declares.
	unscoped := func(verb, member string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			value, sent := body[member]
			if !sent {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"%s needs %s in its request body", verb, member)
				return
			}
			key := computeGlobalLink(project, "targetHttpsProxies", name)
			if !gcpComputeTargetHTTPSProxies.Update(key, func(m *map[string]any) { (*m)[member] = value }) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "targetHttpsProxies %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, verb))
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/targetHttpsProxies/{name}/setUrlMap",
		unscoped("setUrlMap", "urlMap"))
	srv.HandleFunc("POST /compute/v1/projects/{project}/targetHttpsProxies/{name}/setSslCertificates",
		unscoped("setSslCertificates", "sslCertificates"))

	// A proxy's own patch, which the collection declares beside its lifecycle.
	patch := func(collection string, apply func(string, map[string]any) (bool, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			key := computeGlobalLink(project, collection, name)
			found, err := apply(key, body)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid %s: %v", collection, err)
				return
			}
			if !found {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "patch"))
		}
	}
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/forwardingRules/{name}",
		patch("forwardingRules", rule))
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/targetHttpProxies/{name}",
		patch("targetHttpProxies", httpProxy))

	// A backend service signs cached URLs as a backend bucket does: the key names
	// come back, the values never do.
	signingKey := func(verb string, add bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			key := computeGlobalLink(project, "backendServices", name)
			held, ok := gcpBackendServices.Get(key)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backendServices %q not found", name)
				return
			}
			existing := []string{}
			if held.CdnPolicy != nil {
				if declared, _ := held.CdnPolicy["signedUrlKeyNames"].([]any); declared != nil {
					for _, entry := range declared {
						if named, ok := entry.(string); ok {
							existing = append(existing, named)
						}
					}
				}
			}

			if add {
				var req struct {
					KeyName  string `json:"keyName"`
					KeyValue string `json:"keyValue"`
				}
				if err := sim.ReadJSON(r, &req); err != nil || req.KeyName == "" || req.KeyValue == "" {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"a signed-URL key needs a keyName and a keyValue")
					return
				}
				for _, already := range existing {
					if already == req.KeyName {
						GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
							"the backend service already has a signed-URL key named %q", req.KeyName)
						return
					}
				}
				existing = append(existing, req.KeyName)
			} else {
				wanted := r.URL.Query().Get("keyName")
				if wanted == "" {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"deleteSignedUrlKey needs the keyName to remove")
					return
				}
				kept, found := []string{}, false
				for _, already := range existing {
					if already == wanted {
						found = true
						continue
					}
					kept = append(kept, already)
				}
				if !found {
					GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"the backend service has no signed-URL key named %q", wanted)
					return
				}
				existing = kept
			}

			policy := held.CdnPolicy
			if policy == nil {
				policy = map[string]any{}
			}
			names := make([]any, 0, len(existing))
			for _, named := range existing {
				names = append(names, named)
			}
			policy["signedUrlKeyNames"] = names
			held.CdnPolicy = policy
			gcpBackendServices.Put(key, held)
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, verb))
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices/{name}/addSignedUrlKey",
		signingKey("addSignedUrlKey", true))
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/backendServices/{name}/deleteSignedUrlKey",
		signingKey("deleteSignedUrlKey", false))

	// The Discovery document declares no response for getEffectiveSecurityPolicies,
	// so it answers none once the service is found to exist.
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/backendServices/{name}/getEffectiveSecurityPolicies",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			if _, ok := gcpBackendServices.Get(computeGlobalLink(project, "backendServices", name)); !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backendServices %q not found", name)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
}
