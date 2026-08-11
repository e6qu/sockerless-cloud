package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	dockerimage "github.com/docker/docker/api/types/image"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
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

func registerCloudFunctions(srv *sim.Server) {
	functions := sim.MakeStore[storedFunction](srv.DB(), "gcf_functions")

	// Create function
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/functions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		functionID := r.URL.Query().Get("functionId")
		if functionID == "" {
			sim.GCPError(w, http.StatusBadRequest, "functionId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var fn storedFunction
		if err := sim.ReadJSON(r, &fn); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)
		if _, exists := functions.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "function %q already exists", name)
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
		// Use the simulator's own address as the function URL for invocations
		fn.ServiceConfig.Uri = fmt.Sprintf("http://%s/v2-functions-invoke/%s", r.Host, functionID)

		// Cloud Functions Gen2 are backed by a Cloud Run service that
		// real GCP creates server-side as part of CreateFunction. The
		// gcf overlay-and-swap path relies on `fn.ServiceConfig.Service`
		// being populated so it can call `Run.Services.GetService` /
		// `UpdateService` to swap the throwaway Buildpacks image with
		// the real overlay. Mirror that linkage here: stamp the
		// service name onto the function, and seed a backing ServiceV2
		// row so subsequent Get/PATCH on the service round-trip.
		buildOutputImage := ""
		if fn.BuildConfig != nil {
			buildOutputImage = fn.BuildConfig.DockerRepository
		}
		// Compose the backing service spec first so we can charge its CPU
		// load against the regional quota BEFORE persisting the function.
		// gcf creates the function; the live cloud creates the underlying
		// Cloud Run service server-side and that's the deploy that hits
		// the regional cpu_allocation quota.
		backingService := seedServiceV2Defaults(ServiceV2{
			Template: &RevisionTemplate{
				Containers: []Container{{
					Name:      functionID,
					Image:     buildOutputImage,
					Resources: functionCPUResources(fn),
				}},
			},
		}, r.Host, project, location, functionID)
		if !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(backingService)) {
			regionalCPUQuotaErrorJSON(w, backingService.Name)
			return
		}
		fn.ServiceConfig.Service = backingService.Name
		crv2Services.Put(backingService.Name, backingService)
		projectCloudRunV2ToV1(backingService)

		functions.Put(name, fn)

		lro := newLRO(project, location, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// GenerateUploadUrl: POST .../functions:generateUploadUrl. The verb
	// rides on the collection segment (no function ID), so capture
	// `functions:generateUploadUrl` in a single wildcard and split on the
	// colon. terraform's google_cloudfunctions2_function calls this before
	// CreateFunction to obtain the source-upload target.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/{functionsVerb}", func(w http.ResponseWriter, r *http.Request) {
		collection, verb, found := strings.Cut(sim.PathParam(r, "functionsVerb"), ":")
		if !found || collection != "functions" || verb != "generateUploadUrl" {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown functions verb %q", sim.PathParam(r, "functionsVerb"))
			return
		}
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		object := "uploads/" + generateUUID() + ".zip"
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on function %q", action, id)
			return
		}
		name := cloudFunctionName(project, location, functionParam)

		fn, ok := functions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
			return
		}
		var patch storedFunction
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		mask := r.URL.Query().Get("updateMask")
		applyFunctionPatch(&fn, &patch, mask)
		fn.Name = name
		fn.UpdateTime = nowTimestamp()
		functions.Put(name, fn)

		lro := newLRO(project, location, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// List functions
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/functions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/functions/", project, location)
		filter := r.URL.Query().Get("filter")

		stored := functions.Filter(func(fn storedFunction) bool {
			if !strings.HasPrefix(fn.Name, prefix) {
				return false
			}
			return matchesFunctionFilter(&fn, filter)
		})
		result := make([]Function, 0, len(stored))
		for _, fn := range stored {
			result = append(result, fn.wire())
		}
		sortCloudFunctions(result)
		result = gcpApplyOrderBy(result, r)
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

	// Invoke function (simulator-only endpoint)
	srv.HandleFunc("POST /v2-functions-invoke/{functionID}", func(w http.ResponseWriter, r *http.Request) {
		functionID := sim.PathParam(r, "functionID")

		// Find the function by scanning for a matching functionID suffix
		var fn *storedFunction
		for _, f := range functions.List() {
			if strings.HasSuffix(f.Name, "/functions/"+functionID) {
				f := f // copy
				fn = &f
				break
			}
		}

		responseBody := []byte("{}")
		if fn != nil {
			parts := strings.Split(fn.Name, "/") // projects/{project}/...
			if len(parts) < 2 {
				sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
					"function %q has a malformed resource name", fn.Name)
				return
			}
			project := parts[1]

			var exitCode int
			responseBody, exitCode = invokeCloudFunctionProcess(fn, project, functionID)
			if exitCode != 0 {
				// Real Cloud Functions returns HTTP error when function crashes
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write(responseBody)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

	// Delete function
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/functions/{function}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		functionID := sim.PathParam(r, "function")
		name := fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)

		fn, ok := functions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
			return
		}

		functions.Delete(name)

		lro := newLRO(project, location, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown function action %q", sim.PathParam(r, "functionAction"))
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
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "function %q not found", name)
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
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, fn.wire(), cloudFunctionTypeURL))
		case "abortFunctionUpgrade":
			applyUpgradeState(&fn, "ELIGIBLE_FOR_2ND_GEN_UPGRADE")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, fn.wire(), cloudFunctionTypeURL))
		case "redirectFunctionUpgradeTraffic":
			applyUpgradeState(&fn, "REDIRECT_FUNCTION_UPGRADE_TRAFFIC_SUCCESSFUL")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, fn.wire(), cloudFunctionTypeURL))
		case "rollbackFunctionUpgradeTraffic":
			// Roll traffic back to the 1st Gen stack; the function returns to
			// the setup-complete state (the 2nd Gen stack still exists).
			applyUpgradeState(&fn, "SETUP_FUNCTION_UPGRADE_CONFIG_SUCCESSFUL")
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, fn.wire(), cloudFunctionTypeURL))
		case "commitFunctionUpgrade", "commitFunctionUpgradeAsGen2":
			// Commit finalizes the migration: the function is now a 2nd Gen
			// function and upgradeInfo is cleared. A successful upgrade is
			// indicated by the LRO completing with the Function in the response.
			fn.Environment = "GEN_2"
			fn.UpgradeInfo = nil
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, fn.wire(), cloudFunctionTypeURL))
		case "detachFunction":
			// Detach the 2nd Gen function from its 1st Gen counterpart; the
			// function survives as a standalone 2nd Gen function.
			fn.UpgradeInfo = nil
			fn.UpdateTime = nowTimestamp()
			functions.Put(name, fn)
			sim.WriteJSON(w, http.StatusOK, newLRO(project, location, fn.wire(), cloudFunctionTypeURL))
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on function %q", action, id)
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

// invokeCloudFunctionProcess executes a Cloud Function invocation. Cloud
// Functions Gen2 are backed by a Cloud Run service whose container image
// is the sockerless overlay; the gcf backend's overlay-and-swap path lands
// that image on the service via `Run.Services.UpdateService`. The sim
// reads the image back from the backing service and HTTP-invokes the
// overlay's bootstrap — start the container, POST the request envelope to
// its bootstrap listener, read the response, stop the container — exactly
// what real Cloud Run Functions Gen2 does on every invocation. The exit
// code rides in the `X-Sockerless-Exit-Code` header.
//
// A function with no backing service image has been created but never
// deployed with an overlay; there is nothing to execute, so the sim records
// the invocation in Cloud Logging and returns an empty body.
func invokeCloudFunctionProcess(fn *storedFunction, project, functionID string) ([]byte, int) {
	// Container image lives on the underlying Cloud Run service — read it
	// back from there; the sim has no other source of truth for what to
	// execute.
	var image string
	var serviceEnv map[string]string
	if fn.ServiceConfig != nil && fn.ServiceConfig.Service != "" {
		if svc, ok := crv2Services.Get(fn.ServiceConfig.Service); ok {
			if svc.Template != nil && len(svc.Template.Containers) > 0 {
				container := svc.Template.Containers[0]
				image = container.Image
				serviceEnv = containerEnvMap(container.Env)
			}
		}
	}

	sink := &cfLogSink{project: project, functionName: functionID}

	if image == "" {
		injectCloudFunctionLog(project, functionID, "Function invoked")
		return []byte("{}"), 0
	}

	timeout := 60 * time.Second // GCP default
	if fn.ServiceConfig != nil && fn.ServiceConfig.TimeoutSeconds > 0 {
		timeout = time.Duration(fn.ServiceConfig.TimeoutSeconds) * time.Second
	}

	// Cloud-faithful: HTTP-invoke the overlay's bootstrap.
	env := serviceEnv
	if fn.ServiceConfig != nil {
		env = mergeEnv(fn.ServiceConfig.EnvironmentVariables, serviceEnv)
	}
	body, exitCode, err := invokeOverlayContainerHTTP(image, functionID, timeout, sink, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sim-gcf] invocation error fn=%s img=%s: %v\n", functionID, image, err)
		injectCloudFunctionLog(project, functionID,
			fmt.Sprintf("Function invocation error: %v", err))
		return []byte(fmt.Sprintf(`{"error":%q}`, err.Error())), 1
	}
	if exitCode != 0 {
		fmt.Fprintf(os.Stderr, "[sim-gcf] non-zero exit fn=%s img=%s exit=%d body=%q\n", functionID, image, exitCode, string(body))
		injectCloudFunctionLog(project, functionID,
			fmt.Sprintf("Function exited with code %d body=%q", exitCode, string(body)))
	}
	return body, exitCode
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
func applyFunctionPatch(fn, patch *storedFunction, mask string) {
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
		fn.ServiceConfig = patch.ServiceConfig
	}
}

// matchesFunctionFilter evaluates a Cloud Functions ListFunctions
// `filter` query against a Function. Supports the subset the gcf
// backend uses for pool-claim and allocation lookup:
//
//   - `labels.<key>:"<value>"` — Cloud Logging-style "has" / substring
//     match against the label value (the `:` operator).
//   - `labels.<key>="<value>"` — exact match.
//   - `-labels.<key>:*` — negation + wildcard: clause matches when the
//     label is unset or empty (i.e. the function is "free" of an
//     allocation claim, used by claimFreeFunction).
//   - Multiple clauses joined by ` AND `.
//
// Empty filter matches every Function. Real Cloud Functions supports
// the full Cloud Logging filter syntax; this is the operator subset
// the backend exercises today.
func matchesFunctionFilter(fn *storedFunction, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	for _, raw := range strings.Split(filter, " AND ") {
		clause := strings.TrimSpace(raw)
		if clause == "" {
			continue
		}
		negate := false
		if strings.HasPrefix(clause, "-") {
			negate = true
			clause = clause[1:]
		}
		// Wildcard form `labels.<key>:*` — clause is true when the
		// label is set to anything non-empty. With `-` prefix, true
		// when the label is unset/empty.
		if strings.HasSuffix(clause, ":*") {
			field := strings.TrimSuffix(clause, ":*")
			val := lookupFunctionField(fn, field)
			present := val != ""
			matched := present
			if negate {
				matched = !present
			}
			if !matched {
				return false
			}
			continue
		}
		c := parseClause(clause)
		val := lookupFunctionField(fn, c.field)
		var matched bool
		switch c.op {
		case opEq:
			matched = val == c.value
		case opHas:
			matched = strings.Contains(val, c.value)
		default:
			// Functions don't have ordered fields the backend
			// filters on — > / >= are unsupported here.
			matched = false
		}
		if negate {
			matched = !matched
		}
		if !matched {
			return false
		}
	}
	return true
}

// lookupFunctionField resolves a dot-notation field path on a Function.
// Currently supports `labels.<key>` and `name`; extend as the backend
// surfaces new filter shapes.
func lookupFunctionField(fn *storedFunction, field string) string {
	if strings.HasPrefix(field, "labels.") {
		key := field[len("labels."):]
		if fn.Labels != nil {
			return fn.Labels[key]
		}
		return ""
	}
	switch field {
	case "name":
		return fn.Name
	case "state":
		return fn.State
	}
	return ""
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

// invokeOverlayContainerHTTP runs the cloud-faithful invocation flow:
// start the overlay container detached, wait for the bootstrap HTTP
// server to be ready on its assigned host port, POST to it, read the
// response body + the bootstrap-set `X-Sockerless-Exit-Code` header,
// then stop and remove the container.
//
// This mirrors what real Cloud Run does for every Cloud Functions Gen2
// invocation: route the request to the underlying container's HTTP
// listener and return the response. The exit code header is set by
// `sockerless-gcf-bootstrap` so the docker-shell perceives the
// underlying subprocess's true exit status (matters for `docker run
// --rm <fail>` semantics where 1 should propagate, etc.).
//
// The container is short-lived per invocation (start → POST → stop).
// That keeps the sim's container-state footprint bounded — at most one
// in-flight invocation container per concurrent request — and matches
// docker-run-style one-shot semantics. Real Cloud Run keeps containers
// warm across invocations; the sim's per-invocation lifecycle is a
// simplification that doesn't change the semantic contract (the same
// command is run, the same output is returned).
//
// Errors are returned only for infrastructure failures (image pull,
// container start, networking). Subprocess non-zero exit is NOT an
// error — it surfaces via the `exitCode` return value.
func invokeOverlayContainerHTTP(image, functionID string, timeout time.Duration, sink sim.LogSink, env map[string]string) (responseBody []byte, exitCode int, err error) {
	return invokeOverlayContainerHTTPWithBody(image, functionID, timeout, sink, env, nil, "application/json")
}

// invokeOverlayContainerHTTPWithBody is the body-aware variant. The
// Cloud Run Services invoke handler uses it to forward the
// envelope-style POST body the gcf backend sends to the overlay
// bootstrap. Cloud Functions Gen2 invocations have no useful body so
// invokeOverlayContainerHTTP delegates here with `body=nil`.
func invokeOverlayContainerHTTPWithBody(image, functionID string, timeout time.Duration, sink sim.LogSink, env map[string]string, body io.Reader, contentType string) (responseBody []byte, exitCode int, err error) {
	cli := sim.DockerClient()
	if cli == nil {
		return nil, -1, fmt.Errorf("docker client not initialized")
	}

	localImage := sim.ResolveLocalImage(image)

	// Bootstrap listens on $PORT (defaults 8080). Bind to a random host
	// port so concurrent invocations on the same host don't collide.
	hostPort, err := pickFreeTCPPort()
	if err != nil {
		return nil, -1, fmt.Errorf("pick free port: %w", err)
	}

	containerName := fmt.Sprintf("sockerless-sim-gcf-%s-%d", functionID, hostPort)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	platform, err := localImagePlatform(ctx, localImage)
	if err != nil {
		return nil, -1, err
	}
	containerID, err := sim.StartHTTPContainer(ctx, sim.HTTPContainerConfig{
		Image:        localImage,
		Architecture: platform,
		HostPort:     hostPort,
		Env: mergeEnv(mergeEnv(map[string]string{
			"PORT": "8080",
		}, env), hostMetadataEnv()),
		Name: containerName,
		Labels: map[string]string{
			"sockerless-sim-function": functionID,
		},
		ExtraHosts: hostMetadataExtraHosts(),
		Sandbox:    sim.SandboxGCFGen2,
	})
	if err != nil {
		return nil, -1, fmt.Errorf("start overlay container: %w", err)
	}
	defer sim.StopAndRemoveContainer(containerID)

	// Stream container logs to Cloud Logging in the background. Uses
	// the same sink as the process path so test assertions on
	// `gcpFunctionLogMessages` find the bootstrap's stdout/stderr (the
	// user subprocess output is written to the bootstrap's own
	// stdout/stderr via io.MultiWriter — see agent/cmd/sockerless-gcf-
	// bootstrap/main.go::handleInvoke).
	logStreamCtx, logStreamCancel := context.WithCancel(context.Background())
	defer logStreamCancel()
	go sim.StreamContainerLogs(logStreamCtx, containerID, sink)

	// Reach the bootstrap by whichever address is connectable: the workload's
	// bridge container IP:8080 (works when the sim runs INSIDE a harness
	// container, where the host-published port binds the host's loopback, not
	// the sim container's), else 127.0.0.1:<hostPort> (sim on the host). Same
	// fix as the Cloud Run Services invoke path.
	var cands []string
	if ip := sim.ContainerIPv4(containerID); ip != "" {
		cands = append(cands, fmt.Sprintf("http://%s:8080", ip))
	}
	cands = append(cands, fmt.Sprintf("http://127.0.0.1:%d", hostPort))
	base, err := firstReachableBase(ctx, cands, 60*time.Second)
	if err != nil {
		return nil, -1, fmt.Errorf("bootstrap not ready (tried %d address(es)): %w", len(cands), err)
	}
	bootstrapURL := base + "/"

	// POST the invocation. Body is forwarded from the caller (the gcf
	// backend's exec envelope) when present. Cloud Functions Gen2
	// invocations pass nil here.
	return postBootstrapWithRetry(ctx, bootstrapURL, body, contentType, timeout)
}

func localImagePlatform(ctx context.Context, image string) (string, error) {
	cli := sim.DockerClient()
	if cli == nil {
		return "", fmt.Errorf("docker client not initialized")
	}
	inspect, _, err := cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		rc, pullErr := cli.ImagePull(ctx, image, dockerimage.PullOptions{})
		if pullErr != nil {
			return "", fmt.Errorf("inspect image %q platform: %w; pull image: %w", image, err, pullErr)
		}
		if _, copyErr := io.Copy(io.Discard, rc); copyErr != nil {
			_ = rc.Close()
			return "", fmt.Errorf("pull image %q: %w", image, copyErr)
		}
		if closeErr := rc.Close(); closeErr != nil {
			return "", fmt.Errorf("close image pull stream %q: %w", image, closeErr)
		}
		inspect, _, err = cli.ImageInspectWithRaw(ctx, image)
		if err != nil {
			return "", fmt.Errorf("inspect pulled image %q platform: %w", image, err)
		}
	}
	if inspect.Os == "" || inspect.Architecture == "" {
		return "", fmt.Errorf("inspect image %q platform: missing os/architecture", image)
	}
	return inspect.Os + "/" + inspect.Architecture, nil
}

func postBootstrapWithRetry(ctx context.Context, bootstrapURL string, body io.Reader, contentType string, timeout time.Duration) ([]byte, int, error) {
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
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, bootstrapURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, -1, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			respBytes, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				return nil, -1, fmt.Errorf("read bootstrap response: %w", readErr)
			}
			return respBytes, bootstrapExitCode(resp), nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, -1, fmt.Errorf("invoke bootstrap: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func bootstrapExitCode(resp *http.Response) int {
	if hdr := resp.Header.Get("X-Sockerless-Exit-Code"); hdr != "" {
		if n, parseErr := strconv.Atoi(hdr); parseErr == nil {
			return n
		}
	}
	if resp.StatusCode >= 400 {
		return 1
	}
	return 0
}

// pickFreeTCPPort opens a transient TCP listener to discover a
// free port number, then closes it. The OS may reassign the port
// before the caller binds it (TOCTOU); on a single-host sim this is
// vanishingly rare and reusing it is safe.
func pickFreeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return 0, fmt.Errorf("listener address is not a *net.TCPAddr: %T", l.Addr())
	}
	port := addr.Port
	_ = l.Close()
	return port, nil
}

func urlpkgParse(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", raw, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("url %q has no host", raw)
	}
	return parsed, nil
}
