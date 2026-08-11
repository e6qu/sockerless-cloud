package simulator

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AWSRouter routes AWS API requests based on the X-Amz-Target header.
// AWS JSON services use POST with X-Amz-Target: ServiceName.ActionName.
type AWSRouter struct {
	// handlers maps X-Amz-Target values to handler functions.
	handlers map[string]http.HandlerFunc
}

// NewAWSRouter creates a new AWS request router.
func NewAWSRouter() *AWSRouter {
	return &AWSRouter{
		handlers: make(map[string]http.HandlerFunc),
	}
}

// Register adds a handler for an X-Amz-Target value.
// Example target: "AmazonEC2ContainerServiceV20141113.RunTask"
func (r *AWSRouter) Register(target string, handler http.HandlerFunc) {
	r.handlers[target] = handler
}

// Targets returns every registered X-Amz-Target value. The
// spec-conformance tests validate this table against the vendored
// Smithy models (specs/cloud-api/aws/).
func (r *AWSRouter) Targets() []string {
	targets := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		targets = append(targets, t)
	}
	return targets
}

// Handler returns the handler registered for an X-Amz-Target value. Internal
// AWS service integrations use this to enter the same service implementation
// as an external SDK request without making a loopback HTTP call.
func (r *AWSRouter) Handler(target string) (http.HandlerFunc, bool) {
	handler, ok := r.handlers[target]
	return handler, ok
}

// ServeHTTP dispatches to the handler matching the X-Amz-Target header.
func (r *AWSRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	target := req.Header.Get("X-Amz-Target")
	if target == "" {
		AWSError(w, "MissingAction", "X-Amz-Target header is required", http.StatusBadRequest)
		return
	}

	handler, ok := r.handlers[target]
	if !ok {
		AWSErrorf(w, "UnknownOperationException", http.StatusBadRequest,
			"Unknown operation: %s", target)
		return
	}

	handler(w, req)
}

// GCPRouter routes GCP API requests based on URL path patterns.
// GCP REST APIs use paths like /v2/projects/{project}/locations/{location}/jobs.
type GCPRouter struct {
	mux *http.ServeMux
}

// NewGCPRouter creates a new GCP request router.
func NewGCPRouter() *GCPRouter {
	return &GCPRouter{
		mux: http.NewServeMux(),
	}
}

// Handle registers a pattern (e.g., "POST /v2/projects/{project}/locations/{location}/jobs").
func (r *GCPRouter) Handle(pattern string, handler http.HandlerFunc) {
	r.mux.HandleFunc(pattern, handler)
}

// ServeHTTP dispatches to the matching path handler.
func (r *GCPRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// AzureRouter routes Azure ARM API requests based on resource provider paths.
// Azure ARM uses paths like /subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.App/jobs/{name}.
type AzureRouter struct {
	mux *http.ServeMux
}

// NewAzureRouter creates a new Azure request router.
func NewAzureRouter() *AzureRouter {
	return &AzureRouter{
		mux: http.NewServeMux(),
	}
}

// Handle registers a pattern for ARM resource paths.
func (r *AzureRouter) Handle(pattern string, handler http.HandlerFunc) {
	r.mux.HandleFunc(pattern, handler)
}

// ServeHTTP dispatches to the matching path handler.
// It validates that the api-version query parameter is present.
func (r *AzureRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Skip api-version check for health and metadata endpoints
	if !strings.HasPrefix(req.URL.Path, "/health") && !strings.HasPrefix(req.URL.Path, "/metadata") {
		if req.URL.Query().Get("api-version") == "" {
			AzureError(w, "MissingApiVersion",
				"The api-version query parameter is required for all requests",
				http.StatusBadRequest)
			return
		}
	}
	r.mux.ServeHTTP(w, req)
}

// AWSQueryRouter routes AWS Query Protocol requests based on the
// (Version, Action) form parameter pair. Multiple AWS services
// define an Action with the same name (`ListTagsForResource` exists
// on RDS, SNS, ElastiCache, CloudWatchLogs at minimum); the
// canonical disambiguator is the per-service `Version` form field
// (RDS=2014-10-31, ElastiCache=2015-02-02, SNS=2010-03-31, etc.).
//
// Services with collision-prone Action names register via
// `RegisterVersioned(version, action, handler)`. Services whose
// action names are globally unique (EC2's `CreateVpc`, STS's
// `GetCallerIdentity`) keep using the legacy `Register(action,
// handler)` form; those handlers live under the empty-string
// version bucket and act as a fallback when no versioned match.
type AWSQueryRouter struct {
	// versioned[version][action] → handler. Empty-string version is
	// the legacy bucket used by Register(action, handler).
	versioned map[string]map[string]http.HandlerFunc
}

// NewAWSQueryRouter creates a new AWS Query Protocol request router.
func NewAWSQueryRouter() *AWSQueryRouter {
	return &AWSQueryRouter{
		versioned: make(map[string]map[string]http.HandlerFunc),
	}
}

// Register adds a handler for an Action value with no Version
// constraint. Used by services whose Action names don't collide
// across AWS — EC2 / IAM / STS / IAM SLR+OIDC.
//
// Example action: "CreateVpc", "GetCallerIdentity"
func (r *AWSQueryRouter) Register(action string, handler http.HandlerFunc) {
	r.RegisterVersioned("", action, handler)
}

// RegisterVersioned adds a handler for an (Action, Version) pair.
// Required when the same Action name is defined by multiple AWS
// services — the Version form parameter selects which service the
// caller is targeting.
//
// Example: r.RegisterVersioned("2014-10-31", "ListTagsForResource",
// handleRDSListTags) so RDS's ListTagsForResource doesn't shadow
// (or get shadowed by) ElastiCache's ListTagsForResource at the
// router level.
func (r *AWSQueryRouter) RegisterVersioned(version, action string, handler http.HandlerFunc) {
	if r.versioned[version] == nil {
		r.versioned[version] = make(map[string]http.HandlerFunc)
	}
	r.versioned[version][action] = handler
}

// VersionedActions returns every registered (version, action) pair;
// actions registered through the legacy Register form appear under
// the empty-string version. The spec-conformance tests validate this
// table against the vendored Smithy models (specs/cloud-api/aws/).
func (r *AWSQueryRouter) VersionedActions() map[string][]string {
	out := make(map[string][]string, len(r.versioned))
	for version, actions := range r.versioned {
		for action := range actions {
			out[version] = append(out[version], action)
		}
	}
	return out
}

// Handler returns the handler registered for a (Version, Action) pair using
// the same versioned-first lookup as ServeHTTP. Internal AWS service
// integrations use it to enter the real Query service implementation without
// creating a signed loopback request.
func (r *AWSQueryRouter) Handler(version, action string) (http.HandlerFunc, bool) {
	if version != "" {
		if actions, ok := r.versioned[version]; ok {
			if handler, ok := actions[action]; ok {
				return handler, true
			}
		}
	}
	if actions, ok := r.versioned[""]; ok {
		handler, ok := actions[action]
		return handler, ok
	}
	return nil, false
}

// ServeHTTP dispatches by (Version, Action). When Version is set
// and a versioned handler exists, it wins. Otherwise the legacy
// bucket (version="") is consulted as a fallback.
func (r *AWSQueryRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := req.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<Response><Errors><Error><Code>MalformedInput</Code><Message>Could not parse form body</Message></Error></Errors></Response>`))
		return
	}

	action := req.FormValue("Action")
	if action == "" {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<Response><Errors><Error><Code>MissingAction</Code><Message>Action parameter is required</Message></Error></Errors></Response>`))
		return
	}

	version := req.FormValue("Version")
	if handler, ok := r.Handler(version, action); ok {
		handler(w, req)
		return
	}

	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusBadRequest)
	// XML-escape the reflected action — a value with <, &, or ]]> would
	// otherwise produce malformed (or attacker-shaped) XML in the response.
	_, _ = w.Write([]byte(`<Response><Errors><Error><Code>InvalidAction</Code><Message>The action ` + xmlEscape(action) + ` is not valid</Message></Error></Errors></Response>`))
}

// xmlEscape returns s with XML metacharacters escaped, for safe interpolation
// into a hand-built XML error body.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// maxQueryJSONBody bounds an AWS control-plane JSON request body so a
// maliciously large payload can't OOM the simulator. Control-plane requests
// are small; this is generous headroom, matching the data-plane cap's intent.
const maxQueryJSONBody = 64 << 20 // 64 MiB

// ReadJSON reads and decodes a JSON request body into the given value. The read
// is capped at maxQueryJSONBody so an unbounded body can't exhaust memory.
func ReadJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxQueryJSONBody+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxQueryJSONBody {
		return fmt.Errorf("request body exceeds %d bytes", maxQueryJSONBody)
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

// PathParam extracts a path parameter from the request using Go 1.22+ routing.
func PathParam(r *http.Request, name string) string {
	return r.PathValue(name)
}
