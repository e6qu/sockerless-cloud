package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// LambdaVersion is an immutable snapshot of a function created by
// PublishVersion. Real Lambda increments the version monotonically
// from "1"; each version carries the same metadata as the function
// but with a frozen Code reference and an updated FunctionArn:
// `arn:aws:lambda:<region>:<account>:function:<name>:<version>`.
type LambdaVersion struct {
	FunctionName           string                        `json:"FunctionName"`
	FunctionArn            string                        `json:"FunctionArn"`
	Version                string                        `json:"Version"`
	Runtime                string                        `json:"Runtime,omitempty"`
	Role                   string                        `json:"Role"`
	Handler                string                        `json:"Handler,omitempty"`
	Code                   *LambdaFunctionCode           `json:"Code,omitempty"`
	CodeSha256             string                        `json:"CodeSha256,omitempty"`
	CodeSize               int64                         `json:"CodeSize"`
	MemorySize             int                           `json:"MemorySize"`
	Timeout                int                           `json:"Timeout"`
	State                  string                        `json:"State"`
	LastUpdateStatus       string                        `json:"LastUpdateStatus,omitempty"`
	LastModified           string                        `json:"LastModified"`
	RevisionId             string                        `json:"RevisionId"`
	PackageType            string                        `json:"PackageType,omitempty"`
	Description            string                        `json:"Description,omitempty"`
	Layers                 []lambdaLayerConfiguration    `json:"Layers,omitempty"`
	Architectures          []string                      `json:"Architectures,omitempty"`
	Environment            *LambdaEnvironment            `json:"Environment,omitempty"`
	ImageConfigResponse    *LambdaImageConfigResponse    `json:"ImageConfigResponse,omitempty"`
	VpcConfig              *lambdaVpcConfigConfiguration `json:"VpcConfig,omitempty"`
	CapacityProviderConfig map[string]any                `json:"CapacityProviderConfig,omitempty"`
	DeadLetterConfig       map[string]any                `json:"DeadLetterConfig,omitempty"`
	DurableConfig          map[string]any                `json:"DurableConfig,omitempty"`
	EphemeralStorage       map[string]any                `json:"EphemeralStorage,omitempty"`
	FileSystemConfigs      []map[string]any              `json:"FileSystemConfigs,omitempty"`
	KMSKeyArn              string                        `json:"KMSKeyArn,omitempty"`
	LoggingConfig          map[string]any                `json:"LoggingConfig,omitempty"`
	SnapStart              map[string]any                `json:"SnapStart,omitempty"`
	TracingConfig          map[string]any                `json:"TracingConfig,omitempty"`
}

// LambdaAlias maps a name (e.g. "live") to a function version.
type LambdaAlias struct {
	AliasArn        string                    `json:"AliasArn"`
	Name            string                    `json:"Name"`
	FunctionVersion string                    `json:"FunctionVersion"`
	Description     string                    `json:"Description,omitempty"`
	RevisionId      string                    `json:"RevisionId"`
	RoutingConfig   *LambdaAliasRoutingConfig `json:"RoutingConfig,omitempty"`
}

type LambdaAliasRoutingConfig struct {
	AdditionalVersionWeights map[string]float64 `json:"AdditionalVersionWeights,omitempty"`
}

// LambdaPolicyStatement is one entry in the function's resource-policy
// document. AddPermission appends, RemovePermission removes by Sid.
type LambdaPolicyStatement struct {
	Sid       string         `json:"Sid"`
	Effect    string         `json:"Effect"`
	Principal map[string]any `json:"Principal"`
	Action    string         `json:"Action"`
	Resource  string         `json:"Resource"`
	Condition map[string]any `json:"Condition,omitempty"`
}

// LambdaFunctionUrlConfig is the per-function URL config. Real Lambda
// returns a canonical `FunctionUrl` like
// `https://<id>.lambda-url.<region>.on.aws/`; the SDK + Terraform
// provider read it as an opaque advertised URL. The sim emits the
// same canonical shape — external by design (sockerless does not
// host the `*.lambda-url.<region>.on.aws` subdomain).
type LambdaFunctionUrlConfig struct {
	FunctionArn      string `json:"FunctionArn"`
	FunctionUrl      string `json:"FunctionUrl"` // external: real-AWS canonical `<id>.lambda-url.<region>.on.aws`
	AuthType         string `json:"AuthType"`
	CreationTime     string `json:"CreationTime"`
	LastModifiedTime string `json:"LastModifiedTime"`
	InvokeMode       string `json:"InvokeMode,omitempty"`
	Cors             any    `json:"Cors,omitempty"`
}

// lambdaVersionExists reports whether the function has a published
// version with the given ID. `$LATEST` is the implicit always-live
// version every function carries; explicit IDs must match one
// PublishVersion call.
func lambdaVersionExists(fn, version string) bool {
	if version == "" || version == "$LATEST" {
		return true
	}
	versions, _ := lambdaVersions.Get(fn)
	for _, v := range versions {
		if v.Version == version {
			return true
		}
	}
	return false
}

var (
	lambdaVersions   sim.Store[[]LambdaVersion]
	lambdaAliases    sim.Store[map[string]LambdaAlias]
	lambdaPolicies   sim.Store[[]LambdaPolicyStatement]
	lambdaURLConfigs sim.Store[LambdaFunctionUrlConfig]
)

func latestLambdaVersion(fn LambdaFunction) LambdaVersion {
	arn := fn.FunctionArn
	if !strings.HasSuffix(arn, ":$LATEST") {
		arn += ":$LATEST"
	}
	version := lambdaVersionFromFunction(fn)
	version.FunctionArn = arn
	version.Version = "$LATEST"
	return version
}

func lambdaVersionFromFunction(fn LambdaFunction) LambdaVersion {
	version := LambdaVersion{
		FunctionName:           fn.FunctionName,
		Runtime:                fn.Runtime,
		Role:                   fn.Role,
		Handler:                fn.Handler,
		Code:                   fn.Code,
		CodeSha256:             fn.CodeSha256,
		CodeSize:               fn.CodeSize,
		MemorySize:             fn.MemorySize,
		Timeout:                fn.Timeout,
		State:                  fn.State,
		LastUpdateStatus:       fn.LastUpdateStatus,
		LastModified:           fn.LastModified,
		RevisionId:             fn.RevisionId,
		PackageType:            fn.PackageType,
		Description:            fn.Description,
		Layers:                 lambdaLayerConfigurations(fn.Layers),
		Architectures:          fn.Architectures,
		Environment:            fn.Environment,
		VpcConfig:              lambdaVpcConfiguration(fn.VpcConfig),
		CapacityProviderConfig: fn.CapacityProviderConfig,
		DeadLetterConfig:       fn.DeadLetterConfig,
		DurableConfig:          fn.DurableConfig,
		EphemeralStorage:       fn.EphemeralStorage,
		FileSystemConfigs:      fn.FileSystemConfigs,
		KMSKeyArn:              fn.KMSKeyArn,
		LoggingConfig:          fn.LoggingConfig,
		SnapStart:              lambdaSnapStartResponse(fn.SnapStart),
		TracingConfig:          fn.TracingConfig,
	}
	if fn.ImageConfig != nil {
		version.ImageConfigResponse = &LambdaImageConfigResponse{ImageConfig: fn.ImageConfig}
	}
	return version
}

func lambdaFunctionFromVersion(version LambdaVersion) LambdaFunction {
	function := LambdaFunction{
		FunctionName:           version.FunctionName,
		FunctionArn:            version.FunctionArn,
		Runtime:                version.Runtime,
		Role:                   version.Role,
		Handler:                version.Handler,
		Code:                   version.Code,
		CodeSha256:             version.CodeSha256,
		CodeSize:               version.CodeSize,
		Description:            version.Description,
		MemorySize:             version.MemorySize,
		Timeout:                version.Timeout,
		Environment:            version.Environment,
		State:                  version.State,
		LastUpdateStatus:       version.LastUpdateStatus,
		LastModified:           version.LastModified,
		RevisionId:             version.RevisionId,
		Version:                version.Version,
		PackageType:            version.PackageType,
		Architectures:          version.Architectures,
		VpcConfig:              nil,
		CapacityProviderConfig: version.CapacityProviderConfig,
		DeadLetterConfig:       version.DeadLetterConfig,
		DurableConfig:          version.DurableConfig,
		EphemeralStorage:       version.EphemeralStorage,
		FileSystemConfigs:      version.FileSystemConfigs,
		KMSKeyArn:              version.KMSKeyArn,
		LoggingConfig:          version.LoggingConfig,
		SnapStart:              version.SnapStart,
		TracingConfig:          version.TracingConfig,
	}
	if version.ImageConfigResponse != nil {
		function.ImageConfig = version.ImageConfigResponse.ImageConfig
	}
	if version.VpcConfig != nil {
		function.VpcConfig = &LambdaVpcConfig{
			SubnetIds:               version.VpcConfig.SubnetIds,
			SecurityGroupIds:        version.VpcConfig.SecurityGroupIds,
			VpcId:                   version.VpcConfig.VpcId,
			Ipv6AllowedForDualStack: version.VpcConfig.Ipv6AllowedForDualStack,
		}
	}
	function.Layers = make([]string, 0, len(version.Layers))
	for _, layer := range version.Layers {
		function.Layers = append(function.Layers, layer.Arn)
	}
	return function
}

func publishLambdaVersion(name, description string, fn LambdaFunction) LambdaVersion {
	var v LambdaVersion
	lambdaVersions.Upsert(name, func(versions *[]LambdaVersion) {
		version := strconv.Itoa(len(*versions) + 1)
		v = lambdaVersionFromFunction(fn)
		v.FunctionArn = fn.FunctionArn + ":" + version
		v.Version = version
		v.State = "Active"
		v.LastUpdateStatus = "Successful"
		v.LastModified = time.Now().UTC().Format(time.RFC3339)
		v.RevisionId = generateUUID()
		v.Description = description
		*versions = append(*versions, v)
	})
	return v
}

func handleLambdaPublishVersion(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	var req struct {
		Description string `json:"Description"`
		RevisionId  string `json:"RevisionId"`
	}
	if r.ContentLength > 0 {
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AWSError(w, "InvalidParameterValueException",
				"Invalid request body", http.StatusBadRequest)
			return
		}
	}
	if req.RevisionId != "" && req.RevisionId != fn.RevisionId {
		sim.AWSError(w, "PreconditionFailedException",
			"The RevisionId provided does not match the latest RevisionId for the function",
			http.StatusPreconditionFailed)
		return
	}

	v := publishLambdaVersion(name, req.Description, fn)
	sim.WriteJSON(w, http.StatusCreated, v)
}

func handleLambdaListVersions(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	published, _ := lambdaVersions.Get(name)
	versions := make([]LambdaVersion, 0, len(published)+1)
	if fn, ok := lambdaFunctions.Get(name); ok {
		versions = append(versions, latestLambdaVersion(fn))
	}
	versions = append(versions, published...)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Versions": versions})
}

func handleLambdaCreateAlias(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	var req struct {
		Name            string                    `json:"Name"`
		FunctionVersion string                    `json:"FunctionVersion"`
		Description     string                    `json:"Description"`
		RoutingConfig   *LambdaAliasRoutingConfig `json:"RoutingConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValueException",
			"Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		sim.AWSError(w, "InvalidParameterValueException",
			"Name is required", http.StatusBadRequest)
		return
	}
	if !lambdaVersionExists(name, req.FunctionVersion) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function version not found: %s on %s", req.FunctionVersion, name)
		return
	}
	alias := LambdaAlias{
		AliasArn:        fn.FunctionArn + ":" + req.Name,
		Name:            req.Name,
		FunctionVersion: req.FunctionVersion,
		Description:     req.Description,
		RevisionId:      generateUUID(),
		RoutingConfig:   req.RoutingConfig,
	}
	if alias.RoutingConfig != nil && len(alias.RoutingConfig.AdditionalVersionWeights) == 0 {
		alias.RoutingConfig = nil
	}
	conflict := false
	lambdaAliases.Upsert(name, func(aliases *map[string]LambdaAlias) {
		if *aliases == nil {
			*aliases = map[string]LambdaAlias{}
		}
		if _, exists := (*aliases)[req.Name]; exists {
			conflict = true
			return
		}
		(*aliases)[req.Name] = alias
	})
	if conflict {
		sim.AWSErrorf(w, "ResourceConflictException", http.StatusConflict,
			"Alias already exists: %s", req.Name)
		return
	}
	sim.WriteJSON(w, http.StatusCreated, alias)
}

func handleLambdaListAliases(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	aliases, _ := lambdaAliases.Get(name)
	out := make([]LambdaAlias, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, a)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Aliases": out})
}

func handleLambdaGetAlias(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	aliasName := sim.PathParam(r, "alias")
	if as, ok := lambdaAliases.Get(name); ok {
		if a, ok := as[aliasName]; ok {
			sim.WriteJSON(w, http.StatusOK, a)
			return
		}
	}
	sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
		"Alias %s not found on function %s", aliasName, name)
}

func handleLambdaUpdateAlias(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	aliasName := sim.PathParam(r, "alias")
	var req struct {
		FunctionVersion string                    `json:"FunctionVersion"`
		Description     string                    `json:"Description"`
		RoutingConfig   *LambdaAliasRoutingConfig `json:"RoutingConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValueException",
			"Invalid request body", http.StatusBadRequest)
		return
	}
	as, ok := lambdaAliases.Get(name)
	if !ok || as[aliasName].Name == "" {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Alias %s not found on function %s", aliasName, name)
		return
	}
	a := as[aliasName]
	if req.FunctionVersion != "" {
		if !lambdaVersionExists(name, req.FunctionVersion) {
			sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function version not found: %s on %s", req.FunctionVersion, name)
			return
		}
		a.FunctionVersion = req.FunctionVersion
	}
	if req.Description != "" {
		a.Description = req.Description
	}
	if req.RoutingConfig != nil {
		if len(req.RoutingConfig.AdditionalVersionWeights) == 0 {
			a.RoutingConfig = nil
		} else {
			a.RoutingConfig = req.RoutingConfig
		}
	}
	a.RevisionId = generateUUID()
	as[aliasName] = a
	lambdaAliases.Put(name, as)
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleLambdaDeleteAlias(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	aliasName := sim.PathParam(r, "alias")
	as, ok := lambdaAliases.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound, "Function not found: %s", name)
		return
	}
	if _, ok := as[aliasName]; !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound, "Alias not found: %s", aliasName)
		return
	}
	delete(as, aliasName)
	lambdaAliases.Put(name, as)
	w.WriteHeader(http.StatusNoContent)
}

func handleLambdaAddPermission(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	var req struct {
		StatementId   string `json:"StatementId"`
		Action        string `json:"Action"`
		Principal     string `json:"Principal"`
		SourceArn     string `json:"SourceArn"`
		SourceAccount string `json:"SourceAccount"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValueException",
			"Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StatementId == "" || req.Action == "" || req.Principal == "" {
		sim.AWSError(w, "InvalidParameterValueException",
			"StatementId, Action, Principal are required",
			http.StatusBadRequest)
		return
	}
	stmt := LambdaPolicyStatement{
		Sid:       req.StatementId,
		Effect:    "Allow",
		Principal: map[string]any{"Service": req.Principal},
		Action:    req.Action,
		Resource:  fn.FunctionArn,
	}
	if req.SourceArn != "" || req.SourceAccount != "" {
		cond := map[string]any{}
		if req.SourceArn != "" {
			cond["ArnLike"] = map[string]any{"AWS:SourceArn": req.SourceArn}
		}
		if req.SourceAccount != "" {
			cond["StringEquals"] = map[string]any{"AWS:SourceAccount": req.SourceAccount}
		}
		stmt.Condition = cond
	}
	conflict := false
	var stmts []LambdaPolicyStatement
	lambdaPolicies.Upsert(name, func(policies *[]LambdaPolicyStatement) {
		for _, existing := range *policies {
			if existing.Sid == req.StatementId {
				conflict = true
				return
			}
		}
		*policies = append(*policies, stmt)
		stmts = append([]LambdaPolicyStatement(nil), (*policies)...)
	})
	if conflict {
		sim.AWSErrorf(w, "ResourceConflictException", http.StatusConflict,
			"Statement %s already exists", req.StatementId)
		return
	}
	lambdaMirrorResourcePolicy(fn.FunctionArn, stmts)
	stmtJSON, err := json.Marshal(stmt)
	if err != nil {
		sim.AWSError(w, "InternalServerError",
			"failed to serialise policy statement: "+err.Error(),
			http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusCreated, map[string]any{
		"Statement": string(stmtJSON),
	})
}

func handleLambdaGetPolicy(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	stmts, _ := lambdaPolicies.Get(name)
	if len(stmts) == 0 {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"No policy on function: %s", name)
		return
	}
	policyDoc := map[string]any{
		"Version":   "2012-10-17",
		"Id":        "default",
		"Statement": stmts,
	}
	docJSON, err := json.Marshal(policyDoc)
	if err != nil {
		sim.AWSError(w, "InternalServerError",
			"failed to serialise policy document: "+err.Error(),
			http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Policy":     string(docJSON),
		"RevisionId": generateUUID(),
	})
}

func handleLambdaRemovePermission(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	sid := sim.PathParam(r, "statement")
	stmts, _ := lambdaPolicies.Get(name)
	out := make([]LambdaPolicyStatement, 0, len(stmts))
	found := false
	for _, s := range stmts {
		if s.Sid == sid {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Statement %s not found on function %s", sid, name)
		return
	}
	lambdaPolicies.Put(name, out)
	remaining := append([]LambdaPolicyStatement(nil), out...)
	if fn, ok := lambdaFunctions.Get(name); ok {
		lambdaMirrorResourcePolicy(fn.FunctionArn, remaining)
	}
	w.WriteHeader(http.StatusNoContent)
}

// lambdaMirrorResourcePolicy mirrors the assembled function policy into the
// central resource-policy store keyed by the function ARN, so the IAM
// enforcement gate can resolve it. Clears the entry when no statements remain.
func lambdaMirrorResourcePolicy(functionArn string, stmts []LambdaPolicyStatement) {
	if len(stmts) == 0 {
		iamDeleteResourcePolicy(functionArn)
		return
	}
	doc := map[string]any{
		"Version":   "2012-10-17",
		"Id":        "default",
		"Statement": stmts,
	}
	docJSON, err := json.Marshal(doc)
	if err != nil {
		return
	}
	iamPutResourcePolicy(functionArn, string(docJSON))
}

func handleLambdaCreateFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", name)
		return
	}
	var req struct {
		AuthType   string `json:"AuthType"`
		InvokeMode string `json:"InvokeMode"`
		Cors       any    `json:"Cors"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValueException",
			"Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AuthType == "" {
		req.AuthType = "NONE"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	urlID := strings.ToLower(generateUUID()[:24])
	urlConfig := LambdaFunctionUrlConfig{
		FunctionArn:      fn.FunctionArn,
		FunctionUrl:      fmt.Sprintf("https://%s.lambda-url.%s.on.aws/", urlID, awsRegion()),
		AuthType:         req.AuthType,
		CreationTime:     now,
		LastModifiedTime: now,
		InvokeMode:       req.InvokeMode,
		Cors:             req.Cors,
	}
	if _, exists := lambdaURLConfigs.Get(name); exists {
		sim.AWSErrorf(w, "ResourceConflictException", http.StatusConflict,
			"FunctionUrlConfig already exists for %s", name)
		return
	}
	lambdaURLConfigs.Put(name, urlConfig)
	sim.WriteJSON(w, http.StatusCreated, urlConfig)
}

func handleLambdaGetFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	cfg, ok := lambdaURLConfigs.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"FunctionUrlConfig not found for %s", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleLambdaUpdateFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	var req struct {
		AuthType   string `json:"AuthType"`
		InvokeMode string `json:"InvokeMode"`
		Cors       any    `json:"Cors"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValueException",
			"Invalid request body", http.StatusBadRequest)
		return
	}
	cfg, ok := lambdaURLConfigs.Get(name)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"FunctionUrlConfig not found for %s", name)
		return
	}
	if req.AuthType != "" {
		cfg.AuthType = req.AuthType
	}
	if req.InvokeMode != "" {
		cfg.InvokeMode = req.InvokeMode
	}
	if req.Cors != nil {
		cfg.Cors = req.Cors
	}
	cfg.LastModifiedTime = time.Now().UTC().Format(time.RFC3339)
	lambdaURLConfigs.Put(name, cfg)
	sim.WriteJSON(w, http.StatusOK, cfg)
}

func handleLambdaDeleteFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaURLConfigs.Get(name); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"FunctionUrlConfig not found for %s", name)
		return
	}
	lambdaURLConfigs.Delete(name)
	w.WriteHeader(http.StatusNoContent)
}
