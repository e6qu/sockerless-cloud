package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Functions v2 types

// Function represents a Cloud Functions v2 function.
type Function struct {
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	BuildConfig   *BuildConfig      `json:"buildConfig,omitempty"`
	ServiceConfig *ServiceConfig    `json:"serviceConfig,omitempty"`
	State         string            `json:"state"`
	CreateTime    string            `json:"createTime"`
	UpdateTime    string            `json:"updateTime"`
	Labels        map[string]string `json:"labels,omitempty"`
	Environment   enumString        `json:"environment,omitempty"`
	URL           string            `json:"url,omitempty"`
	// UpgradeInfo carries the 1st-Gen→2nd-Gen migration state. It is
	// populated only for functions an upgrade-lifecycle verb has touched;
	// the upgrade colon-verbs transition upgradeInfo.upgradeState.
	UpgradeInfo *UpgradeInfo `json:"upgradeInfo,omitempty"`
}

// UpgradeInfo describes a function's 1st Gen → 2nd Gen migration state.
// Mirrors google.cloud.functions.v2.UpgradeInfo: the upgrade colon-verbs
// drive upgradeState through the documented transitions.
type UpgradeInfo struct {
	UpgradeState  string         `json:"upgradeState,omitempty"`
	ServiceConfig *ServiceConfig `json:"serviceConfig,omitempty"`
	BuildConfig   *BuildConfig   `json:"buildConfig,omitempty"`
}

// BuildConfig holds the build configuration for a function.
type BuildConfig struct {
	Runtime          string `json:"runtime,omitempty"`
	EntryPoint       string `json:"entryPoint,omitempty"`
	Source           any    `json:"source,omitempty"`
	DockerRepository string `json:"dockerRepository,omitempty"`
}

// ServiceConfig holds the service configuration for a function.
type ServiceConfig struct {
	Uri              string `json:"uri,omitempty"`
	Service          string `json:"service,omitempty"` // Underlying Cloud Run service name (Gen2)
	TimeoutSeconds   int    `json:"timeoutSeconds,omitempty"`
	AvailableMemory  string `json:"availableMemory,omitempty"`
	AvailableCpu     string `json:"availableCpu,omitempty"` // CPU limit (e.g. "1", "0.5", "2"). Real Cloud Functions Gen2 default: 1.
	MaxInstanceCount int    `json:"maxInstanceCount,omitempty"`
	MinInstanceCount int    `json:"minInstanceCount,omitempty"`
	// AllTrafficOnLatestRevision + IngressSettings carry provider defaults
	// (true / ALLOW_ALL); the read-back must echo them or terraform-provider-
	// google plans an in-place service_config update on every refresh.
	AllTrafficOnLatestRevision *bool             `json:"allTrafficOnLatestRevision,omitempty"`
	IngressSettings            string            `json:"ingressSettings,omitempty"`
	EnvironmentVariables       map[string]string `json:"environmentVariables,omitempty"`
}

// storedServiceConfig is the persisted/request-side ServiceConfig. It embeds
// the wire shape so the persisted row matches what the wire Function carries;
// `wire()` returns the embedded ServiceConfig verbatim.
type storedServiceConfig struct {
	ServiceConfig
}

// storedFunction is the persisted row backing a function. Its
// serviceConfig field shadows the embedded wire Function's so request
// decoding and sim.Store persistence keep the same nested row shape that
// `wire()` recovers the wire Function from.
type storedFunction struct {
	Function
	ServiceConfig *storedServiceConfig `json:"serviceConfig,omitempty"`
}

// wire is the Function resource emitted on the wire: the stored
// function with its serviceConfig narrowed to the schema's member set.
func (f storedFunction) wire() Function {
	fn := f.Function
	if f.ServiceConfig != nil {
		sc := f.ServiceConfig.ServiceConfig
		fn.ServiceConfig = &sc
	}
	return fn
}

// functionCPUResources returns the ResourceRequirements that should be
// stamped onto the underlying Cloud Run service's container so the
// regional quota check sees the real CPU load. ServiceConfig.AvailableCpu
// is the explicit field; default is "1" (Cloud Functions Gen2 minimum).
func functionCPUResources(fn storedFunction) *ResourceRequirements {
	cpu := "1"
	if fn.ServiceConfig != nil && fn.ServiceConfig.AvailableCpu != "" {
		cpu = fn.ServiceConfig.AvailableCpu
	}
	return &ResourceRequirements{Limits: map[string]string{"cpu": cpu}}
}

func functionsLRO(r *http.Request, project, location, target string, resource any, typeName string) Operation {
	return newLRO(project, location, resource, typeName,
		gcpStandardOperationMetadata("type.googleapis.com/google.cloud.functions.v2.OperationMetadata", gcpOperationVerb(r), target))
}

var cloudFunctions sim.Store[storedFunction]

// registerCloudFunctionsFrontEnd serves each function on its cloudfunctions.net
// URL, https://<region>-<project>.cloudfunctions.net/<function>: the request,
// with the function's name taken off the front of its path, goes to the Cloud
// Run service that serves the function, as a request to its run.app URL does.
func registerCloudFunctionsFrontEnd(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hostname := lbplane.Hostname(r.Host)
			if !strings.HasSuffix(hostname, ".cloudfunctions.net") {
				next.ServeHTTP(w, r)
				return
			}
			functionID, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
			fn, ok := cloudFunctionByURL("https://" + hostname + "/" + functionID)
			if !ok {
				cloudRunFrontEndError(w, http.StatusNotFound, "The requested URL was not found on this server.")
				return
			}
			svc, ok := cloudFunctionService(fn)
			if !ok {
				cloudRunFrontEndError(w, http.StatusNotFound, "The requested URL was not found on this server.")
				return
			}
			forwarded := r.Clone(r.Context())
			forwarded.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"+functionID), "/")
			forwarded.URL.RawPath = ""
			if r.URL.RawPath != "" {
				forwarded.URL.RawPath = "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.RawPath, "/"+functionID), "/")
			}
			serveCloudRunService(w, forwarded, svc, fn.URL)
		})
	})
}

var cloudFunctionsByURL sim.GenerationIndex[storedFunction]

// cloudFunctionByURL returns the function whose url is functionURL.
func cloudFunctionByURL(functionURL string) (storedFunction, bool) {
	if cloudFunctions == nil {
		return storedFunction{}, false
	}
	return cloudFunctionsByURL.Lookup(cloudFunctions, functionURL, func(fn storedFunction) []string {
		if fn.URL == "" {
			return nil
		}
		return []string{fn.URL}
	})
}

func registerCloudFunctions(srv *sim.Server) {
	functions := sim.MakeStore[storedFunction](srv.DB(), "gcf_functions")
	cloudFunctions = functions
	registerCloudFunctionsFrontEnd(srv)

	// Create function
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/functions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		functionID := r.URL.Query().Get("functionId")
		if functionID == "" {
			GCPError(w, http.StatusBadRequest, "functionId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var fn storedFunction
		if err := sim.ReadJSON(r, &fn); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)
		if _, exists := functions.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "function %q already exists", name)
			return
		}

		now := nowTimestamp()
		fn.Name = name
		fn.State = "ACTIVE"
		fn.CreateTime = now
		fn.UpdateTime = now
		if fn.Environment == "" {
			fn.Environment = "GEN_2"
		}
		if fn.ServiceConfig == nil {
			fn.ServiceConfig = &storedServiceConfig{}
		}
		if fn.ServiceConfig.AllTrafficOnLatestRevision == nil {
			allTraffic := true
			fn.ServiceConfig.AllTrafficOnLatestRevision = &allTraffic
		}
		if fn.ServiceConfig.IngressSettings == "" {
			fn.ServiceConfig.IngressSettings = "ALLOW_ALL"
		}
		fn.URL = cloudFunctionURL(project, location, functionID)

		// Cloud Run functions creates the Cloud Run service that serves the
		// function as part of CreateFunction, and that deploy is the one the
		// regional CPU quota charges.
		buildOutputImage := ""
		if fn.BuildConfig != nil {
			buildOutputImage = fn.BuildConfig.DockerRepository
		}
		backingService := seedServiceV2Defaults(ServiceV2{
			Template: &RevisionTemplate{
				Containers: []Container{{
					Name:  functionID,
					Image: buildOutputImage,
				}},
			},
		}, project, location, functionID)
		applyFunctionServiceConfig(&backingService, fn)
		if !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(backingService)) {
			regionalCPUQuotaErrorJSON(w, backingService.Name)
			return
		}
		fn.ServiceConfig.Service = backingService.Name
		fn.ServiceConfig.Uri = backingService.URI
		backingService.Etag = sim.NewUUID()
		crv2Services.Put(backingService.Name, backingService)
		projectCloudRunV2ToV1(backingService)

		functions.Put(name, fn)

		lro := functionsLRO(r, project, location, name, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// GenerateUploadUrl: POST .../functions:generateUploadUrl. The verb
	// rides on the collection segment (no function ID), so capture
	// `functions:generateUploadUrl` in a single wildcard and split on the
	// colon. terraform's google_cloudfunctions2_function calls this before
	// CreateFunction to obtain the source-upload target.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/{functionsVerb}", func(w http.ResponseWriter, r *http.Request) {
		collection, verb, found := strings.Cut(sim.PathParam(r, "functionsVerb"), ":")
		// Cloud Run v2 shares this /v2 locations path, so its verbs are
		// offered the fan-in before the Cloud Functions one.
		if found && cloudRunLocationVerbHandled(w, r, collection, verb) {
			return
		}
		if !found || collection != "functions" || verb != "generateUploadUrl" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown functions verb %q", sim.PathParam(r, "functionsVerb"))
			return
		}
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		object := "uploads/" + sim.NewUUID() + ".zip"
		bucket := fmt.Sprintf("gcf-sources-%s-%s", project, location)
		uploadURL := fmt.Sprintf("http://%s/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s",
			r.Host, bucket, url.QueryEscape(object))
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"uploadUrl": uploadURL,
			"storageSource": map[string]any{
				"bucket":     bucket,
				"object":     object,
				"generation": "1",
			},
		})
	})

	// Get function. The `{function}` wildcard also captures the GET-side
	// AIP-141 IAM verb `{id}:getIamPolicy` (Go's mux can't spell `{id}:verb`),
	// dispatched by splitting on the colon.
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/functions/{function}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		functionParam := sim.PathParam(r, "function")
		if id, action, found := strings.Cut(functionParam, ":"); found {
			if action == "getIamPolicy" {
				handleResourceIAM(w, r, gcpResourceIAMStore(), cloudFunctionName(project, location, id), action)
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on function %q", action, id)
			return
		}
		name := cloudFunctionName(project, location, functionParam)

		fn, ok := functions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, fn.wire())
	})

	// Update function: PATCH .../functions/{function}?updateMask=...
	// terraform's google_cloudfunctions2_function PATCHes on every in-place
	// change. Merge the updateMask fields onto the stored function and
	// return an LRO (the client polls GetOperation, which resolves done).
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/functions/{function}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		functionID := sim.PathParam(r, "function")
		name := fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)

		fn, ok := functions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
			return
		}
		var patch storedFunction
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		serviceConfigChanged := applyFunctionPatch(&fn, &patch, mask)
		fn.Name = name
		fn.UpdateTime = nowTimestamp()
		if svc, ok := cloudFunctionService(fn); ok && serviceConfigChanged {
			applyFunctionServiceConfig(&svc, fn)
			if !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(svc)) {
				regionalCPUQuotaErrorJSON(w, svc.Name)
				return
			}
			rollOutServiceRevision(svc)
		}
		functions.Put(name, fn)

		lro := functionsLRO(r, project, location, name, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// List functions
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/functions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/functions/", project, location)
		stored := functions.Filter(func(fn storedFunction) bool {
			return strings.HasPrefix(fn.Name, prefix)
		})
		result := make([]Function, 0, len(stored))
		for _, fn := range stored {
			result = append(result, fn.wire())
		}
		sortCloudFunctions(result)
		listed, listOK := gcpApplyListParams(w, r, result)
		if !listOK {
			return
		}
		result = listed
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}

		resp := map[string]any{"functions": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Delete function
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/functions/{function}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		functionID := sim.PathParam(r, "function")
		name := fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)

		fn, ok := functions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
			return
		}

		functions.Delete(name)
		if fn.ServiceConfig != nil && fn.ServiceConfig.Service != "" {
			removeCloudRunServiceV2(project, location, fn.ServiceConfig.Service[strings.LastIndex(fn.ServiceConfig.Service, "/")+1:])
		}

		lro := functionsLRO(r, project, location, name, nil, "type.googleapis.com/google.protobuf.Empty")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// Function-scoped POST colon-verbs: the AIP-141 IAM verbs
	// (setIamPolicy / testIamPermissions), generateDownloadUrl, detachFunction,
	// and the seven 1st-Gen→2nd-Gen upgrade-lifecycle verbs. Go's mux can't
	// spell `{id}:verb`, so a single `{functionAction}` wildcard captures
	// `<functionId>:<verb>` and fans in on the verb.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/functions/{functionAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		id, action, found := strings.Cut(sim.PathParam(r, "functionAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown function action %q", sim.PathParam(r, "functionAction"))
			return
		}
		name := cloudFunctionName(project, location, id)

		switch action {
		case "setIamPolicy", "testIamPermissions":
			handleResourceIAM(w, r, gcpResourceIAMStore(), name, action)
			return
		}

		// The remaining verbs operate on an existing function.
		fn, ok := functions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
			return
		}

		switch action {
		case "generateDownloadUrl":
			// GenerateDownloadUrlRequest has no body fields; the response is
			// a signed Cloud Storage URL for the function's source archive.
			bucket := fmt.Sprintf("gcf-sources-%s-%s", project, location)
			object := fmt.Sprintf("downloads/%s.zip", id)
			downloadURL := fmt.Sprintf("http://%s/download/storage/v1/b/%s/o/%s?alt=media",
				r.Host, bucket, url.QueryEscape(object))
			sim.WriteJSON(w, http.StatusOK, map[string]any{"downloadUrl": downloadURL})
		case "setupFunctionUpgradeConfig":
			applyUpgradeState(&fn, "SETUP_FUNCTION_UPGRADE_CONFIG_SUCCESSFUL")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, functionsLRO(r, project, location, name, fn.wire(), cloudFunctionTypeURL))
		case "abortFunctionUpgrade":
			applyUpgradeState(&fn, "ELIGIBLE_FOR_2ND_GEN_UPGRADE")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, functionsLRO(r, project, location, name, fn.wire(), cloudFunctionTypeURL))
		case "redirectFunctionUpgradeTraffic":
			applyUpgradeState(&fn, "REDIRECT_FUNCTION_UPGRADE_TRAFFIC_SUCCESSFUL")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, functionsLRO(r, project, location, name, fn.wire(), cloudFunctionTypeURL))
		case "rollbackFunctionUpgradeTraffic":
			// Roll traffic back to the 1st Gen stack; the function returns to
			// the setup-complete state (the 2nd Gen stack still exists).
			applyUpgradeState(&fn, "SETUP_FUNCTION_UPGRADE_CONFIG_SUCCESSFUL")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, functionsLRO(r, project, location, name, fn.wire(), cloudFunctionTypeURL))
		case "commitFunctionUpgrade", "commitFunctionUpgradeAsGen2":
			// Commit finalizes the migration: the function is now a 2nd Gen
			// function and upgradeInfo is cleared. A successful upgrade is
			// indicated by the LRO completing with the Function in the response.
			fn.Environment = "GEN_2"
			fn.UpgradeInfo = nil
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, functionsLRO(r, project, location, name, fn.wire(), cloudFunctionTypeURL))
		case "detachFunction":
			// Detach the 2nd Gen function from its 1st Gen counterpart; the
			// function survives as a standalone 2nd Gen function.
			fn.UpgradeInfo = nil
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, functionsLRO(r, project, location, name, fn.wire(), cloudFunctionTypeURL))
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on function %q", action, id)
		}
	})

	// List locations (Locations.ListLocations).
	srv.HandleFunc("GET /v2/projects/{project}/locations", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		locations := make([]map[string]any, 0, len(cloudFunctionRegions))
		for _, region := range cloudFunctionRegions {
			locations = append(locations, map[string]any{
				"name":        fmt.Sprintf("projects/%s/locations/%s", project, region),
				"locationId":  region,
				"displayName": region,
				"labels":      map[string]string{"cloud.googleapis.com/region": region},
			})
		}
		page, next, ok := paginateList(w, r, locations)
		if !ok {
			return
		}
		resp := map[string]any{"locations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// List operations (Operations.ListOperations) under a location. Projects
	// the shared crOperations store filtered to this location's prefix.
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/operations", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/operations/", project, location)
		out := make([]Operation, 0)
		for _, op := range crOperations.List() {
			if strings.HasPrefix(op.Name, prefix) {
				out = append(out, op)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		page, next, ok := paginateList(w, r, out)
		if !ok {
			return
		}
		resp := map[string]any{"operations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// List runtimes (ListRuntimes) — the function runtimes available in a
	// location. A faithful representative slice of the real Cloud Functions
	// Gen2 runtime catalog.
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/runtimes", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"runtimes": cloudFunctionRuntimes()})
	})
}

// cloudFunctionTypeURL is the protobuf type URL for the Function resource,
// stamped into LRO responses.
const cloudFunctionTypeURL = "type.googleapis.com/google.cloud.functions.v2.Function"

// cloudFunctionRegions is a representative slice of the regions in which
// Cloud Functions Gen2 is available, used to back ListLocations.
var cloudFunctionRegions = []string{
	"us-central1", "us-east1", "us-west1",
	"europe-west1", "europe-west2",
	"asia-east1", "asia-northeast1",
}

// cloudFunctionService returns the Cloud Run service that serves a function.
func cloudFunctionService(fn storedFunction) (ServiceV2, bool) {
	if fn.ServiceConfig == nil || fn.ServiceConfig.Service == "" {
		return ServiceV2{}, false
	}
	return crv2Services.Get(fn.ServiceConfig.Service)
}

// cloudFunctionURL is the cloudfunctions.net URL Cloud Run functions reports
// as a function's url and serves the function on.
func cloudFunctionURL(project, location, functionID string) string {
	return fmt.Sprintf("https://%s-%s.cloudfunctions.net/%s", location, project, functionID)
}

// applyFunctionServiceConfig carries a function's serviceConfig onto the
// template of the Cloud Run service that serves it: its CPU, its environment
// variables and its request timeout, which defaults to 60 seconds.
func applyFunctionServiceConfig(svc *ServiceV2, fn storedFunction) {
	if svc.Template == nil || len(svc.Template.Containers) == 0 {
		return
	}
	container := &svc.Template.Containers[0]
	container.Resources = functionCPUResources(fn)
	timeout := 60
	var env []EnvVar
	if sc := fn.ServiceConfig; sc != nil {
		if sc.TimeoutSeconds > 0 {
			timeout = sc.TimeoutSeconds
		}
		names := make([]string, 0, len(sc.EnvironmentVariables))
		for name := range sc.EnvironmentVariables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			env = append(env, EnvVar{Name: name, Value: sc.EnvironmentVariables[name]})
		}
	}
	container.Env = env
	svc.Template.Timeout = fmt.Sprintf("%ds", timeout)
}

// cloudFunctionName builds the fully-qualified Cloud Functions v2 resource
// name from its coordinates.
func cloudFunctionName(project, location, functionID string) string {
	return fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)
}

// applyUpgradeState stamps the function's upgradeInfo.upgradeState, allocating
// the UpgradeInfo sub-object on first use.
func applyUpgradeState(fn *storedFunction, state string) {
	if fn.UpgradeInfo == nil {
		fn.UpgradeInfo = &UpgradeInfo{}
	}
	fn.UpgradeInfo.UpgradeState = state
}

// cloudFunctionRuntimes returns a representative slice of the real Cloud
// Functions Gen2 runtime catalog for ListRuntimes.
func cloudFunctionRuntimes() []map[string]any {
	type rt struct {
		name, displayName string
	}
	catalog := []rt{
		{"nodejs20", "Node.js 20"},
		{"nodejs18", "Node.js 18"},
		{"python312", "Python 3.12"},
		{"python311", "Python 3.11"},
		{"go122", "Go 1.22"},
		{"go121", "Go 1.21"},
		{"java21", "Java 21"},
		{"java17", "Java 17"},
		{"dotnet8", ".NET 8"},
		{"ruby33", "Ruby 3.3"},
		{"php83", "PHP 8.3"},
	}
	out := make([]map[string]any, 0, len(catalog))
	for _, c := range catalog {
		out = append(out, map[string]any{
			"name":        c.name,
			"displayName": c.displayName,
			"stage":       "GA",
			"environment": "GEN_2",
		})
	}
	return out
}

// cfLogSink implements sim.LogSink and writes log lines to Cloud Logging
// for Cloud Function invocations.
type cfLogSink struct {
	project      string
	functionName string
}

func (s *cfLogSink) WriteLog(line sim.LogLine) {
	injectCloudFunctionLog(s.project, s.functionName, line.Text)
}

// applyFunctionPatch merges the updateMask fields of a PATCH body onto the
// stored function. The mask carries dot-notation paths (description, labels,
// buildConfig.*, serviceConfig.*); for the nested config objects a named
// path replaces the whole sub-object when present, which matches how the
// provider sends grouped service_config / build_config updates.
func applyFunctionPatch(fn, patch *storedFunction, mask string) (serviceConfigChanged bool) {
	fields := strings.Split(mask, ",")
	if mask == "" {
		fields = nil
	}
	has := func(prefix string) bool {
		for _, f := range fields {
			f = strings.TrimSpace(f)
			if f == prefix || strings.HasPrefix(f, prefix+".") {
				return true
			}
		}
		return false
	}
	if len(fields) == 0 || has("description") {
		fn.Description = patch.Description
	}
	if len(fields) == 0 || has("labels") {
		fn.Labels = patch.Labels
	}
	if (len(fields) == 0 || has("buildConfig")) && patch.BuildConfig != nil {
		fn.BuildConfig = patch.BuildConfig
	}
	if (len(fields) == 0 || has("serviceConfig")) && patch.ServiceConfig != nil {
		output := fn.ServiceConfig
		fn.ServiceConfig = patch.ServiceConfig
		if output != nil {
			fn.ServiceConfig.Uri = output.Uri
			fn.ServiceConfig.Service = output.Service
		}
		serviceConfigChanged = true
	}
	return serviceConfigChanged
}

// injectCloudFunctionLog writes a log entry to the Cloud Logging store for a
// Cloud Function invocation, using the resource type and labels that the
// Cloud Functions backend's log filter expects.
func injectCloudFunctionLog(project, functionName, text string) {
	logName := fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2Fstdout", project)
	writeLogEntries(logName, &MonitoredResource{
		Type:   "cloud_run_revision",
		Labels: map[string]string{"service_name": functionName},
	}, nil, []LogEntry{{TextPayload: text}})
}
