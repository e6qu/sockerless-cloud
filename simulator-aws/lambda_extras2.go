package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerLambdaExtras2(srv *sim.Server) {
	mux := srv
	lambdaEICs = sim.MakeStore[LambdaEventInvokeConfig](srv.DB(), "lambda_event_invoke_configs")
	lambdaPCs = sim.MakeStore[LambdaProvisionedConcurrency](srv.DB(), "lambda_provisioned_concurrency")
	lambdaFnCSC = sim.MakeStore[lambdaFunctionCodeSigningConfig](srv.DB(), "lambda_function_code_signing_configs")
	lambdaRTMs = sim.MakeStore[lambdaRuntimeMgmt](srv.DB(), "lambda_runtime_management")
	lambdaRecursion = sim.MakeStore[string](srv.DB(), "lambda_recursion_configs")
	lambdaLayerPolicies = sim.MakeStore[lambdaLayerPolicy](srv.DB(), "lambda_layer_policies")
	lambdaResource := cloudTrailRESTResource("AWS::Lambda::Function", "name", "arn")

	mux.HandleFunc("PUT /2019-09-25/functions/{name}/event-invoke-config", cloudTrailRecordedREST("PutFunctionEventInvokeConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutFunctionEventInvokeConfig", lambdaFunctionResourceARN, handleLambdaPutFunctionEventInvokeConfig)))
	mux.HandleFunc("GET /2019-09-25/functions/{name}/event-invoke-config", cloudTrailRecordedREST("GetFunctionEventInvokeConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionEventInvokeConfig", lambdaFunctionResourceARN, handleLambdaGetFunctionEventInvokeConfig)))
	mux.HandleFunc("POST /2019-09-25/functions/{name}/event-invoke-config", cloudTrailRecordedREST("UpdateFunctionEventInvokeConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("UpdateFunctionEventInvokeConfig", lambdaFunctionResourceARN, handleLambdaUpdateFunctionEventInvokeConfig)))
	mux.HandleFunc("DELETE /2019-09-25/functions/{name}/event-invoke-config", cloudTrailRecordedREST("DeleteFunctionEventInvokeConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteFunctionEventInvokeConfig", lambdaFunctionResourceARN, handleLambdaDeleteFunctionEventInvokeConfig)))
	mux.HandleFunc("GET /2019-09-25/functions/{name}/event-invoke-config/list", cloudTrailRecordedREST("ListFunctionEventInvokeConfigs", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("ListFunctionEventInvokeConfigs", lambdaFunctionResourceARN, handleLambdaListFunctionEventInvokeConfigs)))

	mux.HandleFunc("PUT /2019-09-30/functions/{name}/provisioned-concurrency", cloudTrailRecordedREST("PutProvisionedConcurrencyConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutProvisionedConcurrencyConfig", lambdaFunctionResourceARN, handleLambdaPutProvisionedConcurrencyConfig)))
	mux.HandleFunc("GET /2019-09-30/functions/{name}/provisioned-concurrency", cloudTrailRecordedRESTDynamic(func(r *http.Request, _ []byte) string { return lambdaProvisionedConcurrencyOpName(r) }, "lambda.amazonaws.com", lambdaResource, lambdaEnforcedDynamic(lambdaProvisionedConcurrencyOpName, lambdaFunctionResourceARN, handleLambdaGetProvisionedConcurrencyConfig)))
	mux.HandleFunc("DELETE /2019-09-30/functions/{name}/provisioned-concurrency", cloudTrailRecordedREST("DeleteProvisionedConcurrencyConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteProvisionedConcurrencyConfig", lambdaFunctionResourceARN, handleLambdaDeleteProvisionedConcurrencyConfig)))
	// GetProvisionedConcurrencyConfig and ListProvisionedConcurrencyConfigs share
	// the path, split by ?List=ALL, which ServeMux cannot match on; the GET handler
	// dispatches on it, so register both operation names.
	restRegisterOp("lambda.amazonaws.com", "GetProvisionedConcurrencyConfig")
	restRegisterOp("lambda.amazonaws.com", "ListProvisionedConcurrencyConfigs")

	mux.HandleFunc("POST /2020-04-22/code-signing-configs", cloudTrailRecordedREST("CreateCodeSigningConfig", "lambda.amazonaws.com", nil, lambdaEnforced("CreateCodeSigningConfig", nil, handleLambdaCreateCodeSigningConfig)))
	mux.HandleFunc("GET /2020-04-22/code-signing-configs", cloudTrailRecordedREST("ListCodeSigningConfigs", "lambda.amazonaws.com", nil, lambdaEnforced("ListCodeSigningConfigs", nil, handleLambdaListCodeSigningConfigs)))
	mux.HandleFunc("GET /2020-04-22/code-signing-configs/{arn...}", cloudTrailRecordedREST("GetCodeSigningConfig", "lambda.amazonaws.com", nil, lambdaEnforced("GetCodeSigningConfig", nil, handleLambdaGetCodeSigningConfig)))
	mux.HandleFunc("PUT /2020-04-22/code-signing-configs/{arn...}", cloudTrailRecordedREST("UpdateCodeSigningConfig", "lambda.amazonaws.com", nil, lambdaEnforced("UpdateCodeSigningConfig", nil, handleLambdaUpdateCodeSigningConfig)))
	mux.HandleFunc("DELETE /2020-04-22/code-signing-configs/{arn...}", cloudTrailRecordedREST("DeleteCodeSigningConfig", "lambda.amazonaws.com", nil, lambdaEnforced("DeleteCodeSigningConfig", nil, handleLambdaDeleteCodeSigningConfig)))

	mux.HandleFunc("PUT /2020-06-30/functions/{name}/code-signing-config", cloudTrailRecordedREST("PutFunctionCodeSigningConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutFunctionCodeSigningConfig", lambdaFunctionResourceARN, handleLambdaPutFunctionCodeSigningConfig)))
	mux.HandleFunc("GET /2020-06-30/functions/{name}/code-signing-config", cloudTrailRecordedREST("GetFunctionCodeSigningConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionCodeSigningConfig", lambdaFunctionResourceARN, handleLambdaGetFunctionCodeSigningConfig)))
	mux.HandleFunc("DELETE /2020-06-30/functions/{name}/code-signing-config", cloudTrailRecordedREST("DeleteFunctionCodeSigningConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteFunctionCodeSigningConfig", lambdaFunctionResourceARN, handleLambdaDeleteFunctionCodeSigningConfig)))

	mux.HandleFunc("GET /2021-07-20/functions/{name}/runtime-management-config", cloudTrailRecordedREST("GetRuntimeManagementConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetRuntimeManagementConfig", lambdaFunctionResourceARN, handleLambdaGetRuntimeManagementConfig)))
	mux.HandleFunc("PUT /2021-07-20/functions/{name}/runtime-management-config", cloudTrailRecordedREST("PutRuntimeManagementConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutRuntimeManagementConfig", lambdaFunctionResourceARN, handleLambdaPutRuntimeManagementConfig)))

	mux.HandleFunc("GET /2016-08-19/account-settings", cloudTrailRecordedREST("GetAccountSettings", "lambda.amazonaws.com", nil, lambdaEnforced("GetAccountSettings", nil, handleLambdaGetAccountSettings)))
	mux.HandleFunc("GET /2016-08-19/account-settings/", cloudTrailRecordedREST("GetAccountSettings", "lambda.amazonaws.com", nil, lambdaEnforced("GetAccountSettings", nil, handleLambdaGetAccountSettings)))

	mux.HandleFunc("GET /2024-08-31/functions/{name}/recursion-config", cloudTrailRecordedREST("GetFunctionRecursionConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionRecursionConfig", lambdaFunctionResourceARN, handleLambdaGetFunctionRecursionConfig)))
	mux.HandleFunc("PUT /2024-08-31/functions/{name}/recursion-config", cloudTrailRecordedREST("PutFunctionRecursionConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutFunctionRecursionConfig", lambdaFunctionResourceARN, handleLambdaPutFunctionRecursionConfig)))

	mux.HandleFunc("POST /2018-10-31/layers/{layer}/versions/{version}/policy", cloudTrailRecordedREST("AddLayerVersionPermission", "lambda.amazonaws.com", nil, lambdaEnforced("AddLayerVersionPermission", nil, handleLambdaAddLayerVersionPermission)))
	mux.HandleFunc("GET /2018-10-31/layers/{layer}/versions/{version}/policy", cloudTrailRecordedREST("GetLayerVersionPolicy", "lambda.amazonaws.com", nil, lambdaEnforced("GetLayerVersionPolicy", nil, handleLambdaGetLayerVersionPolicy)))
	mux.HandleFunc("DELETE /2018-10-31/layers/{layer}/versions/{version}/policy/{statement}", cloudTrailRecordedREST("RemoveLayerVersionPermission", "lambda.amazonaws.com", nil, lambdaEnforced("RemoveLayerVersionPermission", nil, handleLambdaRemoveLayerVersionPermission)))
}

type lambdaDestination struct {
	Destination string `json:"Destination,omitempty"`
}

type lambdaDestinationConfig struct {
	OnSuccess *lambdaDestination `json:"OnSuccess,omitempty"`
	OnFailure *lambdaDestination `json:"OnFailure,omitempty"`
}

type LambdaEventInvokeConfig struct {
	LastModified             float64                  `json:"LastModified"`
	FunctionArn              string                   `json:"FunctionArn"`
	MaximumRetryAttempts     *int                     `json:"MaximumRetryAttempts,omitempty"`
	MaximumEventAgeInSeconds *int                     `json:"MaximumEventAgeInSeconds,omitempty"`
	DestinationConfig        *lambdaDestinationConfig `json:"DestinationConfig,omitempty"`
}

// Keyed by "<functionName>:<qualifier>", $LATEST without a qualifier.
var lambdaEICs sim.Store[LambdaEventInvokeConfig]

func lambdaEICKey(name, qualifier string) string {
	if qualifier == "" {
		qualifier = "$LATEST"
	}
	return name + ":" + qualifier
}

// lambdaEICArn returns the FunctionArn echoed in an event-invoke config; the
// arn carries the qualifier suffix when one is set, matching real Lambda.
func lambdaEICArn(name, qualifier string) string {
	arn := lambdaArn(name)
	if qualifier != "" && qualifier != "$LATEST" {
		arn += ":" + qualifier
	}
	return arn
}

func handleLambdaPutFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	var req struct {
		MaximumRetryAttempts     *int                     `json:"MaximumRetryAttempts"`
		MaximumEventAgeInSeconds *int                     `json:"MaximumEventAgeInSeconds"`
		DestinationConfig        *lambdaDestinationConfig `json:"DestinationConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	cfg := LambdaEventInvokeConfig{
		LastModified:             lambdaNowEpoch(),
		FunctionArn:              lambdaEICArn(name, qualifier),
		MaximumRetryAttempts:     req.MaximumRetryAttempts,
		MaximumEventAgeInSeconds: req.MaximumEventAgeInSeconds,
		DestinationConfig:        req.DestinationConfig,
	}
	lambdaEICs.Put(lambdaEICKey(name, qualifier), cfg)
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleLambdaUpdateFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	var req struct {
		MaximumRetryAttempts     *int                     `json:"MaximumRetryAttempts"`
		MaximumEventAgeInSeconds *int                     `json:"MaximumEventAgeInSeconds"`
		DestinationConfig        *lambdaDestinationConfig `json:"DestinationConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := lambdaEICKey(name, qualifier)
	cfg, ok := lambdaEICs.Get(key)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s does not have an EventInvokeConfig.", lambdaEICArn(name, qualifier))
		return
	}
	// Update merges: only members present in the request overwrite.
	if req.MaximumRetryAttempts != nil {
		cfg.MaximumRetryAttempts = req.MaximumRetryAttempts
	}
	if req.MaximumEventAgeInSeconds != nil {
		cfg.MaximumEventAgeInSeconds = req.MaximumEventAgeInSeconds
	}
	if req.DestinationConfig != nil {
		cfg.DestinationConfig = req.DestinationConfig
	}
	cfg.LastModified = lambdaNowEpoch()
	lambdaEICs.Put(key, cfg)
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleLambdaGetFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	cfg, ok := lambdaEICs.Get(lambdaEICKey(name, qualifier))
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s does not have an EventInvokeConfig.", lambdaEICArn(name, qualifier))
		return
	}
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleLambdaDeleteFunctionEventInvokeConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	lambdaEICs.Delete(lambdaEICKey(name, qualifier))
	w.WriteHeader(http.StatusNoContent)
}

func handleLambdaListFunctionEventInvokeConfigs(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	functionARN := lambdaArn(name)
	configs := lambdaEICs.Filter(func(cfg LambdaEventInvokeConfig) bool {
		return cfg.FunctionArn == functionARN || strings.HasPrefix(cfg.FunctionArn, functionARN+":")
	})
	sort.Slice(configs, func(i, j int) bool { return configs[i].FunctionArn < configs[j].FunctionArn })
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"FunctionEventInvokeConfigs": configs,
	})
}

// LambdaProvisionedConcurrency is a provisioned concurrency configuration of
// an alias or version. Where its environments stand is read from the
// version's pool (lambdaProvisionedState), not stored.
type LambdaProvisionedConcurrency struct {
	FunctionName string `json:"FunctionName"`
	Qualifier    string `json:"Qualifier"`
	Requested    int    `json:"Requested"`
	LastModified string `json:"LastModified"`
}

// keyed by "<functionName>:<qualifier>".
var lambdaPCs sim.Store[LambdaProvisionedConcurrency]

func lambdaProvisionedConcurrencyBody(pc LambdaProvisionedConcurrency, includeArn bool) map[string]any {
	status, allocated, available, reason := lambdaProvisionedState(pc)
	out := map[string]any{
		"RequestedProvisionedConcurrentExecutions": pc.Requested,
		"AvailableProvisionedConcurrentExecutions": available,
		"AllocatedProvisionedConcurrentExecutions": allocated,
		"Status":       status,
		"LastModified": pc.LastModified,
	}
	if reason != "" {
		out["StatusReason"] = reason
	}
	if includeArn {
		out["FunctionArn"] = lambdaEICArn(pc.FunctionName, pc.Qualifier)
	}
	return out
}

func handleLambdaPutProvisionedConcurrencyConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	if qualifier == "" || qualifier == "$LATEST" {
		// "You cannot use provisioned concurrency with the $LATEST version of
		// any function."
		AWSError(w, "InvalidParameterValueException",
			"Provisioned Concurrency Configs cannot be applied to unpublished function versions.", http.StatusBadRequest)
		return
	}
	aliases, _ := lambdaAliases.Get(name)
	if _, isAlias := aliases[qualifier]; !isAlias {
		if _, isVersion := lambdaFunctionVersion(name, qualifier); !isVersion {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function not found: %s:%s", lambdaArn(name), qualifier)
			return
		}
	}
	var req struct {
		ProvisionedConcurrentExecutions *int `json:"ProvisionedConcurrentExecutions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ProvisionedConcurrentExecutions == nil || *req.ProvisionedConcurrentExecutions < 1 {
		AWSError(w, "InvalidParameterValueException",
			"'ProvisionedConcurrentExecutions' failed to satisfy constraint: Member must have value greater than or equal to 1",
			http.StatusBadRequest)
		return
	}
	requested := *req.ProvisionedConcurrentExecutions
	others := 0
	for _, pc := range lambdaPCs.List() {
		if pc.FunctionName == name && pc.Qualifier != qualifier {
			others += pc.Requested
		}
	}
	// "The maximum provisioned concurrency you can assign to a specific
	// function version is the function's reserved concurrency minus the
	// provisioned concurrency on other function versions." Without reserved
	// concurrency the bound is the account's: "You can configure up to the
	// Unreserved account concurrency in your account, minus 100."
	if reserved, has := lambdaConcurrency.Get(name); has {
		if others+requested > reserved {
			AWSError(w, "InvalidParameterValueException",
				"Requested Provisioned Concurrency should not be greater than the reservedConcurrentExecution for function",
				http.StatusBadRequest)
			return
		}
	} else if limit := lambdaUnreservedConcurrency() - lambdaMinimumUnreservedConcurrency - lambdaUnreservedProvisioned(name); others+requested > limit {
		AWSErrorf(w, "InvalidParameterValueException", http.StatusBadRequest,
			"Requested Provisioned Concurrency exceeds the account's unreserved concurrency minus %d (%d available)",
			lambdaMinimumUnreservedConcurrency, limit)
		return
	}
	pc := LambdaProvisionedConcurrency{
		FunctionName: name,
		Qualifier:    qualifier,
		Requested:    requested,
		LastModified: time.Now().UTC().Format(time.RFC3339),
	}
	lambdaPCs.Put(name+":"+qualifier, pc)
	lambdaEnvironments.reconcile(name)
	// PutProvisionedConcurrencyConfig answers 202: the environments are being
	// allocated, and Status reads IN_PROGRESS until they all have
	// initialised.
	sim.WriteJSON(w, http.StatusAccepted, lambdaProvisionedConcurrencyBody(pc, false))
}

// lambdaUnreservedProvisioned is the provisioned concurrency configured on
// the functions other than except that reserve none, which the account's
// unreserved concurrency holds.
func lambdaUnreservedProvisioned(except string) int {
	total := 0
	for _, pc := range lambdaPCs.List() {
		if pc.FunctionName == except {
			continue
		}
		if _, reserved := lambdaConcurrency.Get(pc.FunctionName); !reserved {
			total += pc.Requested
		}
	}
	return total
}

func handleLambdaGetProvisionedConcurrencyConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	// ListProvisionedConcurrencyConfigs shares this path keyed on ?List=ALL.
	if r.URL.Query().Get("List") == "ALL" {
		lambdaListProvisionedConcurrencyConfigs(w, name)
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	pc, ok := lambdaPCs.Get(name + ":" + qualifier)
	if !ok {
		AWSErrorf(w, "ProvisionedConcurrencyConfigNotFoundException", http.StatusNotFound,
			"No Provisioned Concurrency Config found for this function")
		return
	}
	sim.WriteJSON(w, http.StatusOK, lambdaProvisionedConcurrencyBody(pc, false))
}

func lambdaListProvisionedConcurrencyConfigs(w http.ResponseWriter, name string) {
	configs := make([]map[string]any, 0)
	for _, pc := range lambdaPCs.List() {
		if pc.FunctionName == name {
			configs = append(configs, lambdaProvisionedConcurrencyBody(pc, true))
		}
	}
	sort.Slice(configs, func(i, j int) bool {
		return fmt.Sprint(configs[i]["FunctionArn"]) < fmt.Sprint(configs[j]["FunctionArn"])
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ProvisionedConcurrencyConfigs": configs,
	})
}

func handleLambdaDeleteProvisionedConcurrencyConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	lambdaPCs.Delete(name + ":" + qualifier)
	lambdaEnvironments.reconcile(name)
	w.WriteHeader(http.StatusNoContent)
}

type lambdaAllowedPublishers struct {
	SigningProfileVersionArns []string `json:"SigningProfileVersionArns,omitempty"`
}

type lambdaCodeSigningPolicies struct {
	UntrustedArtifactOnDeployment string `json:"UntrustedArtifactOnDeployment,omitempty"`
}

type LambdaCodeSigningConfig struct {
	CodeSigningConfigId  string                     `json:"CodeSigningConfigId"`
	CodeSigningConfigArn string                     `json:"CodeSigningConfigArn"`
	Description          string                     `json:"Description,omitempty"`
	AllowedPublishers    *lambdaAllowedPublishers   `json:"AllowedPublishers,omitempty"`
	CodeSigningPolicies  *lambdaCodeSigningPolicies `json:"CodeSigningPolicies,omitempty"`
	LastModified         string                     `json:"LastModified"`
}

var lambdaCSCStore sim.Store[LambdaCodeSigningConfig]

// lambdaCSCID generates a csc-<17 lowercase alphanumeric> identifier matching
// the CodeSigningConfigArn pattern in the smithy model.
func lambdaCSCID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 17)
	raw := make([]byte, 17)
	_, _ = rand.Read(raw)
	for i := range b {
		b[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return "csc-" + string(b)
}

func lambdaCSCArn(id string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:code-signing-config:%s", awsRegion(), awsAccountID(), id)
}

func handleLambdaCreateCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description         string                     `json:"Description"`
		AllowedPublishers   *lambdaAllowedPublishers   `json:"AllowedPublishers"`
		CodeSigningPolicies *lambdaCodeSigningPolicies `json:"CodeSigningPolicies"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AllowedPublishers == nil || len(req.AllowedPublishers.SigningProfileVersionArns) == 0 {
		AWSError(w, "InvalidParameterValueException",
			"AllowedPublishers must specify at least one SigningProfileVersionArn", http.StatusBadRequest)
		return
	}
	policies := req.CodeSigningPolicies
	if policies == nil {
		// Real Lambda defaults UntrustedArtifactOnDeployment to Warn.
		policies = &lambdaCodeSigningPolicies{UntrustedArtifactOnDeployment: "Warn"}
	}
	id := lambdaCSCID()
	csc := LambdaCodeSigningConfig{
		CodeSigningConfigId:  id,
		CodeSigningConfigArn: lambdaCSCArn(id),
		Description:          req.Description,
		AllowedPublishers:    req.AllowedPublishers,
		CodeSigningPolicies:  policies,
		LastModified:         time.Now().UTC().Format(time.RFC3339),
	}
	lambdaCSCStore.Put(csc.CodeSigningConfigArn, csc)
	sim.WriteJSON(w, http.StatusCreated, map[string]any{"CodeSigningConfig": csc})
}

func handleLambdaGetCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	csc, ok := lambdaCSCStore.Get(arn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The code signing configuration cannot be found.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CodeSigningConfig": csc})
}

func handleLambdaUpdateCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	var req struct {
		Description         *string                    `json:"Description"`
		AllowedPublishers   *lambdaAllowedPublishers   `json:"AllowedPublishers"`
		CodeSigningPolicies *lambdaCodeSigningPolicies `json:"CodeSigningPolicies"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	found := lambdaCSCStore.Update(arn, func(csc *LambdaCodeSigningConfig) {
		if req.Description != nil {
			csc.Description = *req.Description
		}
		if req.AllowedPublishers != nil {
			csc.AllowedPublishers = req.AllowedPublishers
		}
		if req.CodeSigningPolicies != nil {
			csc.CodeSigningPolicies = req.CodeSigningPolicies
		}
		csc.LastModified = time.Now().UTC().Format(time.RFC3339)
	})
	if !found {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The code signing configuration cannot be found.")
		return
	}
	csc, _ := lambdaCSCStore.Get(arn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CodeSigningConfig": csc})
}

func handleLambdaDeleteCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	if !lambdaCSCStore.Delete(arn) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The code signing configuration cannot be found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleLambdaListCodeSigningConfigs(w http.ResponseWriter, r *http.Request) {
	stored := lambdaCSCStore.List()
	sortBy(stored, func(c LambdaCodeSigningConfig) string { return c.CodeSigningConfigArn })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"CodeSigningConfigs": stored})
}

type lambdaFunctionCodeSigningConfig struct {
	FunctionName         string
	CodeSigningConfigARN string
}

// function name -> code-signing configuration attachment.
var lambdaFnCSC sim.Store[lambdaFunctionCodeSigningConfig]

func handleLambdaPutFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	var req struct {
		CodeSigningConfigArn string `json:"CodeSigningConfigArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.CodeSigningConfigArn == "" {
		AWSError(w, "InvalidParameterValueException", "CodeSigningConfigArn is required", http.StatusBadRequest)
		return
	}
	if _, ok := lambdaCSCStore.Get(req.CodeSigningConfigArn); !ok {
		AWSErrorf(w, "CodeSigningConfigNotFoundException", http.StatusNotFound,
			"The code signing configuration %s cannot be found.", req.CodeSigningConfigArn)
		return
	}
	lambdaFnCSC.Put(name, lambdaFunctionCodeSigningConfig{
		FunctionName: name, CodeSigningConfigARN: req.CodeSigningConfigArn,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"CodeSigningConfigArn": req.CodeSigningConfigArn,
		"FunctionName":         name,
	})
}

func handleLambdaGetFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	attachment, _ := lambdaFnCSC.Get(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"CodeSigningConfigArn": attachment.CodeSigningConfigARN,
		"FunctionName":         name,
	})
}

func handleLambdaDeleteFunctionCodeSigningConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	lambdaFnCSC.Delete(name)
	w.WriteHeader(http.StatusNoContent)
}

type lambdaRuntimeMgmt struct {
	FunctionName      string
	Qualifier         string
	UpdateRuntimeOn   string
	RuntimeVersionArn string
}

// keyed by "<functionName>:<qualifier>".
var lambdaRTMs sim.Store[lambdaRuntimeMgmt]

func handleLambdaGetRuntimeManagementConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	rtm, ok := lambdaRTMs.Get(name + ":" + qualifier)
	out := map[string]any{
		"FunctionArn": lambdaEICArn(name, qualifier),
	}
	if ok {
		out["UpdateRuntimeOn"] = rtm.UpdateRuntimeOn
		if rtm.RuntimeVersionArn != "" {
			out["RuntimeVersionArn"] = rtm.RuntimeVersionArn
		}
	} else {
		// Default for a function with no explicit config is Auto.
		out["UpdateRuntimeOn"] = "Auto"
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleLambdaPutRuntimeManagementConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	var req struct {
		UpdateRuntimeOn   string `json:"UpdateRuntimeOn"`
		RuntimeVersionArn string `json:"RuntimeVersionArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.UpdateRuntimeOn == "" {
		AWSError(w, "InvalidParameterValueException", "UpdateRuntimeOn is required", http.StatusBadRequest)
		return
	}
	// When UpdateRuntimeOn is Manual, RuntimeVersionArn is required.
	if req.UpdateRuntimeOn == "Manual" && req.RuntimeVersionArn == "" {
		AWSError(w, "InvalidParameterValueException",
			"RuntimeVersionArn is required when UpdateRuntimeOn is Manual", http.StatusBadRequest)
		return
	}
	lambdaRTMs.Put(name+":"+qualifier, lambdaRuntimeMgmt{
		FunctionName:      name,
		Qualifier:         qualifier,
		UpdateRuntimeOn:   req.UpdateRuntimeOn,
		RuntimeVersionArn: req.RuntimeVersionArn,
	})
	out := map[string]any{
		"UpdateRuntimeOn": req.UpdateRuntimeOn,
		"FunctionArn":     lambdaEICArn(name, qualifier),
	}
	if req.RuntimeVersionArn != "" {
		out["RuntimeVersionArn"] = req.RuntimeVersionArn
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleLambdaGetAccountSettings(w http.ResponseWriter, _ *http.Request) {
	// Report usage from the function store and AWS's published default limits.
	functions := lambdaFunctions.List()
	var totalCodeSize int64
	for _, fn := range functions {
		totalCodeSize += fn.CodeSize
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"AccountLimit": map[string]any{
			"TotalCodeSize":                  int64(80530636800),
			"CodeSizeUnzipped":               int64(lambdaUnzippedCodeLimit),
			"CodeSizeZipped":                 int64(52428800),
			"ConcurrentExecutions":           lambdaAccountConcurrency,
			"UnreservedConcurrentExecutions": lambdaUnreservedConcurrency(),
		},
		"AccountUsage": map[string]any{
			"TotalCodeSize": totalCodeSize,
			"FunctionCount": int64(len(functions)),
		},
	})
}

// function name -> RecursiveLoop (Allow|Terminate).
var lambdaRecursion sim.Store[string]

func handleLambdaGetFunctionRecursionConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	loop, ok := lambdaRecursion.Get(name)
	if !ok {
		// New functions default to Terminate.
		loop = "Terminate"
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"RecursiveLoop": loop})
}

func handleLambdaPutFunctionRecursionConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The function %s could not be found.", lambdaArn(name))
		return
	}
	var req struct {
		RecursiveLoop string `json:"RecursiveLoop"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RecursiveLoop != "Allow" && req.RecursiveLoop != "Terminate" {
		AWSError(w, "InvalidParameterValueException",
			"RecursiveLoop must be one of Allow or Terminate", http.StatusBadRequest)
		return
	}
	lambdaRecursion.Put(name, req.RecursiveLoop)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"RecursiveLoop": req.RecursiveLoop})
}

type lambdaLayerPermission struct {
	StatementId  string
	Action       string
	Principal    string
	Organization string
	Statement    string // serialized JSON policy statement
}

type lambdaLayerPolicy struct {
	Statements []lambdaLayerPermission
	RevisionID string
}

// keyed by "<layerName>:<version>".
var lambdaLayerPolicies sim.Store[lambdaLayerPolicy]

func lambdaLayerPolicyKey(layer string, version int64) string {
	return fmt.Sprintf("%s:%d", layer, version)
}

// lambdaLayerPolicyDocument renders the IAM-style policy document the
// GetLayerVersionPolicy response returns under Policy.
func lambdaLayerPolicyDocument(layer string, version int64, stmts []lambdaLayerPermission) string {
	resource := lambdaLayerVersionArn(layer, version)
	var sb []string
	for _, s := range stmts {
		cond := ""
		if s.Organization != "" {
			cond = fmt.Sprintf(`,"Condition":{"StringEquals":{"aws:PrincipalOrgID":%q}}`, s.Organization)
		}
		sb = append(sb, fmt.Sprintf(
			`{"Sid":%q,"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":%q,"Resource":%q%s}`,
			s.StatementId, s.Principal, s.Action, resource, cond))
	}
	joined := ""
	for i, s := range sb {
		if i > 0 {
			joined += ","
		}
		joined += s
	}
	return fmt.Sprintf(`{"Version":"2012-10-17","Id":"default","Statement":[%s]}`, joined)
}

func lambdaLayerStatementJSON(layer string, version int64, p lambdaLayerPermission) string {
	resource := lambdaLayerVersionArn(layer, version)
	cond := ""
	if p.Organization != "" {
		cond = fmt.Sprintf(`,"Condition":{"StringEquals":{"aws:PrincipalOrgID":%q}}`, p.Organization)
	}
	return fmt.Sprintf(
		`{"Sid":%q,"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":%q,"Resource":%q%s}`,
		p.StatementId, p.Principal, p.Action, resource, cond)
}

func lambdaNewRevisionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func handleLambdaAddLayerVersionPermission(w http.ResponseWriter, r *http.Request) {
	layer := sim.PathParam(r, "layer")
	version, ok := lambdaParseLayerVersion(w, r)
	if !ok {
		return
	}
	if _, found := lambdaFindLayerVersion(layer, version); !found {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"The layer version %s:%d could not be found.", layer, version)
		return
	}
	var req struct {
		StatementId    string `json:"StatementId"`
		Action         string `json:"Action"`
		Principal      string `json:"Principal"`
		OrganizationId string `json:"OrganizationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StatementId == "" || req.Action == "" || req.Principal == "" {
		AWSError(w, "InvalidParameterValueException",
			"StatementId, Action, and Principal are required", http.StatusBadRequest)
		return
	}
	key := lambdaLayerPolicyKey(layer, version)
	perm := lambdaLayerPermission{
		StatementId:  req.StatementId,
		Action:       req.Action,
		Principal:    req.Principal,
		Organization: req.OrganizationId,
	}
	perm.Statement = lambdaLayerStatementJSON(layer, version, perm)
	conflict := false
	rev := ""
	lambdaLayerPolicies.Upsert(key, func(policy *lambdaLayerPolicy) {
		for _, statement := range policy.Statements {
			if statement.StatementId == req.StatementId {
				conflict = true
				return
			}
		}
		policy.Statements = append(policy.Statements, perm)
		policy.RevisionID = lambdaNewRevisionID()
		rev = policy.RevisionID
	})
	if conflict {
		AWSErrorf(w, "ResourceConflictException", http.StatusConflict,
			"The statement id (%s) provided already exists.", req.StatementId)
		return
	}
	sim.WriteJSON(w, http.StatusCreated, map[string]any{
		"Statement":  perm.Statement,
		"RevisionId": rev,
	})
}

func handleLambdaGetLayerVersionPolicy(w http.ResponseWriter, r *http.Request) {
	layer := sim.PathParam(r, "layer")
	version, ok := lambdaParseLayerVersion(w, r)
	if !ok {
		return
	}
	key := lambdaLayerPolicyKey(layer, version)
	policy, ok := lambdaLayerPolicies.Get(key)
	if !ok || len(policy.Statements) == 0 {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"No policy is associated with the given resource.")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Policy":     lambdaLayerPolicyDocument(layer, version, policy.Statements),
		"RevisionId": policy.RevisionID,
	})
}

func handleLambdaRemoveLayerVersionPermission(w http.ResponseWriter, r *http.Request) {
	layer := sim.PathParam(r, "layer")
	version, ok := lambdaParseLayerVersion(w, r)
	if !ok {
		return
	}
	statementID := sim.PathParam(r, "statement")
	key := lambdaLayerPolicyKey(layer, version)
	policy, _ := lambdaLayerPolicies.Get(key)
	idx := -1
	for i, s := range policy.Statements {
		if s.StatementId == statementID {
			idx = i
			break
		}
	}
	if idx < 0 {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"No statement (%s) is associated with the given resource.", statementID)
		return
	}
	policy.Statements = append(policy.Statements[:idx], policy.Statements[idx+1:]...)
	policy.RevisionID = lambdaNewRevisionID()
	lambdaLayerPolicies.Put(key, policy)
	w.WriteHeader(http.StatusNoContent)
}

func lambdaParseLayerVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := sim.PathParam(r, "version")
	var v int64
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		AWSError(w, "InvalidParameterValueException",
			fmt.Sprintf("Invalid version number: %q", raw), http.StatusBadRequest)
		return 0, false
	}
	return v, true
}
