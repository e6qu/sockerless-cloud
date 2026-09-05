package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This file completes the Lambda restJson1 control-plane slice: the
// configuration read/update pair (GetFunctionConfiguration / UpdateFunctionCode),
// the 2025-11-30 function-scaling and capacity-provider surfaces, the
// 2025-12-01 durable-execution lifecycle (start via durable Invoke, checkpoint,
// callbacks, history/state read-back), and the two legacy/streaming invoke
// entry points (InvokeAsync, InvokeWithResponseStream). All state lives in
// in-process stores whose lifetime matches the running sim, the same as the
// sibling lambda_*.go files. Durable invocations use Lambda's execution
// envelope and operation journal, including checkpoint-driven replay.

func registerLambdaExtras3(srv *sim.Server) {
	mux := srv
	lambdaResource := cloudTrailRESTResource("AWS::Lambda::Function", "name", "arn")
	lambdaCapacityProviders = sim.MakeStore[LambdaCapacityProvider](srv.DB(), "lambda_capacity_providers")
	lambdaScalingCfgs = sim.MakeStore[lambdaStoredScalingConfig](srv.DB(), "lambda_function_scaling_configs")
	lambdaDurableStore = sim.MakeStore[lambdaDurableExecution](srv.DB(), "lambda_durable_executions")
	lambdaDurableCallbackStore = sim.MakeStore[lambdaDurableCallbackState](srv.DB(), "lambda_durable_callbacks")

	// ListLayers shares GET /2018-10-31/layers with GetLayerVersionByArn; the
	// shared handler (lambda_extras.go) composes the op name per-request via
	// cloudTrailRecordedRESTDynamic, so register the static op name here so it
	// lands in the conformance REST registry the same way GetLayerVersionByArn
	// does in lambda.go.
	restRegisterOp("lambda.amazonaws.com", "ListLayers")

	// Function configuration read + code update.
	mux.HandleFunc("GET /2015-03-31/functions/{name}/configuration", cloudTrailRecordedREST("GetFunctionConfiguration", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionConfiguration", lambdaFunctionResourceARN, handleLambdaGetFunctionConfiguration)))
	mux.HandleFunc("PUT /2015-03-31/functions/{name}/code", cloudTrailRecordedREST("UpdateFunctionCode", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("UpdateFunctionCode", lambdaFunctionResourceARN, handleLambdaUpdateFunctionCode)))

	// Per-function scaling config (2025-11-30).
	mux.HandleFunc("GET /2025-11-30/functions/{name}/function-scaling-config", cloudTrailRecordedREST("GetFunctionScalingConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionScalingConfig", lambdaFunctionResourceARN, handleLambdaGetFunctionScalingConfig)))
	mux.HandleFunc("PUT /2025-11-30/functions/{name}/function-scaling-config", cloudTrailRecordedREST("PutFunctionScalingConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutFunctionScalingConfig", lambdaFunctionResourceARN, handleLambdaPutFunctionScalingConfig)))

	// Functions attached to a code-signing config.
	// A CodeSigningConfig ARN has no slashes, so a single {arn} label captures
	// it; a trailing {arn...} wildcard cannot precede the /functions suffix.
	mux.HandleFunc("GET /2020-04-22/code-signing-configs/{arn}/functions", cloudTrailRecordedREST("ListFunctionsByCodeSigningConfig", "lambda.amazonaws.com", nil, lambdaEnforced("ListFunctionsByCodeSigningConfig", nil, handleLambdaListFunctionsByCodeSigningConfig)))

	// Capacity providers (2025-11-30).
	mux.HandleFunc("POST /2025-11-30/capacity-providers", cloudTrailRecordedREST("CreateCapacityProvider", "lambda.amazonaws.com", nil, lambdaEnforced("CreateCapacityProvider", nil, handleLambdaCreateCapacityProvider)))
	mux.HandleFunc("GET /2025-11-30/capacity-providers", cloudTrailRecordedREST("ListCapacityProviders", "lambda.amazonaws.com", nil, lambdaEnforced("ListCapacityProviders", nil, handleLambdaListCapacityProviders)))
	mux.HandleFunc("GET /2025-11-30/capacity-providers/{cpname}", cloudTrailRecordedREST("GetCapacityProvider", "lambda.amazonaws.com", nil, lambdaEnforced("GetCapacityProvider", nil, handleLambdaGetCapacityProvider)))
	mux.HandleFunc("PUT /2025-11-30/capacity-providers/{cpname}", cloudTrailRecordedREST("UpdateCapacityProvider", "lambda.amazonaws.com", nil, lambdaEnforced("UpdateCapacityProvider", nil, handleLambdaUpdateCapacityProvider)))
	mux.HandleFunc("DELETE /2025-11-30/capacity-providers/{cpname}", cloudTrailRecordedREST("DeleteCapacityProvider", "lambda.amazonaws.com", nil, lambdaEnforced("DeleteCapacityProvider", nil, handleLambdaDeleteCapacityProvider)))
	mux.HandleFunc("GET /2025-11-30/capacity-providers/{cpname}/function-versions", cloudTrailRecordedREST("ListFunctionVersionsByCapacityProvider", "lambda.amazonaws.com", nil, lambdaEnforced("ListFunctionVersionsByCapacityProvider", nil, handleLambdaListFunctionVersionsByCapacityProvider)))

	// Legacy + streaming invoke entry points.
	mux.HandleFunc("POST /2014-11-13/functions/{name}/invoke-async", cloudTrailRecordedREST("InvokeAsync", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("InvokeAsync", lambdaFunctionResourceARN, handleLambdaInvokeAsync)))
	mux.HandleFunc("POST /2021-11-15/functions/{name}/response-streaming-invocations", cloudTrailRecordedREST("InvokeWithResponseStream", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("InvokeWithResponseStream", lambdaFunctionResourceARN, handleLambdaInvokeWithResponseStream)))

	// Durable executions (2025-12-01). The DurableExecutionArn is a non-greedy
	// restJson1 path label, so the SDK percent-encodes its embedded slashes
	// (%2F); a single {arn} segment captures the whole arn and ServeMux still
	// resolves the trailing /checkpoint|/history|/state|/stop literal suffix.
	mux.HandleFunc("GET /2025-12-01/functions/{name}/durable-executions", cloudTrailRecordedREST("ListDurableExecutionsByFunction", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("ListDurableExecutionsByFunction", lambdaFunctionResourceARN, handleLambdaListDurableExecutionsByFunction)))
	mux.HandleFunc("GET /2025-12-01/durable-executions/{arn}", cloudTrailRecordedREST("GetDurableExecution", "lambda.amazonaws.com", nil, lambdaEnforced("GetDurableExecution", nil, handleLambdaGetDurableExecution)))
	mux.HandleFunc("POST /2025-12-01/durable-executions/{arn}/checkpoint", cloudTrailRecordedREST("CheckpointDurableExecution", "lambda.amazonaws.com", nil, lambdaEnforced("CheckpointDurableExecution", nil, handleLambdaCheckpointDurableExecution)))
	mux.HandleFunc("GET /2025-12-01/durable-executions/{arn}/history", cloudTrailRecordedREST("GetDurableExecutionHistory", "lambda.amazonaws.com", nil, lambdaEnforced("GetDurableExecutionHistory", nil, handleLambdaGetDurableExecutionHistory)))
	mux.HandleFunc("GET /2025-12-01/durable-executions/{arn}/state", cloudTrailRecordedREST("GetDurableExecutionState", "lambda.amazonaws.com", nil, lambdaEnforced("GetDurableExecutionState", nil, handleLambdaGetDurableExecutionState)))
	mux.HandleFunc("POST /2025-12-01/durable-executions/{arn}/stop", cloudTrailRecordedREST("StopDurableExecution", "lambda.amazonaws.com", nil, lambdaEnforced("StopDurableExecution", nil, handleLambdaStopDurableExecution)))
	mux.HandleFunc("POST /2025-12-01/durable-execution-callbacks/{cbid}/succeed", cloudTrailRecordedREST("SendDurableExecutionCallbackSuccess", "lambda.amazonaws.com", nil, lambdaEnforced("SendDurableExecutionCallbackSuccess", nil, handleLambdaSendDurableCallbackSuccess)))
	mux.HandleFunc("POST /2025-12-01/durable-execution-callbacks/{cbid}/fail", cloudTrailRecordedREST("SendDurableExecutionCallbackFailure", "lambda.amazonaws.com", nil, lambdaEnforced("SendDurableExecutionCallbackFailure", nil, handleLambdaSendDurableCallbackFailure)))
	mux.HandleFunc("POST /2025-12-01/durable-execution-callbacks/{cbid}/heartbeat", cloudTrailRecordedREST("SendDurableExecutionCallbackHeartbeat", "lambda.amazonaws.com", nil, lambdaEnforced("SendDurableExecutionCallbackHeartbeat", nil, handleLambdaSendDurableCallbackHeartbeat)))
}

// GetFunctionConfiguration + UpdateFunctionCode

func handleLambdaGetFunctionConfiguration(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, _, ok := lambdaResolveInvocationTarget(name, r.URL.Query().Get("Qualifier"))
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function or version not found: %s", name)
		return
	}
	// GetFunctionConfiguration returns the FunctionConfiguration shape directly
	// (the same body GetFunction nests under "Configuration").
	sim.WriteJSON(w, http.StatusOK, lambdaConfiguration(fn))
}

func handleLambdaUpdateFunctionCode(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	var req struct {
		ZipFile         string   `json:"ZipFile"`
		S3Bucket        string   `json:"S3Bucket"`
		S3Key           string   `json:"S3Key"`
		S3ObjectVersion string   `json:"S3ObjectVersion"`
		ImageUri        string   `json:"ImageUri"`
		Architectures   []string `json:"Architectures"`
		SourceKMSKeyArn string   `json:"SourceKMSKeyArn"`
		DryRun          bool     `json:"DryRun"`
		Publish         bool     `json:"Publish"`
		RevisionId      string   `json:"RevisionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	newCode := &LambdaFunctionCode{
		S3Bucket:        req.S3Bucket,
		S3Key:           req.S3Key,
		S3ObjectVersion: req.S3ObjectVersion,
		ImageUri:        req.ImageUri,
		ZipFile:         req.ZipFile,
		SourceKMSKeyArn: req.SourceKMSKeyArn,
	}
	fn, _ := lambdaFunctions.Get(name)
	if req.RevisionId != "" && req.RevisionId != fn.RevisionId {
		AWSError(w, "PreconditionFailedException",
			"The RevisionId provided does not match the latest RevisionId for the function",
			http.StatusPreconditionFailed)
		return
	}
	if fn.PackageType == "Image" {
		if req.ImageUri == "" {
			AWSError(w, "InvalidParameterValueException",
				"ImageUri is required for an Image function", http.StatusBadRequest)
			return
		}
	} else if err := validateLambdaDeploymentPackage(newCode); err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}
	codeSize, err := lambdaDeploymentPackageSize(newCode)
	if err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}
	// DryRun validates without persisting; return the current config unchanged.
	if req.DryRun {
		sim.WriteJSON(w, http.StatusOK, lambdaConfiguration(fn))
		return
	}
	lambdaFunctions.Update(name, func(fn *LambdaFunction) {
		fn.Code = newCode
		if req.ImageUri != "" {
			fn.PackageType = "Image"
			fn.CodeSha256 = ""
		} else {
			fn.CodeSha256 = lambdaCodeSha256(newCode)
		}
		// A code change re-stamps size/revision/last-modified the way real
		// Lambda does after a successful deployment-package swap.
		fn.CodeSize = codeSize
		if len(req.Architectures) > 0 {
			fn.Architectures = req.Architectures
		}
		fn.LastModified = time.Now().UTC().Format(time.RFC3339)
		fn.LastUpdateStatus = "Successful"
		fn.RevisionId = generateUUID()
	})
	fn, _ = lambdaFunctions.Get(name)
	if req.Publish {
		version := publishLambdaVersion(name, "", fn)
		sim.WriteJSON(w, http.StatusOK, version)
		return
	}
	sim.WriteJSON(w, http.StatusOK, lambdaConfiguration(fn))
}

// Per-function scaling config

type lambdaFunctionScalingConfig struct {
	MinExecutionEnvironments *int32 `json:"MinExecutionEnvironments,omitempty"`
	MaxExecutionEnvironments *int32 `json:"MaxExecutionEnvironments,omitempty"`
}

type lambdaStoredScalingConfig struct {
	FunctionName string
	Qualifier    string
	Config       lambdaFunctionScalingConfig
}

// keyed by "<functionName>:<qualifier>" ($LATEST when no qualifier).
var lambdaScalingCfgs sim.Store[lambdaStoredScalingConfig]

func handleLambdaGetFunctionScalingConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	stored, ok := lambdaScalingCfgs.Get(lambdaEICKey(name, qualifier))
	out := map[string]any{"FunctionArn": lambdaEICArn(name, qualifier)}
	if ok {
		// Applied == Requested once the allocation settles (synchronous here).
		out["RequestedFunctionScalingConfig"] = stored.Config
		out["AppliedFunctionScalingConfig"] = stored.Config
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleLambdaPutFunctionScalingConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	var req struct {
		FunctionScalingConfig *lambdaFunctionScalingConfig `json:"FunctionScalingConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.FunctionScalingConfig == nil {
		AWSError(w, "InvalidParameterValueException", "FunctionScalingConfig is required", http.StatusBadRequest)
		return
	}
	lambdaScalingCfgs.Put(lambdaEICKey(name, qualifier), lambdaStoredScalingConfig{
		FunctionName: name, Qualifier: qualifier, Config: *req.FunctionScalingConfig,
	})
	// PutFunctionScalingConfig returns 202; the config is being applied.
	sim.WriteJSON(w, http.StatusAccepted, map[string]any{"FunctionState": "Active"})
}

// ListFunctionsByCodeSigningConfig

func handleLambdaListFunctionsByCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	if _, ok := lambdaCSCStore.Get(arn); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The code signing configuration cannot be found.")
		return
	}
	names := make([]string, 0)
	for _, attachment := range lambdaFnCSC.List() {
		if attachment.CodeSigningConfigARN == arn {
			names = append(names, lambdaArn(attachment.FunctionName))
		}
	}
	sort.Strings(names)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"FunctionArns": names})
}

// Capacity providers

// LambdaCapacityProvider mirrors the CapacityProvider shape. The members are
// echoed back exactly as supplied on create; the sim settles a created
// provider straight to Active (no asynchronous Pending window).
type LambdaCapacityProvider struct {
	name                          string
	CapacityProviderArn           string         `json:"CapacityProviderArn"`
	State                         string         `json:"State"`
	VpcConfig                     map[string]any `json:"VpcConfig,omitempty"`
	PermissionsConfig             map[string]any `json:"PermissionsConfig,omitempty"`
	InstanceRequirements          map[string]any `json:"InstanceRequirements,omitempty"`
	CapacityProviderScalingConfig map[string]any `json:"CapacityProviderScalingConfig,omitempty"`
	TelemetryConfig               map[string]any `json:"TelemetryConfig,omitempty"`
	KmsKeyArn                     string         `json:"KmsKeyArn,omitempty"`
	LastModified                  string         `json:"LastModified"`
	PropagateTags                 map[string]any `json:"PropagateTags,omitempty"`
}

var lambdaCapacityProviders sim.Store[LambdaCapacityProvider]

func lambdaCapacityProviderArn(name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:capacity-provider:%s", awsRegion(), awsAccountID(), name)
}

func lambdaCapacityProviderBody(cp LambdaCapacityProvider) map[string]any {
	out := map[string]any{
		"CapacityProviderArn": cp.CapacityProviderArn,
		"State":               cp.State,
		"LastModified":        cp.LastModified,
	}
	if cp.VpcConfig != nil {
		out["VpcConfig"] = cp.VpcConfig
	}
	if cp.PermissionsConfig != nil {
		out["PermissionsConfig"] = cp.PermissionsConfig
	}
	if cp.InstanceRequirements != nil {
		out["InstanceRequirements"] = cp.InstanceRequirements
	}
	if cp.CapacityProviderScalingConfig != nil {
		out["CapacityProviderScalingConfig"] = cp.CapacityProviderScalingConfig
	}
	if cp.TelemetryConfig != nil {
		out["TelemetryConfig"] = cp.TelemetryConfig
	}
	if cp.KmsKeyArn != "" {
		out["KmsKeyArn"] = cp.KmsKeyArn
	}
	if cp.PropagateTags != nil {
		out["PropagateTags"] = cp.PropagateTags
	}
	return out
}

func handleLambdaCreateCapacityProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CapacityProviderName          string         `json:"CapacityProviderName"`
		VpcConfig                     map[string]any `json:"VpcConfig"`
		PermissionsConfig             map[string]any `json:"PermissionsConfig"`
		InstanceRequirements          map[string]any `json:"InstanceRequirements"`
		CapacityProviderScalingConfig map[string]any `json:"CapacityProviderScalingConfig"`
		TelemetryConfig               map[string]any `json:"TelemetryConfig"`
		KmsKeyArn                     string         `json:"KmsKeyArn"`
		PropagateTags                 map[string]any `json:"PropagateTags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.CapacityProviderName == "" {
		AWSError(w, "InvalidParameterValueException", "CapacityProviderName is required", http.StatusBadRequest)
		return
	}
	operatorRole, _ := req.PermissionsConfig["CapacityProviderOperatorRoleArn"].(string)
	if operatorRole == "" {
		AWSError(w, "InvalidParameterValueException",
			"PermissionsConfig.CapacityProviderOperatorRoleArn is required", http.StatusBadRequest)
		return
	}
	subnetIDs, subnetOK := req.VpcConfig["SubnetIds"].([]any)
	securityGroupIDs, securityGroupOK := req.VpcConfig["SecurityGroupIds"].([]any)
	if !subnetOK || len(subnetIDs) == 0 || !securityGroupOK || len(securityGroupIDs) == 0 {
		AWSError(w, "InvalidParameterValueException",
			"VpcConfig.SubnetIds and VpcConfig.SecurityGroupIds are required", http.StatusBadRequest)
		return
	}
	if _, exists := lambdaCapacityProviders.Get(req.CapacityProviderName); exists {
		AWSErrorf(w, "ResourceConflictException", http.StatusConflict,
			"Capacity provider already exists: %s", req.CapacityProviderName)
		return
	}
	cp := LambdaCapacityProvider{
		name:                          req.CapacityProviderName,
		CapacityProviderArn:           lambdaCapacityProviderArn(req.CapacityProviderName),
		State:                         "Active",
		VpcConfig:                     req.VpcConfig,
		PermissionsConfig:             req.PermissionsConfig,
		InstanceRequirements:          req.InstanceRequirements,
		CapacityProviderScalingConfig: req.CapacityProviderScalingConfig,
		TelemetryConfig:               req.TelemetryConfig,
		KmsKeyArn:                     req.KmsKeyArn,
		LastModified:                  time.Now().UTC().Format(time.RFC3339),
		PropagateTags:                 req.PropagateTags,
	}
	lambdaCapacityProviders.Put(req.CapacityProviderName, cp)
	// CreateCapacityProvider returns 202: the provider is being provisioned.
	sim.WriteJSON(w, http.StatusAccepted, map[string]any{"CapacityProvider": lambdaCapacityProviderBody(cp)})
}

func handleLambdaGetCapacityProvider(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "cpname")
	cp, ok := lambdaCapacityProviders.Get(name)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Capacity provider not found: %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CapacityProvider": lambdaCapacityProviderBody(cp)})
}

func handleLambdaListCapacityProviders(w http.ResponseWriter, r *http.Request) {
	stored := lambdaCapacityProviders.List()
	stateFilter := r.URL.Query().Get("State")
	sortBy(stored, func(c LambdaCapacityProvider) string { return c.CapacityProviderArn })
	providers := make([]map[string]any, 0, len(stored))
	for _, cp := range stored {
		if stateFilter != "" && cp.State != stateFilter {
			continue
		}
		providers = append(providers, lambdaCapacityProviderBody(cp))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CapacityProviders": providers})
}

func handleLambdaUpdateCapacityProvider(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "cpname")
	if _, ok := lambdaCapacityProviders.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Capacity provider not found: %s", name)
		return
	}
	var req struct {
		CapacityProviderScalingConfig map[string]any `json:"CapacityProviderScalingConfig"`
		PropagateTags                 map[string]any `json:"PropagateTags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	lambdaCapacityProviders.Update(name, func(cp *LambdaCapacityProvider) {
		if req.CapacityProviderScalingConfig != nil {
			cp.CapacityProviderScalingConfig = req.CapacityProviderScalingConfig
		}
		if req.PropagateTags != nil {
			cp.PropagateTags = req.PropagateTags
		}
		cp.LastModified = time.Now().UTC().Format(time.RFC3339)
	})
	cp, _ := lambdaCapacityProviders.Get(name)
	// UpdateCapacityProvider returns 202.
	sim.WriteJSON(w, http.StatusAccepted, map[string]any{"CapacityProvider": lambdaCapacityProviderBody(cp)})
}

func handleLambdaDeleteCapacityProvider(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "cpname")
	cp, ok := lambdaCapacityProviders.Get(name)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Capacity provider not found: %s", name)
		return
	}
	for _, function := range lambdaFunctions.List() {
		if lambdaCapacityProviderARN(function.CapacityProviderConfig) == cp.CapacityProviderArn {
			AWSError(w, "ResourceConflictException",
				"Capacity provider is currently used by a Lambda function version", http.StatusConflict)
			return
		}
	}
	inUse := false
	for _, versions := range lambdaVersions.List() {
		for _, version := range versions {
			if lambdaCapacityProviderARN(version.CapacityProviderConfig) == cp.CapacityProviderArn {
				inUse = true
				break
			}
		}
	}
	if inUse {
		AWSError(w, "ResourceConflictException",
			"Capacity provider is currently used by a Lambda function version", http.StatusConflict)
		return
	}
	cp.State = "Deleting"
	cp.LastModified = time.Now().UTC().Format(time.RFC3339)
	sim.WriteJSON(w, http.StatusAccepted, map[string]any{
		"CapacityProvider": lambdaCapacityProviderBody(cp),
	})
	// The deletion completed as soon as its only real work—the reference
	// check and durable-store removal—finished.
	lambdaCapacityProviders.Delete(name)
}

func handleLambdaListFunctionVersionsByCapacityProvider(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "cpname")
	cp, ok := lambdaCapacityProviders.Get(name)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Capacity provider not found: %s", name)
		return
	}
	functionVersions := make([]map[string]any, 0)
	for _, function := range lambdaFunctions.List() {
		if lambdaCapacityProviderARN(function.CapacityProviderConfig) == cp.CapacityProviderArn {
			functionVersions = append(functionVersions, map[string]any{
				"FunctionArn": function.FunctionArn + ":$LATEST",
				"State":       function.State,
			})
		}
	}
	for _, versions := range lambdaVersions.List() {
		for _, version := range versions {
			if lambdaCapacityProviderARN(version.CapacityProviderConfig) == cp.CapacityProviderArn {
				functionVersions = append(functionVersions, map[string]any{
					"FunctionArn": version.FunctionArn,
					"State":       version.State,
				})
			}
		}
	}
	sort.Slice(functionVersions, func(i, j int) bool {
		return fmt.Sprint(functionVersions[i]["FunctionArn"]) < fmt.Sprint(functionVersions[j]["FunctionArn"])
	})
	maxItems := 50
	if raw := r.URL.Query().Get("MaxItems"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxItems = parsed
		}
	}
	page, nextMarker := awsPage(functionVersions, r.URL.Query().Get("Marker"), maxItems, 50)
	response := map[string]any{
		"CapacityProviderArn": cp.CapacityProviderArn,
		"FunctionVersions":    page,
	}
	if nextMarker != "" {
		response["NextMarker"] = nextMarker
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func lambdaCapacityProviderARN(config map[string]any) string {
	if config == nil {
		return ""
	}
	managed, ok := config["LambdaManagedInstancesCapacityProviderConfig"].(map[string]any)
	if !ok {
		return ""
	}
	arn, _ := managed["CapacityProviderArn"].(string)
	return arn
}

// InvokeAsync (deprecated) + InvokeWithResponseStream

func handleLambdaInvokeAsync(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, _, ok := lambdaResolveInvocationTarget(name, r.URL.Query().Get("Qualifier"))
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		AWSError(w, "InvalidRequestContentException",
			"failed to read invoke args: "+err.Error(), http.StatusBadRequest)
		return
	}
	// InvokeAsync buffers the invocation and runs it for real in the
	// background (which produces real logs), the same async path Invoke
	// with InvocationType=Event takes.
	lambdaInvokeAsynchronously(fn, payload, lambdaAsyncQualifier(name, r.URL.Query().Get("Qualifier")))
	// The deprecated InvokeAsync response binds Status to the HTTP code (202)
	// and carries no body.
	w.WriteHeader(http.StatusAccepted)
}

// awsEventStreamMessage encodes a single AWS event-stream (vnd.amazon.eventstream)
// frame: total-len, headers-len, prelude-CRC, headers, payload, message-CRC.
// Each header is name-len(1) + name + value-type(1) + value-len(2) + value.
func awsEventStreamMessage(headers map[string]string, payload []byte) []byte {
	var hb []byte
	for name, val := range headers {
		hb = append(hb, byte(len(name)))
		hb = append(hb, name...)
		hb = append(hb, 7) // value type 7 = string
		var vl [2]byte
		binary.BigEndian.PutUint16(vl[:], uint16(len(val)))
		hb = append(hb, vl[:]...)
		hb = append(hb, val...)
	}
	totalLen := uint32(16 + len(hb) + len(payload))
	msg := make([]byte, 0, totalLen)
	var prelude [8]byte
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(hb)))
	msg = append(msg, prelude[:]...)
	var preludeCRC [4]byte
	binary.BigEndian.PutUint32(preludeCRC[:], crc32.ChecksumIEEE(prelude[:]))
	msg = append(msg, preludeCRC[:]...)
	msg = append(msg, hb...)
	msg = append(msg, payload...)
	var msgCRC [4]byte
	binary.BigEndian.PutUint32(msgCRC[:], crc32.ChecksumIEEE(msg))
	msg = append(msg, msgCRC[:]...)
	return msg
}

func handleLambdaInvokeWithResponseStream(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, executedVersion, ok := lambdaResolveInvocationTarget(name, r.URL.Query().Get("Qualifier"))
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	invType := r.Header.Get("X-Amz-Invocation-Type")
	if strings.EqualFold(invType, "DryRun") {
		w.Header().Set("X-Amz-Executed-Version", executedVersion)
		w.WriteHeader(http.StatusOK)
		return
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		AWSError(w, "InvalidRequestContentException",
			"failed to read payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Run the function for real and stream its response back over the AWS
	// event-stream framing: one PayloadChunk carrying the handler's bytes,
	// then a terminal InvokeComplete event. This is the same wire framing
	// real Lambda uses for response streaming, so aws-sdk-go-v2's
	// eventstream decoder reassembles it natively.
	responseBody, unhandled, _ := invokeLambdaViaRuntimeAPI(fn, payload)
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.Header().Set("X-Amz-Executed-Version", executedVersion)
	w.WriteHeader(http.StatusOK)
	if len(responseBody) > 0 {
		_, _ = w.Write(awsEventStreamMessage(map[string]string{
			":event-type":   "PayloadChunk",
			":message-type": "event",
			":content-type": "application/octet-stream",
		}, responseBody))
	}
	complete := []byte("{}")
	if unhandled {
		complete = []byte(`{"ErrorCode":"Unhandled","ErrorDetails":"The function returned an error."}`)
	}
	_, _ = w.Write(awsEventStreamMessage(map[string]string{
		":event-type":   "InvokeComplete",
		":message-type": "event",
		":content-type": "application/json",
	}, complete))
}

// Durable executions

// lambdaDurableEvent is one history entry. Detail unions are emitted only for
// the event type that owns them, matching the Lambda durable-execution API.
type lambdaDurableEvent struct {
	EventType      string         `json:"EventType"`
	EventId        int64          `json:"EventId"`
	EventTimestamp float64        `json:"EventTimestamp"`
	StartedDetails map[string]any `json:"ExecutionStartedDetails,omitempty"`
	SucceededDet   map[string]any `json:"ExecutionSucceededDetails,omitempty"`
	FailedDet      map[string]any `json:"ExecutionFailedDetails,omitempty"`
	StoppedDet     map[string]any `json:"ExecutionStoppedDetails,omitempty"`
	TimedOutDet    map[string]any `json:"ExecutionTimedOutDetails,omitempty"`
}

// lambdaDurableOperation is one entry in the execution-state operation list.
type lambdaDurableOperation struct {
	Id                   string         `json:"Id"`
	Name                 string         `json:"Name,omitempty"`
	ParentId             string         `json:"ParentId,omitempty"`
	Type                 string         `json:"Type"`
	SubType              string         `json:"SubType,omitempty"`
	StartTimestamp       float64        `json:"StartTimestamp"`
	EndTimestamp         float64        `json:"EndTimestamp,omitempty"`
	Status               string         `json:"Status"`
	ExecutionDetails     map[string]any `json:"ExecutionDetails,omitempty"`
	StepDetails          map[string]any `json:"StepDetails,omitempty"`
	WaitDetails          map[string]any `json:"WaitDetails,omitempty"`
	CallbackDetails      map[string]any `json:"CallbackDetails,omitempty"`
	ContextDetails       map[string]any `json:"ContextDetails,omitempty"`
	ChainedInvokeDetails map[string]any `json:"ChainedInvokeDetails,omitempty"`
	callbackStartedAt    time.Time
	callbackHeartbeatAt  time.Time
	callbackTimeout      time.Duration
	heartbeatTimeout     time.Duration
}

type lambdaDurableExecution struct {
	Arn             string
	Name            string
	FunctionArn     string
	Version         string
	InputPayload    string
	Result          string
	ErrorObj        map[string]any
	Status          string // RUNNING|SUCCEEDED|FAILED|STOPPED|TIMED_OUT
	StartTS         float64
	EndTS           float64
	Events          []lambdaDurableEvent
	Operations      []lambdaDurableOperation
	DurableConfig   map[string]any
	CheckpointToken string
	ClientTokens    map[string]string
	PayloadSHA256   string
	UpdatedIDs      []string
	ChangeCh        chan struct{} `json:"-" persist:"-"`
	CoordinatorRun  bool          `json:"-" persist:"-"`
}

type lambdaDurableCallbackState struct {
	CallbackID       string
	ExecutionARN     string
	OperationID      string
	StartedAt        time.Time
	HeartbeatAt      time.Time
	Timeout          time.Duration
	HeartbeatTimeout time.Duration
}

var (
	// lambdaDurableMu guards the durable-execution stores. Reading an
	// execution, its history or its state excludes nothing but a writer, and
	// polling those three is what a durable-execution client spends its time
	// doing. A section that writes, or reads and then writes based on what it
	// read, keeps taking Lock; neither is reentrant.
	lambdaDurableMu            sync.RWMutex
	lambdaDurableStore         sim.Store[lambdaDurableExecution]
	lambdaDurableCallbackStore sim.Store[lambdaDurableCallbackState]
	// keyed by DurableExecutionArn.
	lambdaDurableExecs = map[string]*lambdaDurableExecution{}
	// CallbackId -> DurableExecutionArn, registered by a checkpoint that
	// records a CALLBACK operation; the callback ops advance that execution.
	lambdaDurableCallbacks = map[string]string{}
)

func lambdaDurableCheckpointToken() string {
	return base64.StdEncoding.EncodeToString([]byte(lambdaNewRevisionID()))
}

func lambdaBeginDurableExecution(
	function LambdaFunction,
	version, executionName string,
	payload []byte,
) (string, bool, string) {
	if executionName == "" {
		executionName = generateUUID()
	}
	if len(executionName) > 64 {
		return "", false, "DurableExecutionName must not exceed 64 characters"
	}
	qualifiedFunctionARN := function.FunctionArn
	if !strings.HasSuffix(qualifiedFunctionARN, ":"+version) {
		qualifiedFunctionARN += ":" + version
	}
	payloadDigest := fmt.Sprintf("%x", sha256.Sum256(payload))
	lambdaDurableMu.Lock()
	defer lambdaDurableMu.Unlock()
	for _, execution := range lambdaDurableExecs {
		if execution.FunctionArn != qualifiedFunctionARN || execution.Name != executionName {
			continue
		}
		if execution.PayloadSHA256 != payloadDigest {
			return "", false, "A durable execution with this name already exists with different input"
		}
		return execution.Arn, true, ""
	}
	executionID := generateUUID()
	arn := qualifiedFunctionARN + "/durable-execution/" + executionName + "/" + executionID
	now := lambdaNowEpoch()
	execution := &lambdaDurableExecution{
		Arn:             arn,
		Name:            executionName,
		FunctionArn:     qualifiedFunctionARN,
		Version:         version,
		InputPayload:    string(payload),
		Status:          "RUNNING",
		StartTS:         now,
		DurableConfig:   function.DurableConfig,
		CheckpointToken: lambdaDurableCheckpointToken(),
		ClientTokens:    map[string]string{},
		PayloadSHA256:   payloadDigest,
		ChangeCh:        make(chan struct{}, 1),
		Operations: []lambdaDurableOperation{{
			Id:             "execution",
			Type:           "EXECUTION",
			Status:         "STARTED",
			StartTimestamp: now,
			ExecutionDetails: map[string]any{
				"InputPayload": string(payload),
			},
		}},
		Events: []lambdaDurableEvent{{
			EventType:      "ExecutionStarted",
			EventId:        1,
			EventTimestamp: now,
			StartedDetails: map[string]any{
				"ExecutionTimeout": lambdaDurableExecutionTimeout(function.DurableConfig),
				"Input": map[string]any{
					"Payload":   string(payload),
					"Truncated": false,
				},
			},
		}},
	}
	lambdaDurableExecs[arn] = execution
	lambdaDurableStore.Put(arn, *execution)
	return arn, false, ""
}

func lambdaSignalDurableExecution(execution *lambdaDurableExecution) {
	lambdaDurableStore.Put(execution.Arn, *execution)
	select {
	case execution.ChangeCh <- struct{}{}:
	default:
	}
}

func lambdaFinishDurableExecution(
	execution *lambdaDurableExecution,
	status, result string,
	errorObject map[string]any,
) {
	if execution.Status != "RUNNING" {
		return
	}
	now := lambdaNowEpoch()
	execution.Status = status
	execution.EndTS = now
	event := lambdaDurableEvent{
		EventId:        int64(len(execution.Events) + 1),
		EventTimestamp: now,
	}
	switch status {
	case "SUCCEEDED":
		execution.Result = result
		event.EventType = "ExecutionSucceeded"
		event.SucceededDet = map[string]any{
			"Result": map[string]any{
				"Payload":   result,
				"Truncated": false,
			},
		}
	case "FAILED":
		execution.ErrorObj = errorObject
		event.EventType = "ExecutionFailed"
		event.FailedDet = map[string]any{"Error": errorObject}
	default:
		return
	}
	lambdaDeleteDurableCallbacks(execution.Arn)
	execution.Events = append(execution.Events, event)
	lambdaSignalDurableExecution(execution)
}

func lambdaDeleteDurableCallbacks(executionARN string) {
	for callbackID, arn := range lambdaDurableCallbacks {
		if arn == executionARN {
			delete(lambdaDurableCallbacks, callbackID)
		}
	}
	for _, callback := range lambdaDurableCallbackStore.Filter(func(callback lambdaDurableCallbackState) bool {
		return callback.ExecutionARN == executionARN
	}) {
		lambdaDurableCallbackStore.Delete(callback.CallbackID)
	}
}

// lambdaProcessDurableInvocationResponse consumes the private response
// envelope returned by an AWS Durable Execution SDK wrapper. The envelope is
// service-to-runtime protocol; Invoke callers receive only the customer result.
func lambdaProcessDurableInvocationResponse(arn string, response []byte, unhandled bool) {
	lambdaDurableMu.Lock()
	defer lambdaDurableMu.Unlock()
	execution, ok := lambdaDurableExecs[arn]
	if !ok || execution.Status != "RUNNING" {
		return
	}
	if unhandled {
		lambdaFinishDurableExecution(execution, "FAILED", "", map[string]any{
			"ErrorType":    "Runtime.Unhandled",
			"ErrorMessage": string(response),
		})
		return
	}
	var envelope struct {
		Status string         `json:"Status"`
		Result *string        `json:"Result"`
		Error  map[string]any `json:"Error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil || envelope.Status == "" {
		lambdaFinishDurableExecution(execution, "FAILED", "", map[string]any{
			"ErrorType":    "Runtime.InvalidDurableResponse",
			"ErrorMessage": "Durable function returned an invalid execution response envelope",
			"ErrorData":    string(response),
		})
		return
	}
	switch strings.ToUpper(envelope.Status) {
	case "PENDING":
		return
	case "SUCCEEDED":
		result := ""
		if envelope.Result != nil {
			result = *envelope.Result
		} else if len(execution.Operations) > 0 &&
			execution.Operations[0].ExecutionDetails != nil {
			result, _ = execution.Operations[0].ExecutionDetails["Result"].(string)
		}
		lambdaFinishDurableExecution(execution, "SUCCEEDED", result, nil)
	case "FAILED":
		if envelope.Error == nil {
			envelope.Error = map[string]any{
				"ErrorType":    "Error",
				"ErrorMessage": "Durable execution failed",
			}
		}
		lambdaFinishDurableExecution(execution, "FAILED", "", envelope.Error)
	default:
		lambdaFinishDurableExecution(execution, "FAILED", "", map[string]any{
			"ErrorType":    "Runtime.InvalidDurableResponse",
			"ErrorMessage": "Durable function returned an unknown execution status: " + envelope.Status,
		})
	}
}

func lambdaDurableInvocationPayload(arn string) ([]byte, bool) {
	lambdaDurableMu.Lock()
	defer lambdaDurableMu.Unlock()
	execution, ok := lambdaDurableExecs[arn]
	if !ok || execution.Status != "RUNNING" {
		return nil, false
	}
	operations := append([]lambdaDurableOperation(nil), execution.Operations...)
	updated := append([]string(nil), execution.UpdatedIDs...)
	execution.UpdatedIDs = nil
	lambdaDurableStore.Put(execution.Arn, *execution)
	payload, err := json.Marshal(map[string]any{
		"DurableExecutionArn": arn,
		"CheckpointToken":     execution.CheckpointToken,
		"InitialExecutionState": map[string]any{
			"Operations": operations,
		},
		"UpdatedOperationIds": updated,
	})
	return payload, err == nil
}

func lambdaDurableStatus(arn string) (status string, result []byte, unhandled bool, changed <-chan struct{}) {
	lambdaDurableMu.Lock()
	defer lambdaDurableMu.Unlock()
	execution, ok := lambdaDurableExecs[arn]
	if !ok {
		return "FAILED", lambdaErrorPayload("Durable execution not found"), true, nil
	}
	switch execution.Status {
	case "SUCCEEDED":
		return execution.Status, []byte(execution.Result), false, execution.ChangeCh
	case "FAILED", "STOPPED", "TIMED_OUT":
		if encoded, err := json.Marshal(execution.ErrorObj); err == nil && len(encoded) > 0 {
			return execution.Status, encoded, true, execution.ChangeCh
		}
		return execution.Status, lambdaErrorPayload("Durable execution " + strings.ToLower(execution.Status)), true, execution.ChangeCh
	default:
		return execution.Status, nil, false, execution.ChangeCh
	}
}

func lambdaStartDurableCoordinator(arn string, function LambdaFunction) {
	lambdaDurableMu.Lock()
	execution, ok := lambdaDurableExecs[arn]
	if !ok || execution.CoordinatorRun || execution.Status != "RUNNING" {
		lambdaDurableMu.Unlock()
		return
	}
	execution.CoordinatorRun = true
	timeoutSeconds := lambdaDurableExecutionTimeout(execution.DurableConfig)
	timeoutRemaining := time.Duration(timeoutSeconds) * time.Second
	if timeoutSeconds > 0 {
		started := time.Unix(0, int64(execution.StartTS*float64(time.Second)))
		timeoutRemaining -= time.Since(started)
		if timeoutRemaining < 0 {
			timeoutRemaining = 0
		}
	}
	changeCh := execution.ChangeCh
	lambdaDurableMu.Unlock()

	simGo(func() {
		var executionTimer <-chan time.Time
		if timeoutSeconds > 0 {
			timer := time.NewTimer(timeoutRemaining)
			defer timer.Stop()
			executionTimer = timer.C
		}
		for {
			payload, running := lambdaDurableInvocationPayload(arn)
			if !running {
				return
			}
			response, unhandled, _ := invokeLambdaViaRuntimeAPI(function, payload)
			lambdaProcessDurableInvocationResponse(arn, response, unhandled)
			status, _, _, _ := lambdaDurableStatus(arn)
			if status != "RUNNING" {
				return
			}
			select {
			case <-changeCh:
			case <-executionTimer:
				lambdaDurableMu.Lock()
				if current, exists := lambdaDurableExecs[arn]; exists && current.Status == "RUNNING" {
					now := lambdaNowEpoch()
					current.Status = "TIMED_OUT"
					current.EndTS = now
					current.ErrorObj = map[string]any{
						"ErrorType":    "ExecutionTimedOut",
						"ErrorMessage": "Durable execution exceeded its configured execution timeout",
					}
					current.Events = append(current.Events, lambdaDurableEvent{
						EventType:      "ExecutionTimedOut",
						EventId:        int64(len(current.Events) + 1),
						EventTimestamp: now,
						TimedOutDet:    map[string]any{"Error": current.ErrorObj},
					})
					lambdaDeleteDurableCallbacks(current.Arn)
					lambdaSignalDurableExecution(current)
				}
				lambdaDurableMu.Unlock()
				return
			}
		}
	})
}

func lambdaWaitForDurableExecution(ctx context.Context, arn string) ([]byte, bool) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, result, unhandled, _ := lambdaDurableStatus(arn)
		if status != "RUNNING" {
			return result, unhandled
		}
		select {
		case <-ctx.Done():
			return lambdaErrorPayload("Synchronous durable invocation ended before the execution completed: " + ctx.Err().Error()), true
		case <-ticker.C:
		}
	}
}

func lambdaDurableExecutionTimeout(config map[string]any) int {
	if timeout, ok := lambdaNumber(config["ExecutionTimeout"]); ok {
		return timeout
	}
	return 0
}

func recoverLambdaDurableExecutions() {
	lambdaDurableMu.Lock()
	lambdaDurableExecs = map[string]*lambdaDurableExecution{}
	lambdaDurableCallbacks = map[string]string{}
	running := make([]*lambdaDurableExecution, 0)
	for _, persisted := range lambdaDurableStore.List() {
		execution := persisted
		execution.ChangeCh = make(chan struct{}, 1)
		execution.CoordinatorRun = false
		lambdaDurableExecs[execution.Arn] = &execution
		if execution.Status == "RUNNING" {
			running = append(running, &execution)
		}
	}
	callbacks := lambdaDurableCallbackStore.List()
	for _, callback := range callbacks {
		if execution := lambdaDurableExecs[callback.ExecutionARN]; execution != nil &&
			execution.Status == "RUNNING" {
			lambdaDurableCallbacks[callback.CallbackID] = callback.ExecutionARN
		} else {
			lambdaDurableCallbackStore.Delete(callback.CallbackID)
		}
	}
	lambdaDurableMu.Unlock()

	now := time.Now()
	for _, execution := range running {
		for _, operation := range execution.Operations {
			if operation.Status != "STARTED" {
				continue
			}
			if operation.Type == "WAIT" {
				scheduledEnd, ok := operation.WaitDetails["ScheduledEndTimestamp"].(float64)
				if !ok {
					continue
				}
				deadline := time.Unix(0, int64(scheduledEnd*float64(time.Second)))
				remaining := time.Until(deadline)
				if deadline.Before(now) {
					remaining = 0
				}
				go lambdaCompleteDurableWait(execution.Arn, operation.Id, remaining)
			}
		}
		fnName, version, ok := lambdaParseDurableArn(execution.Arn)
		if !ok {
			continue
		}
		function, _, ok := lambdaResolveInvocationTarget(fnName, version)
		if ok {
			lambdaStartDurableCoordinator(execution.Arn, function)
		}
	}
	for _, callback := range callbacks {
		if _, ok := lambdaDurableCallbacks[callback.CallbackID]; ok &&
			(callback.Timeout > 0 || callback.HeartbeatTimeout > 0) {
			go lambdaMonitorDurableCallback(callback.CallbackID)
		}
	}
}

// lambdaParseDurableArn validates the DurableExecutionArn shape and extracts
// the embedded function name and version. Real arns look like
// arn:aws:lambda:<region>:<acct>:function:<name>:<version>/durable-execution/<name>/<id>.
func lambdaParseDurableArn(arn string) (fnName, version string, ok bool) {
	slash := strings.Index(arn, "/durable-execution/")
	if slash < 0 {
		return "", "", false
	}
	head := arn[:slash] // arn:...:function:<name>:<version>
	marker := ":function:"
	fi := strings.Index(head, marker)
	if fi < 0 {
		return "", "", false
	}
	rest := head[fi+len(marker):] // <name>:<version>
	colon := strings.LastIndex(rest, ":")
	if colon < 0 {
		return "", "", false
	}
	return rest[:colon], rest[colon+1:], true
}

// lambdaDurableExecKey resolves the {arn...} label, which the mux delivers with
// its colons intact, into the canonical DurableExecutionArn.
func lambdaDurableArnFromLabel(r *http.Request) string {
	return r.PathValue("arn")
}

func handleLambdaGetDurableExecution(w http.ResponseWriter, r *http.Request) {
	arn := lambdaDurableArnFromLabel(r)
	lambdaDurableMu.RLock()
	stored, ok := lambdaDurableExecs[arn]
	var de lambdaDurableExecution
	if ok {
		de = *stored
	}
	lambdaDurableMu.RUnlock()
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Durable execution not found: %s", arn)
		return
	}
	out := map[string]any{
		"DurableExecutionArn":  de.Arn,
		"DurableExecutionName": de.Name,
		"FunctionArn":          de.FunctionArn,
		"Status":               de.Status,
		"StartTimestamp":       de.StartTS,
	}
	if de.Version != "" {
		out["Version"] = de.Version
	}
	if de.DurableConfig != nil {
		out["DurableConfig"] = de.DurableConfig
	}
	includeExecutionData := !strings.EqualFold(r.URL.Query().Get("IncludeExecutionData"), "false")
	out["ExecutionDataIncluded"] = includeExecutionData
	if includeExecutionData {
		if de.InputPayload != "" {
			out["InputPayload"] = de.InputPayload
		}
		if de.Result != "" {
			out["Result"] = de.Result
		}
		if de.ErrorObj != nil {
			out["Error"] = de.ErrorObj
		}
	}
	if de.EndTS != 0 {
		out["EndTimestamp"] = de.EndTS
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleLambdaCheckpointDurableExecution(w http.ResponseWriter, r *http.Request) {
	arn := lambdaDurableArnFromLabel(r)
	fnName, _, ok := lambdaParseDurableArn(arn)
	if !ok {
		AWSError(w, "InvalidParameterValueException",
			"Invalid DurableExecutionArn: "+arn, http.StatusBadRequest)
		return
	}
	if _, exists := lambdaFunctions.Get(fnName); !exists {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(fnName))
		return
	}
	var req struct {
		CheckpointToken string `json:"CheckpointToken"`
		Updates         []struct {
			Id                   string         `json:"Id"`
			ParentId             string         `json:"ParentId"`
			Name                 string         `json:"Name"`
			Type                 string         `json:"Type"`
			SubType              string         `json:"SubType"`
			Action               string         `json:"Action"`
			Payload              string         `json:"Payload"`
			Error                map[string]any `json:"Error"`
			CallbackOptions      map[string]any `json:"CallbackOptions"`
			ChainedInvokeOptions map[string]any `json:"ChainedInvokeOptions"`
			ContextOptions       map[string]any `json:"ContextOptions"`
			StepOptions          map[string]any `json:"StepOptions"`
			WaitOptions          map[string]any `json:"WaitOptions"`
		} `json:"Updates"`
		ClientToken string `json:"ClientToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	now := lambdaNowEpoch()
	lambdaDurableMu.Lock()
	de, exists := lambdaDurableExecs[arn]
	if !exists {
		lambdaDurableMu.Unlock()
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Durable execution not found: %s", arn)
		return
	}
	if de.Status != "RUNNING" {
		lambdaDurableMu.Unlock()
		AWSError(w, "InvalidStateException",
			"Only a running durable execution can be checkpointed", http.StatusConflict)
		return
	}
	if req.CheckpointToken == "" || req.CheckpointToken != de.CheckpointToken {
		lambdaDurableMu.Unlock()
		AWSError(w, "InvalidParameterValueException",
			"CheckpointToken is missing, expired, or out of order", http.StatusBadRequest)
		return
	}
	if req.ClientToken != "" {
		if responseToken, duplicate := de.ClientTokens[req.ClientToken]; duplicate {
			ops := append([]lambdaDurableOperation(nil), de.Operations...)
			lambdaDurableMu.Unlock()
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"CheckpointToken": responseToken,
				"NewExecutionState": map[string]any{
					"Operations": ops,
				},
			})
			return
		}
	}
	type scheduledWait struct {
		operationID string
		delay       time.Duration
	}
	var waits []scheduledWait
	var callbackMonitors []string
	// Apply each operation update to the durable operation journal. Updates
	// target an existing operation by ID; an ID is never appended twice.
	for _, u := range req.Updates {
		if u.Id == "" || u.Type == "" || u.Action == "" {
			lambdaDurableMu.Unlock()
			AWSError(w, "InvalidParameterValueException",
				"Each update requires Id, Type, and Action", http.StatusBadRequest)
			return
		}
		index := -1
		for i := range de.Operations {
			if de.Operations[i].Id == u.Id {
				index = i
				break
			}
		}
		action := strings.ToUpper(u.Action)
		if action == "START" && index >= 0 {
			lambdaDurableMu.Unlock()
			AWSError(w, "InvalidParameterValueException",
				"Operation "+u.Id+" has already started", http.StatusBadRequest)
			return
		}
		if action != "START" && index < 0 {
			lambdaDurableMu.Unlock()
			AWSError(w, "InvalidParameterValueException",
				"Operation "+u.Id+" has not started", http.StatusBadRequest)
			return
		}
		if index < 0 {
			de.Operations = append(de.Operations, lambdaDurableOperation{
				Id:             u.Id,
				Name:           u.Name,
				ParentId:       u.ParentId,
				Type:           strings.ToUpper(u.Type),
				SubType:        u.SubType,
				StartTimestamp: now,
				Status:         "STARTED",
			})
			index = len(de.Operations) - 1
		}
		op := &de.Operations[index]
		switch action {
		case "START":
			switch op.Type {
			case "STEP":
				op.StepDetails = map[string]any{"Attempt": 1}
			case "WAIT":
				waitSeconds, valid := lambdaNumber(u.WaitOptions["WaitSeconds"])
				if !valid || waitSeconds < 0 {
					lambdaDurableMu.Unlock()
					AWSError(w, "InvalidParameterValueException",
						"WAIT operations require a non-negative WaitSeconds", http.StatusBadRequest)
					return
				}
				scheduledEnd := now + float64(waitSeconds)
				op.WaitDetails = map[string]any{"ScheduledEndTimestamp": scheduledEnd}
				waits = append(waits, scheduledWait{
					operationID: u.Id,
					delay:       time.Duration(waitSeconds) * time.Second,
				})
			case "CALLBACK":
				timeoutSeconds, timeoutOK := lambdaOptionalNonNegativeNumber(
					u.CallbackOptions, "TimeoutSeconds",
				)
				heartbeatSeconds, heartbeatOK := lambdaOptionalNonNegativeNumber(
					u.CallbackOptions, "HeartbeatTimeoutSeconds",
				)
				if !timeoutOK || !heartbeatOK {
					lambdaDurableMu.Unlock()
					AWSError(w, "InvalidParameterValueException",
						"Callback timeout values must be non-negative integers", http.StatusBadRequest)
					return
				}
				callbackID := generateUUID()
				op.CallbackDetails = map[string]any{"CallbackId": callbackID}
				op.callbackStartedAt = time.Now()
				op.callbackHeartbeatAt = op.callbackStartedAt
				op.callbackTimeout = time.Duration(timeoutSeconds) * time.Second
				op.heartbeatTimeout = time.Duration(heartbeatSeconds) * time.Second
				lambdaDurableCallbacks[callbackID] = arn
				lambdaDurableCallbackStore.Put(callbackID, lambdaDurableCallbackState{
					CallbackID: callbackID, ExecutionARN: arn, OperationID: op.Id,
					StartedAt: op.callbackStartedAt, HeartbeatAt: op.callbackHeartbeatAt,
					Timeout: op.callbackTimeout, HeartbeatTimeout: op.heartbeatTimeout,
				})
				if op.callbackTimeout > 0 || op.heartbeatTimeout > 0 {
					callbackMonitors = append(callbackMonitors, callbackID)
				}
			case "CONTEXT":
				op.ContextDetails = map[string]any{
					"ReplayChildren": u.ContextOptions["ReplayChildren"],
				}
			case "CHAINED_INVOKE":
				op.ChainedInvokeDetails = map[string]any{}
			}
		case "SUCCEED":
			op.Status = "SUCCEEDED"
			op.EndTimestamp = now
			switch op.Type {
			case "EXECUTION":
				if op.ExecutionDetails == nil {
					op.ExecutionDetails = map[string]any{}
				}
				op.ExecutionDetails["Result"] = u.Payload
			case "STEP":
				if op.StepDetails == nil {
					op.StepDetails = map[string]any{"Attempt": 1}
				}
				op.StepDetails["Result"] = u.Payload
			case "CALLBACK":
				if op.CallbackDetails == nil {
					op.CallbackDetails = map[string]any{}
				}
				op.CallbackDetails["Result"] = u.Payload
			case "CONTEXT":
				if op.ContextDetails == nil {
					op.ContextDetails = map[string]any{}
				}
				op.ContextDetails["Result"] = u.Payload
			case "CHAINED_INVOKE":
				if op.ChainedInvokeDetails == nil {
					op.ChainedInvokeDetails = map[string]any{}
				}
				op.ChainedInvokeDetails["Result"] = u.Payload
			}
		case "FAIL":
			op.Status = "FAILED"
			op.EndTimestamp = now
			switch op.Type {
			case "STEP":
				if op.StepDetails == nil {
					op.StepDetails = map[string]any{"Attempt": 1}
				}
				op.StepDetails["Error"] = u.Error
			case "CALLBACK":
				if op.CallbackDetails == nil {
					op.CallbackDetails = map[string]any{}
				}
				op.CallbackDetails["Error"] = u.Error
			case "CONTEXT":
				if op.ContextDetails == nil {
					op.ContextDetails = map[string]any{}
				}
				op.ContextDetails["Error"] = u.Error
			case "CHAINED_INVOKE":
				if op.ChainedInvokeDetails == nil {
					op.ChainedInvokeDetails = map[string]any{}
				}
				op.ChainedInvokeDetails["Error"] = u.Error
			}
		case "RETRY":
			op.Status = "PENDING"
			if op.StepDetails == nil {
				op.StepDetails = map[string]any{"Attempt": 1}
			}
			attempt, _ := lambdaNumber(op.StepDetails["Attempt"])
			op.StepDetails["Attempt"] = attempt + 1
			if delay, ok := lambdaNumber(u.StepOptions["NextAttemptDelaySeconds"]); ok {
				op.StepDetails["NextAttemptTimestamp"] = now + float64(delay)
			}
		case "CANCEL":
			op.Status = "CANCELLED"
			op.EndTimestamp = now
		default:
			lambdaDurableMu.Unlock()
			AWSError(w, "InvalidParameterValueException",
				"Unknown operation action: "+u.Action, http.StatusBadRequest)
			return
		}
		if action != "START" {
			de.UpdatedIDs = append(de.UpdatedIDs, u.Id)
		}
	}
	token := lambdaDurableCheckpointToken()
	de.CheckpointToken = token
	if req.ClientToken != "" {
		de.ClientTokens[req.ClientToken] = token
	}
	ops := append([]lambdaDurableOperation(nil), de.Operations...)
	lambdaDurableStore.Put(de.Arn, *de)
	lambdaDurableMu.Unlock()
	for _, pendingWait := range waits {
		go lambdaCompleteDurableWait(arn, pendingWait.operationID, pendingWait.delay)
	}
	for _, callbackID := range callbackMonitors {
		go lambdaMonitorDurableCallback(callbackID)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"CheckpointToken": token,
		"NewExecutionState": map[string]any{
			"Operations": ops,
		},
	})
}

func lambdaOptionalNonNegativeNumber(values map[string]any, key string) (int, bool) {
	value, exists := values[key]
	if !exists {
		return 0, true
	}
	number, ok := lambdaNumber(value)
	return number, ok && number >= 0
}

func lambdaMonitorDurableCallback(callbackID string) {
	for {
		lambdaDurableMu.Lock()
		arn, exists := lambdaDurableCallbacks[callbackID]
		execution := lambdaDurableExecs[arn]
		var operation *lambdaDurableOperation
		if exists && execution != nil && execution.Status == "RUNNING" {
			for i := range execution.Operations {
				candidate := &execution.Operations[i]
				if candidate.CallbackDetails != nil &&
					candidate.CallbackDetails["CallbackId"] == callbackID &&
					candidate.Status == "STARTED" {
					operation = candidate
					break
				}
			}
		}
		if operation == nil {
			lambdaDurableMu.Unlock()
			return
		}
		state, stored := lambdaDurableCallbackStore.Get(callbackID)
		if !stored {
			lambdaDurableMu.Unlock()
			return
		}
		now := time.Now()
		nextDeadline := time.Time{}
		timeoutKind := ""
		if state.Timeout > 0 {
			nextDeadline = state.StartedAt.Add(state.Timeout)
			timeoutKind = "CallbackTimeout"
		}
		if state.HeartbeatTimeout > 0 {
			heartbeatDeadline := state.HeartbeatAt.Add(state.HeartbeatTimeout)
			if nextDeadline.IsZero() || heartbeatDeadline.Before(nextDeadline) {
				nextDeadline = heartbeatDeadline
				timeoutKind = "CallbackHeartbeatTimeout"
			}
		}
		if !nextDeadline.After(now) {
			operation.Status = "TIMED_OUT"
			operation.EndTimestamp = lambdaNowEpoch()
			operation.CallbackDetails["Error"] = map[string]any{
				"ErrorType":    timeoutKind,
				"ErrorMessage": "Durable callback exceeded its configured timeout",
			}
			execution.UpdatedIDs = append(execution.UpdatedIDs, operation.Id)
			delete(lambdaDurableCallbacks, callbackID)
			lambdaDurableCallbackStore.Delete(callbackID)
			lambdaSignalDurableExecution(execution)
			lambdaDurableMu.Unlock()
			return
		}
		wait := time.Until(nextDeadline)
		lambdaDurableMu.Unlock()

		timer := time.NewTimer(wait)
		<-timer.C
	}
}

func lambdaCompleteDurableWait(arn, operationID string, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	lambdaDurableMu.Lock()
	defer lambdaDurableMu.Unlock()
	execution, ok := lambdaDurableExecs[arn]
	if !ok || execution.Status != "RUNNING" {
		return
	}
	for i := range execution.Operations {
		operation := &execution.Operations[i]
		if operation.Id != operationID || operation.Status != "STARTED" {
			continue
		}
		operation.Status = "SUCCEEDED"
		operation.EndTimestamp = lambdaNowEpoch()
		execution.UpdatedIDs = append(execution.UpdatedIDs, operationID)
		lambdaSignalDurableExecution(execution)
		return
	}
}

func handleLambdaGetDurableExecutionHistory(w http.ResponseWriter, r *http.Request) {
	arn := lambdaDurableArnFromLabel(r)
	lambdaDurableMu.RLock()
	de, ok := lambdaDurableExecs[arn]
	var events []lambdaDurableEvent
	if ok {
		events = append([]lambdaDurableEvent(nil), de.Events...)
	}
	lambdaDurableMu.RUnlock()
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Durable execution not found: %s", arn)
		return
	}
	if r.URL.Query().Get("ReverseOrder") == "true" {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	maxItems := 100
	if raw := r.URL.Query().Get("MaxItems"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 || parsed > 1000 {
			AWSError(w, "InvalidParameterValueException",
				"MaxItems must be between 0 and 1000", http.StatusBadRequest)
			return
		}
		if parsed > 0 {
			maxItems = parsed
		}
	}
	page, nextMarker := awsPage(events, r.URL.Query().Get("Marker"), maxItems, 100)
	response := map[string]any{"Events": page}
	if nextMarker != "" {
		response["NextMarker"] = nextMarker
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func handleLambdaGetDurableExecutionState(w http.ResponseWriter, r *http.Request) {
	arn := lambdaDurableArnFromLabel(r)
	lambdaDurableMu.RLock()
	de, ok := lambdaDurableExecs[arn]
	var ops []lambdaDurableOperation
	checkpointToken := ""
	if ok {
		ops = append([]lambdaDurableOperation(nil), de.Operations...)
		checkpointToken = de.CheckpointToken
	}
	lambdaDurableMu.RUnlock()
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Durable execution not found: %s", arn)
		return
	}
	if token := r.URL.Query().Get("CheckpointToken"); token == "" || token != checkpointToken {
		AWSError(w, "InvalidParameterValueException",
			"CheckpointToken is missing, expired, or out of order", http.StatusBadRequest)
		return
	}
	maxItems := 100
	if raw := r.URL.Query().Get("MaxItems"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 1000 {
			if parsed > 0 {
				maxItems = parsed
			}
		} else {
			AWSError(w, "InvalidParameterValueException",
				"MaxItems must be between 0 and 1000", http.StatusBadRequest)
			return
		}
	}
	page, nextMarker := awsPage(ops, r.URL.Query().Get("Marker"), maxItems, 100)
	response := map[string]any{"Operations": page}
	if nextMarker != "" {
		response["NextMarker"] = nextMarker
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func handleLambdaListDurableExecutionsByFunction(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	fnArn := lambdaArn(name)
	statusFilter := r.URL.Query()["Statuses"]
	executionNameFilter := r.URL.Query().Get("DurableExecutionName")
	qualifier := r.URL.Query().Get("Qualifier")
	qualifiedVersion := "$LATEST"
	if qualifier != "" {
		_, resolvedVersion, resolved := lambdaResolveInvocationTarget(name, qualifier)
		if !resolved {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function not found: %s:%s", fnArn, qualifier)
			return
		}
		qualifiedVersion = resolvedVersion
	}
	startedAfter, parseErr := lambdaOptionalRFC3339(r.URL.Query().Get("StartedAfter"))
	if parseErr != nil {
		AWSError(w, "InvalidParameterValueException",
			"StartedAfter must be an ISO 8601 timestamp", http.StatusBadRequest)
		return
	}
	startedBefore, parseErr := lambdaOptionalRFC3339(r.URL.Query().Get("StartedBefore"))
	if parseErr != nil {
		AWSError(w, "InvalidParameterValueException",
			"StartedBefore must be an ISO 8601 timestamp", http.StatusBadRequest)
		return
	}
	lambdaDurableMu.RLock()
	executions := make([]map[string]any, 0)
	for _, de := range lambdaDurableExecs {
		if de.FunctionArn != fnArn+":"+qualifiedVersion {
			continue
		}
		if executionNameFilter != "" && de.Name != executionNameFilter {
			continue
		}
		started := time.Unix(0, int64(de.StartTS*float64(time.Second))).UTC()
		if !startedAfter.IsZero() && !started.After(startedAfter) {
			continue
		}
		if !startedBefore.IsZero() && !started.Before(startedBefore) {
			continue
		}
		if len(statusFilter) > 0 {
			match := false
			for _, s := range statusFilter {
				if s == de.Status {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		item := map[string]any{
			"DurableExecutionArn":  de.Arn,
			"DurableExecutionName": de.Name,
			"FunctionArn":          de.FunctionArn,
			"Status":               de.Status,
			"StartTimestamp":       de.StartTS,
		}
		if de.EndTS != 0 {
			item["EndTimestamp"] = de.EndTS
		}
		executions = append(executions, item)
	}
	lambdaDurableMu.RUnlock()
	sort.Slice(executions, func(i, j int) bool {
		left, leftOK := executions[i]["StartTimestamp"].(float64)
		right, rightOK := executions[j]["StartTimestamp"].(float64)
		if !leftOK || !rightOK {
			return fmt.Sprint(executions[i]["DurableExecutionArn"]) < fmt.Sprint(executions[j]["DurableExecutionArn"])
		}
		if left == right {
			return fmt.Sprint(executions[i]["DurableExecutionArn"]) < fmt.Sprint(executions[j]["DurableExecutionArn"])
		}
		if strings.EqualFold(r.URL.Query().Get("ReverseOrder"), "true") {
			return left < right
		}
		return left > right
	})
	maxItems := 100
	if raw := r.URL.Query().Get("MaxItems"); raw != "" {
		parsed, maxErr := strconv.Atoi(raw)
		if maxErr != nil || parsed < 1 || parsed > 1000 {
			AWSError(w, "InvalidParameterValueException",
				"MaxItems must be between 1 and 1000", http.StatusBadRequest)
			return
		}
		maxItems = parsed
	}
	page, nextMarker := awsPage(executions, r.URL.Query().Get("Marker"), maxItems, 100)
	response := map[string]any{"DurableExecutions": page}
	if nextMarker != "" {
		response["NextMarker"] = nextMarker
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func lambdaOptionalRFC3339(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

func handleLambdaStopDurableExecution(w http.ResponseWriter, r *http.Request) {
	arn := lambdaDurableArnFromLabel(r)
	var req struct {
		Error map[string]any `json:"Error"`
	}
	_ = sim.ReadJSON(r, &req)
	now := lambdaNowEpoch()
	lambdaDurableMu.Lock()
	de, ok := lambdaDurableExecs[arn]
	if ok {
		if de.Status != "RUNNING" {
			lambdaDurableMu.Unlock()
			AWSError(w, "InvalidStateException",
				"Only a running durable execution can be stopped", http.StatusConflict)
			return
		}
		de.Status = "STOPPED"
		de.EndTS = now
		de.Events = append(de.Events, lambdaDurableEvent{
			EventType:      "ExecutionStopped",
			EventId:        int64(len(de.Events) + 1),
			EventTimestamp: now,
			StoppedDet:     map[string]any{"Error": req.Error},
		})
		if req.Error != nil {
			de.ErrorObj = req.Error
		}
		lambdaDeleteDurableCallbacks(de.Arn)
		lambdaSignalDurableExecution(de)
	}
	lambdaDurableMu.Unlock()
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Durable execution not found: %s", arn)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"StopTimestamp": now})
}

// lambdaAdvanceCallback resolves a CallbackId to its operation, persists the
// callback result, and wakes the replay coordinator. A heartbeat validates the
// live callback without making it terminal.
func lambdaAdvanceCallback(callbackID, status, result string, errorObject map[string]any) bool {
	lambdaDurableMu.Lock()
	defer lambdaDurableMu.Unlock()
	arn, ok := lambdaDurableCallbacks[callbackID]
	if !ok {
		return false
	}
	de, ok := lambdaDurableExecs[arn]
	if !ok {
		return false
	}
	now := lambdaNowEpoch()
	for i := range de.Operations {
		operation := &de.Operations[i]
		if operation.CallbackDetails == nil ||
			operation.CallbackDetails["CallbackId"] != callbackID {
			continue
		}
		if status == "" {
			if operation.Status != "STARTED" {
				return false
			}
			operation.callbackHeartbeatAt = time.Now()
			state, exists := lambdaDurableCallbackStore.Get(callbackID)
			if exists {
				state.HeartbeatAt = operation.callbackHeartbeatAt
				lambdaDurableCallbackStore.Put(callbackID, state)
			}
			return true
		}
		if operation.Status != "STARTED" {
			return false
		}
		operation.Status = status
		operation.EndTimestamp = now
		if status == "SUCCEEDED" {
			operation.CallbackDetails["Result"] = result
		} else {
			operation.CallbackDetails["Error"] = errorObject
		}
		de.UpdatedIDs = append(de.UpdatedIDs, operation.Id)
		delete(lambdaDurableCallbacks, callbackID)
		lambdaDurableCallbackStore.Delete(callbackID)
		lambdaSignalDurableExecution(de)
		return true
	}
	return false
}

func handleLambdaSendDurableCallbackSuccess(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "cbid")
	result, err := io.ReadAll(io.LimitReader(r.Body, 256*1024+1))
	if err != nil || len(result) > 256*1024 {
		AWSError(w, "InvalidParameterValueException",
			"Callback result exceeds the 256 KB limit", http.StatusBadRequest)
		return
	}
	if !lambdaAdvanceCallback(id, "SUCCEEDED", string(result), nil) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Callback not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleLambdaSendDurableCallbackFailure(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "cbid")
	var req struct {
		Error map[string]any `json:"Error"`
	}
	if err := sim.ReadJSON(r, &req); err != nil || req.Error == nil {
		AWSError(w, "InvalidParameterValueException", "Error is required", http.StatusBadRequest)
		return
	}
	if !lambdaAdvanceCallback(id, "FAILED", "", req.Error) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Callback not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleLambdaSendDurableCallbackHeartbeat(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "cbid")
	if !lambdaAdvanceCallback(id, "", "", nil) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Callback not found: %s", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
