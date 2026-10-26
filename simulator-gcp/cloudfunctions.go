package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/e6qu/sockerless-cloud/sim/revisions"
)

type Function struct {
	Name          string                          `json:"name"`
	Description   string                          `json:"description,omitempty"`
	BuildConfig   *BuildConfig                    `json:"buildConfig,omitempty"`
	ServiceConfig *ServiceConfig                  `json:"serviceConfig,omitempty"`
	State         string                          `json:"state"`
	CreateTime    string                          `json:"createTime"`
	UpdateTime    string                          `json:"updateTime"`
	Labels        map[string]string               `json:"labels,omitempty"`
	Environment   protoEnum[functionsEnvironment] `json:"environment,omitempty"`
	URL           string                          `json:"url,omitempty"`
	// UpgradeInfo carries the 1st gen → 2nd gen migration state of a function an
	// upgrade colon-verb has touched.
	UpgradeInfo *UpgradeInfo `json:"upgradeInfo,omitempty"`
}

// UpgradeInfo mirrors google.cloud.functions.v2.UpgradeInfo.
type UpgradeInfo struct {
	UpgradeState  string         `json:"upgradeState,omitempty"`
	ServiceConfig *ServiceConfig `json:"serviceConfig,omitempty"`
	BuildConfig   *BuildConfig   `json:"buildConfig,omitempty"`
}

type BuildConfig struct {
	Runtime          string `json:"runtime,omitempty"`
	EntryPoint       string `json:"entryPoint,omitempty"`
	Source           any    `json:"source,omitempty"`
	DockerRepository string `json:"dockerRepository,omitempty"`
}

type ServiceConfig struct {
	Uri              string `json:"uri,omitempty"`
	Service          string `json:"service,omitempty"` // Cloud Run service that serves the function
	TimeoutSeconds   int    `json:"timeoutSeconds,omitempty"`
	AvailableMemory  string `json:"availableMemory,omitempty"`
	AvailableCpu     string `json:"availableCpu,omitempty"` // CPU limit ("1", "0.5", "2"); Cloud Run functions defaults to 1.
	MaxInstanceCount int    `json:"maxInstanceCount,omitempty"`
	MinInstanceCount int    `json:"minInstanceCount,omitempty"`
	// VpcConnector names a Serverless VPC Access connector as
	// projects/*/locations/*/connectors/*; VpcConnectorEgressSettings is
	// PRIVATE_RANGES_ONLY or ALL_TRAFFIC.
	VpcConnector               string                                 `json:"vpcConnector,omitempty"`
	VpcConnectorEgressSettings protoEnum[functionsVPCConnectorEgress] `json:"vpcConnectorEgressSettings,omitempty"`
	// AllTrafficOnLatestRevision and IngressSettings carry provider defaults
	// (true, ALLOW_ALL); the read must echo them or terraform-provider-google
	// plans a service_config update on every refresh.
	AllTrafficOnLatestRevision *bool                       `json:"allTrafficOnLatestRevision,omitempty"`
	IngressSettings            protoEnum[functionsIngress] `json:"ingressSettings,omitempty"`
	EnvironmentVariables       map[string]string           `json:"environmentVariables,omitempty"`
}

// storedServiceConfig embeds the wire ServiceConfig, so the persisted row has
// the wire shape and wire() returns it verbatim.
type storedServiceConfig struct {
	ServiceConfig
}

// storedFunction is the persisted function. Its serviceConfig shadows the
// embedded wire Function's; wire() recovers the wire Function from it.
type storedFunction struct {
	Function
	ServiceConfig *storedServiceConfig `json:"serviceConfig,omitempty"`
}

// wire narrows the stored serviceConfig to the schema's member set.
func (f storedFunction) wire() Function {
	fn := f.Function
	if f.ServiceConfig != nil {
		sc := f.ServiceConfig.ServiceConfig
		fn.ServiceConfig = &sc
	}
	return fn
}

// functionCPUResources returns the resources to stamp on the Cloud Run
// service's container so the regional quota check sees the function's CPU:
// ServiceConfig.AvailableCpu, or "1", the Cloud Run functions minimum.
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

// cloudFunctionsDomain is the domain of every cloudfunctions.net URL.
const cloudFunctionsDomain = ".cloudfunctions.net"

// registerCloudFunctionsFrontEnd serves each function on its cloudfunctions.net
// URL, https://<region>-<project>.cloudfunctions.net/<function>: the request,
// with the function's name taken off the front of its path, goes to the Cloud
// Run service that serves the function, as a request to its run.app URL does.
func registerCloudFunctionsFrontEnd(srv *sim.Server) {
	srv.WrapHandler(lbplane.HostRouter{
		Domains: []string{cloudFunctionsDomain},
		Route: func(r *http.Request, hostname string) (http.Handler, bool) {
			functionID, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
			fn, ok := cloudFunctionByURL("https://" + hostname + "/" + functionID)
			if !ok {
				return nil, false
			}
			svc, ok := cloudFunctionService(fn)
			if !ok {
				return nil, false
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded := r.Clone(r.Context())
				forwarded.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"+functionID), "/")
				forwarded.URL.RawPath = ""
				if r.URL.RawPath != "" {
					forwarded.URL.RawPath = "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.RawPath, "/"+functionID), "/")
				}
				serveCloudRunService(w, forwarded, svc, fn.URL)
			}), true
		},
		NotFound: cloudRunNotFound,
	}.Wrap)
}

var cloudFunctionsByURL sim.GenerationIndex[storedFunction]

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
		reconcileServiceRevision(crv2Revisions, backingService.Name,
			backingService.LatestCreatedRevision[strings.LastIndex(backingService.LatestCreatedRevision, "/")+1:], backingService)
		projectCloudRunV2ToV1(backingService)
		keepCloudRunRevisionsWarm(backingService)

		functions.Put(name, fn)

		lro := functionsLRO(r, project, location, name, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// generateUploadUrl rides the collection segment, so one wildcard captures
	// `functions:generateUploadUrl`. google_cloudfunctions2_function calls it
	// before CreateFunction to get the source-upload target.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/{functionsVerb}", func(w http.ResponseWriter, r *http.Request) {
		collection, verb, found := strings.Cut(sim.PathParam(r, "functionsVerb"), ":")
		// Cloud Run v2 shares this /v2 locations path, so its verbs get the fan-in
		// first.
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

	// {function} also captures `{id}:getIamPolicy`, which ServeMux cannot spell as
	// its own pattern; split on the colon.
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

	// google_cloudfunctions2_function PATCHes every in-place change: merge the
	// updateMask fields and answer an operation.
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
			// applyFunctionServiceConfig edits the template in place, so take its
			// signature first.
			before, beforeErr := revisions.Signature(svc.Template)
			applyFunctionServiceConfig(&svc, fn)
			after, afterErr := revisions.Signature(svc.Template)
			templateChanged := beforeErr != nil || afterErr != nil || before != after
			if templateChanged && !regionalCPUQuotaInstance.tryDebit(project, location, serviceCPULoad(svc)) {
				regionalCPUQuotaErrorJSON(w, svc.Name)
				return
			}
			rollOutServiceRevision(svc, templateChanged)
		}
		functions.Put(name, fn)

		lro := functionsLRO(r, project, location, name, fn.wire(), "type.googleapis.com/google.cloud.functions.v2.Function")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

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
		page, next, ok := paginateList(w, r, result, gcpAsc(func(fn Function) string { return fn.Name }))
		if !ok {
			return
		}

		resp := map[string]any{"functions": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

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

	// ServeMux cannot spell `{id}:verb`, so `{functionAction}` captures
	// `<functionId>:<verb>` for the IAM verbs, generateDownloadUrl,
	// detachFunction and the seven upgrade-lifecycle verbs.
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
			// Commit makes the function 2nd gen and clears upgradeInfo; the operation
			// completes with the Function as its response.
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
		page, next, ok := paginateList(w, r, locations, gcpAsc(gcpField("name")))
		if !ok {
			return
		}
		resp := map[string]any{"locations": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// ListRuntimes answers a representative slice of the Cloud Run functions
	// runtime catalog.
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/runtimes", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"runtimes": cloudFunctionRuntimes()})
	})
}

const cloudFunctionTypeURL = "type.googleapis.com/google.cloud.functions.v2.Function"

// cloudFunctionRegions backs ListLocations with a representative slice of the
// regions Cloud Run functions serves.
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
// variables, its request timeout, which defaults to 60 seconds, its instance
// bounds and the Serverless VPC Access connector it egresses through.
func applyFunctionServiceConfig(svc *ServiceV2, fn storedFunction) {
	if svc.Template == nil || len(svc.Template.Containers) == 0 {
		return
	}
	container := &svc.Template.Containers[0]
	container.Resources = functionCPUResources(fn)
	timeout := 60
	var env []EnvVar
	svc.Template.Scaling = nil
	svc.Template.VpcAccess = nil
	if sc := fn.ServiceConfig; sc != nil {
		if sc.TimeoutSeconds > 0 {
			timeout = sc.TimeoutSeconds
		}
		if sc.MinInstanceCount > 0 || sc.MaxInstanceCount > 0 {
			svc.Template.Scaling = &RevisionScaling{MinInstanceCount: int32(sc.MinInstanceCount), MaxInstanceCount: int32(sc.MaxInstanceCount)}
		}
		if sc.VpcConnector != "" {
			svc.Template.VpcAccess = &VpcAccess{Connector: sc.VpcConnector}
			switch sc.VpcConnectorEgressSettings {
			case "PRIVATE_RANGES_ONLY", "ALL_TRAFFIC":
				svc.Template.VpcAccess.Egress = vpcEgressString(sc.VpcConnectorEgressSettings)
			}
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

func cloudFunctionName(project, location, functionID string) string {
	return fmt.Sprintf("projects/%s/locations/%s/functions/%s", project, location, functionID)
}

func applyUpgradeState(fn *storedFunction, state string) {
	if fn.UpgradeInfo == nil {
		fn.UpgradeInfo = &UpgradeInfo{}
	}
	fn.UpgradeInfo.UpgradeState = state
}

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

// applyFunctionPatch merges the updateMask fields of a PATCH body onto the
// stored function. A mask path into buildConfig or serviceConfig replaces the
// whole sub-object, which is how the provider sends grouped updates.
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
