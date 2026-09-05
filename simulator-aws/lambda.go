package main

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// lambdaProcessHandles tracks running Lambda containers for cancellation.
var lambdaProcessHandles sync.Map // map[requestID]*sim.ContainerHandle

// Lambda types

type LambdaFunction struct {
	FunctionName           string              `json:"FunctionName"`
	FunctionArn            string              `json:"FunctionArn"`
	Runtime                string              `json:"Runtime,omitempty"`
	Role                   string              `json:"Role"`
	Handler                string              `json:"Handler,omitempty"`
	Code                   *LambdaFunctionCode `json:"Code,omitempty"`
	CodeSha256             string              `json:"CodeSha256,omitempty"`
	CodeSize               int64               `json:"CodeSize"`
	Description            string              `json:"Description,omitempty"`
	MemorySize             int                 `json:"MemorySize"`
	Timeout                int                 `json:"Timeout"`
	Environment            *LambdaEnvironment  `json:"Environment,omitempty"`
	Tags                   map[string]string   `json:"Tags,omitempty"`
	State                  string              `json:"State"`
	LastUpdateStatus       string              `json:"LastUpdateStatus,omitempty"`
	LastModified           string              `json:"LastModified"`
	RevisionId             string              `json:"RevisionId"`
	Version                string              `json:"Version"`
	PackageType            string              `json:"PackageType,omitempty"`
	Architectures          []string            `json:"Architectures,omitempty"`
	ImageConfig            *LambdaImageConfig  `json:"ImageConfig,omitempty"`
	VpcConfig              *LambdaVpcConfig    `json:"VpcConfig,omitempty"`
	Layers                 []string            `json:"Layers,omitempty"`
	CapacityProviderConfig map[string]any      `json:"CapacityProviderConfig,omitempty"`
	DeadLetterConfig       map[string]any      `json:"DeadLetterConfig,omitempty"`
	DurableConfig          map[string]any      `json:"DurableConfig,omitempty"`
	EphemeralStorage       map[string]any      `json:"EphemeralStorage,omitempty"`
	FileSystemConfigs      []map[string]any    `json:"FileSystemConfigs,omitempty"`
	KMSKeyArn              string              `json:"KMSKeyArn,omitempty"`
	LoggingConfig          map[string]any      `json:"LoggingConfig,omitempty"`
	SnapStart              map[string]any      `json:"SnapStart,omitempty"`
	TracingConfig          map[string]any      `json:"TracingConfig,omitempty"`
}

// LambdaVpcConfig matches the real Lambda CreateFunction shape. When
// SubnetIds is set, AWS allocates a Hyperplane ENI per subnet for the
// function's outbound traffic; the sim allocates an IP from each
// subnet's CidrBlock so DescribeFunction returns an accurate Ipv4 list.
type LambdaVpcConfig struct {
	SubnetIds               []string `json:"SubnetIds,omitempty"`
	SecurityGroupIds        []string `json:"SecurityGroupIds,omitempty"`
	VpcId                   string   `json:"VpcId,omitempty"`
	Ipv6AllowedForDualStack bool     `json:"Ipv6AllowedForDualStack,omitempty"`
	// SubnetIPv4Allocations: one entry per SubnetId, in matching order.
	// Real Lambda's DescribeFunction doesn't expose ENI IPs directly,
	// but Hyperplane creates them in the listed subnets — backends that
	// need to verify ENI provisioning consume this slice. Empty when no
	// VpcConfig is set on CreateFunction.
	SubnetIPv4Allocations []string `json:"SubnetIPv4Allocations,omitempty"`
}

type lambdaVpcConfigConfiguration struct {
	SubnetIds               []string `json:"SubnetIds,omitempty"`
	SecurityGroupIds        []string `json:"SecurityGroupIds,omitempty"`
	VpcId                   string   `json:"VpcId,omitempty"`
	Ipv6AllowedForDualStack bool     `json:"Ipv6AllowedForDualStack,omitempty"`
}

func lambdaVpcConfiguration(vpc *LambdaVpcConfig) *lambdaVpcConfigConfiguration {
	if vpc == nil {
		return nil
	}
	return &lambdaVpcConfigConfiguration{
		SubnetIds:               vpc.SubnetIds,
		SecurityGroupIds:        vpc.SecurityGroupIds,
		VpcId:                   vpc.VpcId,
		Ipv6AllowedForDualStack: vpc.Ipv6AllowedForDualStack,
	}
}

func prepareLambdaVpcConfig(vpc *LambdaVpcConfig) ([]string, string, error) {
	if vpc == nil || len(vpc.SubnetIds) == 0 {
		return nil, "", nil
	}
	var vpcID string
	for _, subnetID := range vpc.SubnetIds {
		subnet, ok := ec2Subnets.Get(subnetID)
		if !ok {
			return nil, "", fmt.Errorf("subnet %q not found", subnetID)
		}
		if vpcID == "" {
			vpcID = subnet.VpcId
			continue
		}
		if subnet.VpcId != vpcID {
			return nil, "", fmt.Errorf("subnets must belong to the same VPC")
		}
	}
	for _, securityGroupID := range vpc.SecurityGroupIds {
		group, ok := ec2SecurityGroups.Get(securityGroupID)
		if !ok {
			return nil, "", fmt.Errorf("security group %q not found", securityGroupID)
		}
		if group.VpcId != vpcID {
			return nil, "", fmt.Errorf("security group %q and subnet do not belong to the same VPC", securityGroupID)
		}
	}
	allocations := make([]string, 0, len(vpc.SubnetIds))
	for _, subnetID := range vpc.SubnetIds {
		ip, err := AllocateSubnetIP(subnetID)
		if err != nil {
			return nil, "", err
		}
		allocations = append(allocations, ip)
	}
	return allocations, vpcID, nil
}

type LambdaFunctionCode struct {
	S3Bucket        string `json:"S3Bucket,omitempty"`
	S3Key           string `json:"S3Key,omitempty"`
	S3ObjectVersion string `json:"S3ObjectVersion,omitempty"`
	ImageUri        string `json:"ImageUri,omitempty"` // external (operator-supplied): OCI image reference, any registry
	ZipFile         string `json:"ZipFile,omitempty"`
	SourceKMSKeyArn string `json:"SourceKMSKeyArn,omitempty"`
}

type LambdaEnvironment struct {
	Variables map[string]string `json:"Variables,omitempty"`
}

type LambdaImageConfig struct {
	EntryPoint       []string `json:"EntryPoint,omitempty"`
	Command          []string `json:"Command,omitempty"`
	WorkingDirectory string   `json:"WorkingDirectory,omitempty"`
}

// LambdaImageConfigResponse wraps the image config in FunctionConfiguration
// responses. AWS accepts ImageConfig as CreateFunction input but returns it
// under ImageConfigResponse on GetFunction/CreateFunction, which is the field
// the SDK FunctionConfiguration shape reads.
type LambdaImageConfigResponse struct {
	ImageConfig *LambdaImageConfig `json:"ImageConfig,omitempty"`
}

type lambdaFunctionConfiguration struct {
	FunctionName           string                        `json:"FunctionName"`
	FunctionArn            string                        `json:"FunctionArn"`
	Runtime                string                        `json:"Runtime,omitempty"`
	Role                   string                        `json:"Role"`
	Handler                string                        `json:"Handler,omitempty"`
	CodeSha256             string                        `json:"CodeSha256,omitempty"`
	CodeSize               int64                         `json:"CodeSize"`
	Description            string                        `json:"Description,omitempty"`
	MemorySize             int                           `json:"MemorySize"`
	Timeout                int                           `json:"Timeout"`
	Environment            *LambdaEnvironment            `json:"Environment,omitempty"`
	State                  string                        `json:"State"`
	LastUpdateStatus       string                        `json:"LastUpdateStatus,omitempty"`
	LastModified           string                        `json:"LastModified"`
	RevisionId             string                        `json:"RevisionId"`
	Version                string                        `json:"Version"`
	PackageType            string                        `json:"PackageType,omitempty"`
	Architectures          []string                      `json:"Architectures,omitempty"`
	ImageConfigResponse    *LambdaImageConfigResponse    `json:"ImageConfigResponse,omitempty"`
	VpcConfig              *lambdaVpcConfigConfiguration `json:"VpcConfig,omitempty"`
	Layers                 []lambdaLayerConfiguration    `json:"Layers,omitempty"`
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

type lambdaLayerConfiguration struct {
	Arn      string `json:"Arn"`
	CodeSize int64  `json:"CodeSize"`
}

func lambdaLayerConfigurations(layerARNs []string) []lambdaLayerConfiguration {
	var configurations []lambdaLayerConfiguration
	for _, arn := range layerARNs {
		if layer, ok := lambdaLayerVersionByARN(arn); ok {
			configurations = append(configurations, lambdaLayerConfiguration{Arn: arn, CodeSize: layer.CodeSize})
		}
	}
	return configurations
}

func lambdaConfiguration(fn LambdaFunction) lambdaFunctionConfiguration {
	cfg := lambdaFunctionConfiguration{
		FunctionName:           fn.FunctionName,
		FunctionArn:            fn.FunctionArn,
		Runtime:                fn.Runtime,
		Role:                   fn.Role,
		Handler:                fn.Handler,
		CodeSha256:             fn.CodeSha256,
		CodeSize:               fn.CodeSize,
		Description:            fn.Description,
		MemorySize:             fn.MemorySize,
		Timeout:                fn.Timeout,
		Environment:            fn.Environment,
		State:                  fn.State,
		LastUpdateStatus:       fn.LastUpdateStatus,
		LastModified:           fn.LastModified,
		RevisionId:             fn.RevisionId,
		Version:                fn.Version,
		PackageType:            fn.PackageType,
		Architectures:          fn.Architectures,
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
	cfg.Layers = lambdaLayerConfigurations(fn.Layers)
	if fn.ImageConfig != nil {
		cfg.ImageConfigResponse = &LambdaImageConfigResponse{ImageConfig: fn.ImageConfig}
	}
	return cfg
}

func lambdaSnapStartResponse(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	response := make(map[string]any, len(input)+1)
	for key, value := range input {
		response[key] = value
	}
	if _, ok := response["OptimizationStatus"]; !ok {
		response["OptimizationStatus"] = "Off"
	}
	return response
}

func lambdaNumber(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		return int(number), number == float64(int(number))
	case int:
		return number, true
	case int32:
		return int(number), true
	case int64:
		return int(number), true
	default:
		return 0, false
	}
}

// State store
var (
	lambdaFunctions   sim.Store[LambdaFunction]
	lambdaConcurrency sim.Store[int]
)

func lambdaArn(name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", awsRegion(), awsAccountID(), name)
}

func lambdaResolveInvocationTarget(identifier, queryQualifier string) (LambdaFunction, string, bool) {
	name := identifier
	qualifier := queryQualifier
	if marker := strings.Index(name, ":function:"); marker >= 0 {
		name = name[marker+len(":function:"):]
	}
	if separator := strings.IndexByte(name, ':'); separator >= 0 {
		if qualifier == "" {
			qualifier = name[separator+1:]
		}
		name = name[:separator]
	}
	function, ok := lambdaFunctions.Get(name)
	if !ok {
		return LambdaFunction{}, "", false
	}
	if qualifier == "" || qualifier == "$LATEST" {
		function.Version = "$LATEST"
		return function, "$LATEST", true
	}
	aliases, _ := lambdaAliases.Get(name)
	alias, isAlias := aliases[qualifier]
	if isAlias {
		selectedVersion := alias.FunctionVersion
		if alias.RoutingConfig != nil && len(alias.RoutingConfig.AdditionalVersionWeights) > 0 {
			point, err := cryptorand.Int(cryptorand.Reader, big.NewInt(10_000))
			if err != nil {
				return LambdaFunction{}, "", false
			}
			cursor := 0
			for candidate, weight := range alias.RoutingConfig.AdditionalVersionWeights {
				cursor += int(weight * 10_000)
				if int(point.Int64()) < cursor {
					selectedVersion = candidate
					break
				}
			}
		}
		qualifier = selectedVersion
	}
	versions, _ := lambdaVersions.Get(name)
	for _, version := range versions {
		if version.Version == qualifier {
			return lambdaFunctionFromVersion(version), version.Version, true
		}
	}
	return LambdaFunction{}, "", false
}

func lambdaInvocationHasQualifier(identifier, queryQualifier string) bool {
	if queryQualifier != "" {
		return true
	}
	if marker := strings.Index(identifier, ":function:"); marker >= 0 {
		identifier = identifier[marker+len(":function:"):]
	}
	return strings.Contains(identifier, ":")
}

func registerLambda(srv *sim.Server, startBackgroundPollers bool) {
	// Invoke is a CloudTrail DATA event (excluded from LookupEvents); the
	// function-management ops are management events.
	cloudTrailDeclareDataEvents("lambda.amazonaws.com", "Invoke")
	lambdaFunctions = sim.MakeStore[LambdaFunction](srv.DB(), "lambda_functions")
	lambdaConcurrency = sim.MakeStore[int](srv.DB(), "lambda_concurrency")
	lambdaAsyncInvocations = sim.MakeStore[LambdaAsyncInvocation](srv.DB(), "lambda_async_invocations")
	lambdaVersions = sim.MakeStore[[]LambdaVersion](srv.DB(), "lambda_versions")
	lambdaAliases = sim.MakeStore[map[string]LambdaAlias](srv.DB(), "lambda_aliases")
	lambdaPolicies = sim.MakeStore[[]LambdaPolicyStatement](srv.DB(), "lambda_policies")
	lambdaURLConfigs = sim.MakeStore[LambdaFunctionUrlConfig](srv.DB(), "lambda_url_configs")
	lambdaESMs = sim.MakeStore[LambdaEventSourceMapping](srv.DB(), "lambda_event_source_mappings")
	lambdaLayers = sim.MakeStore[[]LambdaLayerVersion](srv.DB(), "lambda_layers")
	lambdaESMLogger = srv.Logger()
	if startBackgroundPollers {
		startLambdaEventSourcePollers(srv)
	}

	mux := srv

	lambdaResource := cloudTrailRESTResource("AWS::Lambda::Function", "name", "arn")
	mux.HandleFunc("POST /2015-03-31/functions", cloudTrailRecordedREST("CreateFunction", "lambda.amazonaws.com", nil, lambdaEnforced("CreateFunction", nil, handleLambdaCreateFunction)))
	mux.HandleFunc("GET /2015-03-31/functions/{name}", cloudTrailRecordedREST("GetFunction", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunction", lambdaFunctionResourceARN, handleLambdaGetFunction)))
	mux.HandleFunc("DELETE /2015-03-31/functions/{name}", cloudTrailRecordedREST("DeleteFunction", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteFunction", lambdaFunctionResourceARN, handleLambdaDeleteFunction)))
	mux.HandleFunc("PUT /2015-03-31/functions/{name}/configuration", cloudTrailRecordedREST("UpdateFunctionConfiguration", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("UpdateFunctionConfiguration", lambdaFunctionResourceARN, handleLambdaUpdateFunctionConfiguration)))
	mux.HandleFunc("POST /2015-03-31/functions/{name}/invocations", cloudTrailRecordedREST("Invoke", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("Invoke", lambdaFunctionResourceARN, handleLambdaInvoke)))
	mux.HandleFunc("GET /2015-03-31/functions", cloudTrailRecordedREST("ListFunctions", "lambda.amazonaws.com", nil, lambdaEnforced("ListFunctions", nil, handleLambdaListFunctions)))
	mux.HandleFunc("GET /2015-03-31/functions/", cloudTrailRecordedREST("ListFunctions", "lambda.amazonaws.com", nil, lambdaEnforced("ListFunctions", nil, handleLambdaListFunctions)))
	mux.HandleFunc("GET /2017-03-31/tags/{arn...}", cloudTrailRecordedREST("ListTags", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("ListTags", lambdaFunctionResourceARN, handleLambdaListTags)))
	mux.HandleFunc("POST /2017-03-31/tags/{arn...}", cloudTrailRecordedREST("TagResource", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("TagResource", lambdaFunctionResourceARN, handleLambdaTagResource)))
	mux.HandleFunc("DELETE /2017-03-31/tags/{arn...}", cloudTrailRecordedREST("UntagResource", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("UntagResource", lambdaFunctionResourceARN, handleLambdaUntagResource)))

	// Versions + aliases + permissions + function URL config.
	mux.HandleFunc("POST /2015-03-31/functions/{name}/versions", cloudTrailRecordedREST("PublishVersion", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PublishVersion", lambdaFunctionResourceARN, handleLambdaPublishVersion)))
	mux.HandleFunc("GET /2015-03-31/functions/{name}/versions", cloudTrailRecordedREST("ListVersionsByFunction", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("ListVersionsByFunction", lambdaFunctionResourceARN, handleLambdaListVersions)))
	mux.HandleFunc("POST /2015-03-31/functions/{name}/aliases", cloudTrailRecordedREST("CreateAlias", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("CreateAlias", lambdaFunctionResourceARN, handleLambdaCreateAlias)))
	mux.HandleFunc("GET /2015-03-31/functions/{name}/aliases", cloudTrailRecordedREST("ListAliases", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("ListAliases", lambdaFunctionResourceARN, handleLambdaListAliases)))
	mux.HandleFunc("GET /2015-03-31/functions/{name}/aliases/{alias}", cloudTrailRecordedREST("GetAlias", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetAlias", lambdaFunctionResourceARN, handleLambdaGetAlias)))
	mux.HandleFunc("PUT /2015-03-31/functions/{name}/aliases/{alias}", cloudTrailRecordedREST("UpdateAlias", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("UpdateAlias", lambdaFunctionResourceARN, handleLambdaUpdateAlias)))
	mux.HandleFunc("DELETE /2015-03-31/functions/{name}/aliases/{alias}", cloudTrailRecordedREST("DeleteAlias", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteAlias", lambdaFunctionResourceARN, handleLambdaDeleteAlias)))
	mux.HandleFunc("POST /2015-03-31/functions/{name}/policy", cloudTrailRecordedREST("AddPermission", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("AddPermission", lambdaFunctionResourceARN, handleLambdaAddPermission)))
	mux.HandleFunc("GET /2015-03-31/functions/{name}/policy", cloudTrailRecordedREST("GetPolicy", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetPolicy", lambdaFunctionResourceARN, handleLambdaGetPolicy)))
	mux.HandleFunc("DELETE /2015-03-31/functions/{name}/policy/{statement}", cloudTrailRecordedREST("RemovePermission", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("RemovePermission", lambdaFunctionResourceARN, handleLambdaRemovePermission)))
	mux.HandleFunc("POST /2021-10-31/functions/{name}/url", cloudTrailRecordedREST("CreateFunctionUrlConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("CreateFunctionUrlConfig", lambdaFunctionResourceARN, handleLambdaCreateFunctionUrlConfig)))
	mux.HandleFunc("GET /2021-10-31/functions/{name}/url", cloudTrailRecordedREST("GetFunctionUrlConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionUrlConfig", lambdaFunctionResourceARN, handleLambdaGetFunctionUrlConfig)))
	mux.HandleFunc("PUT /2021-10-31/functions/{name}/url", cloudTrailRecordedREST("UpdateFunctionUrlConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("UpdateFunctionUrlConfig", lambdaFunctionResourceARN, handleLambdaUpdateFunctionUrlConfig)))
	mux.HandleFunc("DELETE /2021-10-31/functions/{name}/url", cloudTrailRecordedREST("DeleteFunctionUrlConfig", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteFunctionUrlConfig", lambdaFunctionResourceARN, handleLambdaDeleteFunctionUrlConfig)))
	mux.HandleFunc("GET /2021-10-31/functions/{name}/urls", cloudTrailRecordedREST("ListFunctionUrlConfigs", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("ListFunctionUrlConfigs", lambdaFunctionResourceARN, handleLambdaListFunctionUrlConfigs)))

	// Event source mappings.
	mux.HandleFunc("POST /2015-03-31/event-source-mappings", cloudTrailRecordedREST("CreateEventSourceMapping", "lambda.amazonaws.com", nil, lambdaEnforced("CreateEventSourceMapping", nil, handleLambdaCreateEventSourceMapping)))
	mux.HandleFunc("GET /2015-03-31/event-source-mappings", cloudTrailRecordedREST("ListEventSourceMappings", "lambda.amazonaws.com", nil, lambdaEnforced("ListEventSourceMappings", nil, handleLambdaListEventSourceMappings)))
	mux.HandleFunc("GET /2015-03-31/event-source-mappings/{uuid}", cloudTrailRecordedREST("GetEventSourceMapping", "lambda.amazonaws.com", nil, lambdaEnforced("GetEventSourceMapping", nil, handleLambdaGetEventSourceMapping)))
	mux.HandleFunc("PUT /2015-03-31/event-source-mappings/{uuid}", cloudTrailRecordedREST("UpdateEventSourceMapping", "lambda.amazonaws.com", nil, lambdaEnforced("UpdateEventSourceMapping", nil, handleLambdaUpdateEventSourceMapping)))
	mux.HandleFunc("DELETE /2015-03-31/event-source-mappings/{uuid}", cloudTrailRecordedREST("DeleteEventSourceMapping", "lambda.amazonaws.com", nil, lambdaEnforced("DeleteEventSourceMapping", nil, handleLambdaDeleteEventSourceMapping)))

	// Layers + layer versions. ListLayers and GetLayerVersionByArn share
	// GET /2018-10-31/layers; the latter is keyed on ?find=LayerVersion&Arn=…
	// and dispatched inside handleLambdaListLayers.
	mux.HandleFunc("POST /2018-10-31/layers/{layer}/versions", cloudTrailRecordedREST("PublishLayerVersion", "lambda.amazonaws.com", nil, lambdaEnforced("PublishLayerVersion", nil, handleLambdaPublishLayerVersion)))
	mux.HandleFunc("GET /2018-10-31/layers/{layer}/versions", cloudTrailRecordedREST("ListLayerVersions", "lambda.amazonaws.com", nil, lambdaEnforced("ListLayerVersions", nil, handleLambdaListLayerVersions)))
	mux.HandleFunc("GET /2018-10-31/layers/{layer}/versions/{version}", cloudTrailRecordedREST("GetLayerVersion", "lambda.amazonaws.com", nil, lambdaEnforced("GetLayerVersion", nil, handleLambdaGetLayerVersion)))
	mux.HandleFunc("DELETE /2018-10-31/layers/{layer}/versions/{version}", cloudTrailRecordedREST("DeleteLayerVersion", "lambda.amazonaws.com", nil, lambdaEnforced("DeleteLayerVersion", nil, handleLambdaDeleteLayerVersion)))
	// GetLayerVersionByArn shares this path (?find=LayerVersion&Arn=…); the
	// event name is composed per-request so both ops land in the CloudTrail
	// log and the conformance REST registry.
	restRegisterOp("lambda.amazonaws.com", "GetLayerVersionByArn")
	mux.HandleFunc("GET /2018-10-31/layers", cloudTrailRecordedRESTDynamic(lambdaLayersEventName, "lambda.amazonaws.com", nil, lambdaEnforcedDynamic(lambdaLayersOpName, nil, handleLambdaListLayers)))

	// Reserved concurrency.
	mux.HandleFunc("PUT /2017-10-31/functions/{name}/concurrency", cloudTrailRecordedREST("PutFunctionConcurrency", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("PutFunctionConcurrency", lambdaFunctionResourceARN, handleLambdaPutFunctionConcurrency)))
	mux.HandleFunc("GET /2019-09-30/functions/{name}/concurrency", cloudTrailRecordedREST("GetFunctionConcurrency", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("GetFunctionConcurrency", lambdaFunctionResourceARN, handleLambdaGetFunctionConcurrency)))
	mux.HandleFunc("DELETE /2017-10-31/functions/{name}/concurrency", cloudTrailRecordedREST("DeleteFunctionConcurrency", "lambda.amazonaws.com", lambdaResource, lambdaEnforced("DeleteFunctionConcurrency", lambdaFunctionResourceARN, handleLambdaDeleteFunctionConcurrency)))

	// Event-invoke config, provisioned concurrency, code-signing configs,
	// runtime management, account settings, recursion config, and
	// layer-version permissions.
	lambdaCSCStore = sim.MakeStore[LambdaCodeSigningConfig](srv.DB(), "lambda_code_signing_configs")
	registerLambdaExtras2(srv)
	registerLambdaExtras3(srv)
}

func handleLambdaCreateFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FunctionName           string              `json:"FunctionName"`
		Runtime                string              `json:"Runtime"`
		Role                   string              `json:"Role"`
		Handler                string              `json:"Handler"`
		Code                   *LambdaFunctionCode `json:"Code"`
		Description            string              `json:"Description"`
		MemorySize             int                 `json:"MemorySize"`
		Timeout                int                 `json:"Timeout"`
		Environment            *LambdaEnvironment  `json:"Environment"`
		Tags                   map[string]string   `json:"Tags"`
		PackageType            string              `json:"PackageType"`
		Publish                bool                `json:"Publish"`
		Architectures          []string            `json:"Architectures"`
		ImageConfig            *LambdaImageConfig  `json:"ImageConfig"`
		VpcConfig              *LambdaVpcConfig    `json:"VpcConfig"`
		Layers                 []string            `json:"Layers"`
		CapacityProviderConfig map[string]any      `json:"CapacityProviderConfig"`
		DeadLetterConfig       map[string]any      `json:"DeadLetterConfig"`
		DurableConfig          map[string]any      `json:"DurableConfig"`
		EphemeralStorage       map[string]any      `json:"EphemeralStorage"`
		FileSystemConfigs      []map[string]any    `json:"FileSystemConfigs"`
		KMSKeyArn              string              `json:"KMSKeyArn"`
		LoggingConfig          map[string]any      `json:"LoggingConfig"`
		SnapStart              map[string]any      `json:"SnapStart"`
		TracingConfig          map[string]any      `json:"TracingConfig"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.FunctionName == "" {
		AWSError(w, "InvalidParameterValueException", "FunctionName is required", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		AWSError(w, "InvalidParameterValueException", "Role is required", http.StatusBadRequest)
		return
	}

	if _, exists := lambdaFunctions.Get(req.FunctionName); exists {
		AWSErrorf(w, "ResourceConflictException", http.StatusConflict,
			"Function already exist: %s", req.FunctionName)
		return
	}

	if req.MemorySize == 0 {
		req.MemorySize = 128
	}
	if req.Timeout == 0 {
		req.Timeout = 3
	}
	// MemorySize is 128–10240 MB; Timeout is 1–900 s (server-side ranges the
	// SDK doesn't check).
	if req.MemorySize < 128 || req.MemorySize > 10240 {
		AWSError(w, "InvalidParameterValueException",
			"'memorySize' failed to satisfy constraint: Member must have value less than or equal to 10240 and greater than or equal to 128",
			http.StatusBadRequest)
		return
	}
	if req.Timeout < 1 || req.Timeout > 900 {
		AWSError(w, "InvalidParameterValueException",
			"'timeout' failed to satisfy constraint: Member must have value less than or equal to 900 and greater than or equal to 1",
			http.StatusBadRequest)
		return
	}
	if req.PackageType == "" {
		req.PackageType = "Zip"
	}
	if len(req.Architectures) == 0 {
		req.Architectures = []string{"x86_64"}
	}
	if req.Code == nil {
		AWSError(w, "InvalidParameterValueException", "Code is required", http.StatusBadRequest)
		return
	}
	if req.EphemeralStorage == nil {
		req.EphemeralStorage = map[string]any{"Size": float64(512)}
	}
	if size, ok := lambdaNumber(req.EphemeralStorage["Size"]); !ok || size < 512 || size > 10240 {
		AWSError(w, "InvalidParameterValueException",
			"EphemeralStorage.Size must be between 512 and 10240", http.StatusBadRequest)
		return
	}
	if req.LoggingConfig == nil {
		req.LoggingConfig = map[string]any{
			"LogFormat":      "Text",
			"LogGroup":       "/aws/lambda/" + req.FunctionName,
			"SystemLogLevel": "INFO",
		}
	}
	if req.TracingConfig == nil {
		req.TracingConfig = map[string]any{"Mode": "PassThrough"}
	}
	if req.SnapStart == nil {
		req.SnapStart = map[string]any{"ApplyOn": "None"}
	}
	if req.DurableConfig != nil && req.PackageType != "Image" &&
		!strings.HasPrefix(req.Runtime, "nodejs") &&
		!strings.HasPrefix(req.Runtime, "python") &&
		!strings.HasPrefix(req.Runtime, "java") {
		AWSError(w, "InvalidParameterValueException",
			"Durable functions require a supported Node.js, Python, Java, or container-image runtime", http.StatusBadRequest)
		return
	}
	if req.PackageType == "Image" {
		if req.Code.ImageUri == "" {
			AWSError(w, "InvalidParameterValueException",
				"ImageUri is required when PackageType is Image", http.StatusBadRequest)
			return
		}
	} else {
		if req.Runtime == "" || req.Handler == "" {
			AWSError(w, "InvalidParameterValueException",
				"Runtime and Handler are required when PackageType is Zip", http.StatusBadRequest)
			return
		}
		if err := validateLambdaDeploymentPackage(req.Code); err != nil {
			AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
			return
		}
	}
	codeSize, err := lambdaDeploymentPackageSize(req.Code)
	if err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}
	for _, layerARN := range req.Layers {
		if _, ok := lambdaLayerVersionByARN(layerARN); !ok {
			AWSErrorf(w, "InvalidParameterValueException", http.StatusBadRequest,
				"Layer version %s does not exist", layerARN)
			return
		}
	}

	// Real Lambda allocates a Hyperplane ENI per VpcConfig.SubnetId, with
	// an IP drawn from the subnet's CIDR. Validate the subnet exists and
	// allocate one IP per subnet up front so DescribeFunction reflects
	// the real attached-IP list.
	vpcConfig := req.VpcConfig
	if vpcConfig != nil {
		ips, vpcID, vpcErr := prepareLambdaVpcConfig(vpcConfig)
		if vpcErr != nil {
			AWSError(w, "InvalidParameterValueException", vpcErr.Error(), http.StatusBadRequest)
			return
		}
		vpcConfig.SubnetIPv4Allocations = ips
		vpcConfig.VpcId = vpcID
	}

	fn := LambdaFunction{
		FunctionName:           req.FunctionName,
		FunctionArn:            lambdaArn(req.FunctionName),
		Runtime:                req.Runtime,
		Role:                   req.Role,
		Handler:                req.Handler,
		Code:                   req.Code,
		CodeSha256:             lambdaCodeSha256(req.Code),
		CodeSize:               codeSize,
		Description:            req.Description,
		MemorySize:             req.MemorySize,
		Timeout:                req.Timeout,
		Environment:            req.Environment,
		Tags:                   req.Tags,
		State:                  "Active",
		LastUpdateStatus:       "Successful",
		LastModified:           time.Now().UTC().Format(time.RFC3339),
		RevisionId:             generateUUID(),
		Version:                "$LATEST",
		PackageType:            req.PackageType,
		Architectures:          req.Architectures,
		ImageConfig:            req.ImageConfig,
		VpcConfig:              vpcConfig,
		Layers:                 req.Layers,
		CapacityProviderConfig: req.CapacityProviderConfig,
		DeadLetterConfig:       req.DeadLetterConfig,
		DurableConfig:          req.DurableConfig,
		EphemeralStorage:       req.EphemeralStorage,
		FileSystemConfigs:      req.FileSystemConfigs,
		KMSKeyArn:              req.KMSKeyArn,
		LoggingConfig:          req.LoggingConfig,
		SnapStart:              req.SnapStart,
		TracingConfig:          req.TracingConfig,
	}
	lambdaFunctions.Put(req.FunctionName, fn)
	if req.Publish {
		publishLambdaVersion(req.FunctionName, "", fn)
	}

	sim.WriteJSON(w, http.StatusCreated, lambdaConfiguration(fn))
}

func lambdaCodeSha256(code *LambdaFunctionCode) string {
	if code == nil || code.ImageUri != "" {
		return ""
	}
	material, err := lambdaDeploymentPackageBytes(code)
	if err != nil || len(material) == 0 {
		return ""
	}
	sum := sha256.Sum256(material)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// lambdaDeploymentPackageBytes resolves the deployment archive from the same
// sources AWS Lambda accepts. Inline ZipFile is base64 on the REST-JSON wire;
// S3 coordinates address the real Amazon S3 slice in this cloud process.
// These typed errors preserve AWS's user-facing capitalization when handlers
// copy the validation message into an InvalidParameterValueException.
type lambdaDeploymentPackageError string

func (e lambdaDeploymentPackageError) Error() string {
	return string(e)
}

func lambdaDeploymentPackageBytes(code *LambdaFunctionCode) ([]byte, error) {
	if code == nil {
		return nil, lambdaDeploymentPackageError("Code is required")
	}
	if code.ZipFile != "" {
		b, err := base64.StdEncoding.DecodeString(code.ZipFile)
		if err != nil {
			return nil, fmt.Errorf("ZipFile must be valid base64: %w", err)
		}
		return b, nil
	}
	if code.S3Bucket != "" || code.S3Key != "" {
		if code.S3Bucket == "" || code.S3Key == "" {
			return nil, fmt.Errorf("S3Bucket and S3Key must be supplied together")
		}
		obj, ok := s3Objects.Get(s3ObjectKey(code.S3Bucket, code.S3Key))
		if !ok {
			return nil, lambdaDeploymentPackageError(fmt.Sprintf(
				"Amazon S3 object s3://%s/%s does not exist", code.S3Bucket, code.S3Key,
			))
		}
		return append([]byte(nil), obj.Data...), nil
	}
	return nil, lambdaDeploymentPackageError("ZipFile or Amazon S3 deployment package coordinates are required")
}

func lambdaDeploymentPackageSize(code *LambdaFunctionCode) (int64, error) {
	if code == nil {
		return 0, lambdaDeploymentPackageError("Code is required")
	}
	if code.ImageUri != "" {
		return 0, nil
	}
	b, err := lambdaDeploymentPackageBytes(code)
	if err != nil {
		return 0, err
	}
	return int64(len(b)), nil
}

func validateLambdaDeploymentPackage(code *LambdaFunctionCode) error {
	b, err := lambdaDeploymentPackageBytes(code)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return lambdaDeploymentPackageError(fmt.Sprintf("Could not unzip uploaded file: %v", err))
	}
	if len(zr.File) == 0 {
		return lambdaDeploymentPackageError("Could not unzip uploaded file: archive is empty")
	}
	var uncompressed uint64
	for _, entry := range zr.File {
		uncompressed += entry.UncompressedSize64
		if uncompressed > 250*1024*1024 {
			return lambdaDeploymentPackageError("Unzipped size must be smaller than 262144000 bytes")
		}
	}
	return nil
}

func handleLambdaGetFunction(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if name == "" {
		AWSError(w, "InvalidParameterValueException", "Function name is required", http.StatusBadRequest)
		return
	}

	queryQualifier := r.URL.Query().Get("Qualifier")
	fn, _, ok := lambdaResolveInvocationTarget(name, queryQualifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}

	code := map[string]string{
		"RepositoryType": "S3",
	}
	if fn.Code != nil {
		if fn.Code.ImageUri != "" {
			code["ImageUri"] = fn.Code.ImageUri
			code["ResolvedImageUri"] = fn.Code.ImageUri
			code["RepositoryType"] = "ECR"
		} else {
			archive, err := lambdaDeploymentPackageBytes(fn.Code)
			if err != nil {
				AWSError(w, "ServiceException", err.Error(), http.StatusInternalServerError)
				return
			}
			key := "functions/" + name + "/" + fn.RevisionId + ".zip"
			lambdaPutArtifact(key, archive)
			code["Location"] = presignedS3URLBase(
				awsRequestURLBase(r), lambdaArtifactBucketName(), key, http.MethodGet,
			)
		}
		if fn.Code.SourceKMSKeyArn != "" {
			code["SourceKMSKeyArn"] = fn.Code.SourceKMSKeyArn
		}
	}
	tags := fn.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	out := map[string]any{
		"Configuration": lambdaConfiguration(fn),
		"Code":          code,
		"Tags":          tags,
	}
	if reserved, ok := lambdaConcurrency.Get(name); ok {
		out["Concurrency"] = map[string]any{
			"ReservedConcurrentExecutions": reserved,
		}
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func lambdaArtifactBucketName() string {
	return awsAccountID() + "-lambda-artifacts"
}

func lambdaPutArtifact(key string, data []byte) {
	bucket := lambdaArtifactBucketName()
	if _, ok := s3Buckets_.Get(bucket); !ok {
		s3Buckets_.Put(bucket, S3Bucket{
			Name:         bucket,
			CreationDate: time.Now().UTC().Format(time.RFC3339),
		})
	}
	digest := md5.Sum(data)
	s3Objects.Put(s3ObjectKey(bucket, key), S3Object{
		Key:          s3ObjectKey(bucket, key),
		Data:         append([]byte(nil), data...),
		ContentType:  "application/zip",
		ETag:         fmt.Sprintf("\"%x\"", digest),
		LastModified: time.Now().UTC(),
		Size:         int64(len(data)),
		Metadata:     map[string]string{"aws-service": "lambda"},
	})
}

func handleLambdaDeleteFunction(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if name == "" {
		AWSError(w, "InvalidParameterValueException", "Function name is required", http.StatusBadRequest)
		return
	}
	qualifier := r.URL.Query().Get("Qualifier")
	if qualifier != "" {
		if qualifier == "$LATEST" {
			AWSError(w, "InvalidParameterValueException",
				"$LATEST cannot be deleted independently", http.StatusBadRequest)
			return
		}
		if _, ok := lambdaFunctions.Get(name); !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function not found: %s", lambdaArn(name))
			return
		}
		aliases, _ := lambdaAliases.Get(name)
		for _, alias := range aliases {
			if alias.FunctionVersion == qualifier {
				AWSError(w, "ResourceConflictException",
					"Lambda version "+qualifier+" is referenced by an alias",
					http.StatusConflict)
				return
			}
			if alias.RoutingConfig != nil {
				if _, referenced := alias.RoutingConfig.AdditionalVersionWeights[qualifier]; referenced {
					AWSError(w, "ResourceConflictException",
						"Lambda version "+qualifier+" is referenced by an alias",
						http.StatusConflict)
					return
				}
			}
		}
		deleted := false
		lambdaVersions.Update(name, func(versions *[]LambdaVersion) {
			filtered := make([]LambdaVersion, 0, len(*versions))
			for _, version := range *versions {
				if version.Version == qualifier {
					deleted = true
					continue
				}
				filtered = append(filtered, version)
			}
			*versions = filtered
		})
		if !deleted {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function version not found: %s:%s", lambdaArn(name), qualifier)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if !lambdaFunctions.Delete(name) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	lambdaVersions.Delete(name)
	lambdaAliases.Delete(name)
	lambdaPolicies.Delete(name)
	lambdaURLConfigs.Delete(name)
	lambdaConcurrency.Delete(name)
	lambdaFnCSC.Delete(name)
	lambdaRecursion.Delete(name)
	functionARN := lambdaArn(name)
	for _, config := range lambdaEICs.List() {
		if config.FunctionArn == functionARN || strings.HasPrefix(config.FunctionArn, functionARN+":") {
			qualifier := strings.TrimPrefix(config.FunctionArn, functionARN)
			qualifier = strings.TrimPrefix(qualifier, ":")
			lambdaEICs.Delete(lambdaEICKey(name, qualifier))
		}
	}
	for _, config := range lambdaPCs.List() {
		if config.FunctionName == name {
			lambdaPCs.Delete(name + ":" + config.Qualifier)
		}
	}
	for _, config := range lambdaRTMs.List() {
		if config.FunctionName == name {
			lambdaRTMs.Delete(name + ":" + config.Qualifier)
		}
	}
	for _, config := range lambdaScalingCfgs.List() {
		if config.FunctionName == name {
			lambdaScalingCfgs.Delete(lambdaEICKey(name, config.Qualifier))
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func handleLambdaUpdateFunctionConfiguration(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if name == "" {
		AWSError(w, "InvalidParameterValueException", "Function name is required", http.StatusBadRequest)
		return
	}

	var req struct {
		Runtime                *string            `json:"Runtime"`
		Handler                *string            `json:"Handler"`
		Description            *string            `json:"Description"`
		MemorySize             *int               `json:"MemorySize"`
		Timeout                *int               `json:"Timeout"`
		Environment            *LambdaEnvironment `json:"Environment"`
		Role                   *string            `json:"Role"`
		VpcConfig              *LambdaVpcConfig   `json:"VpcConfig"`
		Layers                 *[]string          `json:"Layers"`
		CapacityProviderConfig map[string]any     `json:"CapacityProviderConfig"`
		DeadLetterConfig       map[string]any     `json:"DeadLetterConfig"`
		DurableConfig          map[string]any     `json:"DurableConfig"`
		EphemeralStorage       map[string]any     `json:"EphemeralStorage"`
		FileSystemConfigs      *[]map[string]any  `json:"FileSystemConfigs"`
		ImageConfig            *LambdaImageConfig `json:"ImageConfig"`
		KMSKeyArn              *string            `json:"KMSKeyArn"`
		LoggingConfig          map[string]any     `json:"LoggingConfig"`
		SnapStart              map[string]any     `json:"SnapStart"`
		TracingConfig          map[string]any     `json:"TracingConfig"`
		RevisionId             *string            `json:"RevisionId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	current, exists := lambdaFunctions.Get(name)
	if !exists {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	if req.RevisionId != nil && *req.RevisionId != current.RevisionId {
		AWSError(w, "PreconditionFailedException",
			"The Revision Id provided does not match the latest Revision Id.", http.StatusPreconditionFailed)
		return
	}
	if req.MemorySize != nil && (*req.MemorySize < 128 || *req.MemorySize > 10240) {
		AWSError(w, "InvalidParameterValueException",
			"MemorySize must be between 128 and 10240", http.StatusBadRequest)
		return
	}
	if req.Timeout != nil && (*req.Timeout < 1 || *req.Timeout > 900) {
		AWSError(w, "InvalidParameterValueException",
			"Timeout must be between 1 and 900", http.StatusBadRequest)
		return
	}

	// Real Lambda re-allocates Hyperplane ENIs when SubnetIds change.
	// Validate the new subnets exist before applying any update so a
	// half-applied configuration can't slip through.
	var newAllocations []string
	var newVpcID string
	if req.VpcConfig != nil {
		var vpcErr error
		newAllocations, newVpcID, vpcErr = prepareLambdaVpcConfig(req.VpcConfig)
		if vpcErr != nil {
			AWSError(w, "InvalidParameterValueException", vpcErr.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Layers != nil {
		for _, layerARN := range *req.Layers {
			if _, ok := lambdaLayerVersionByARN(layerARN); !ok {
				AWSErrorf(w, "InvalidParameterValueException", http.StatusBadRequest,
					"Layer version %s does not exist", layerARN)
				return
			}
		}
	}
	if req.EphemeralStorage != nil {
		if size, ok := lambdaNumber(req.EphemeralStorage["Size"]); !ok || size < 512 || size > 10240 {
			AWSError(w, "InvalidParameterValueException",
				"EphemeralStorage.Size must be between 512 and 10240", http.StatusBadRequest)
			return
		}
	}

	found := lambdaFunctions.Update(name, func(fn *LambdaFunction) {
		if req.Runtime != nil {
			fn.Runtime = *req.Runtime
		}
		if req.Handler != nil {
			fn.Handler = *req.Handler
		}
		if req.Description != nil {
			fn.Description = *req.Description
		}
		if req.MemorySize != nil {
			fn.MemorySize = *req.MemorySize
		}
		if req.Timeout != nil {
			fn.Timeout = *req.Timeout
		}
		if req.Environment != nil {
			fn.Environment = req.Environment
		}
		if req.Role != nil {
			fn.Role = *req.Role
		}
		if req.VpcConfig != nil {
			req.VpcConfig.SubnetIPv4Allocations = newAllocations
			req.VpcConfig.VpcId = newVpcID
			fn.VpcConfig = req.VpcConfig
		}
		if req.Layers != nil {
			fn.Layers = append([]string(nil), (*req.Layers)...)
		}
		if req.CapacityProviderConfig != nil {
			fn.CapacityProviderConfig = req.CapacityProviderConfig
		}
		if req.DeadLetterConfig != nil {
			fn.DeadLetterConfig = req.DeadLetterConfig
		}
		if req.DurableConfig != nil {
			fn.DurableConfig = req.DurableConfig
		}
		if req.EphemeralStorage != nil {
			fn.EphemeralStorage = req.EphemeralStorage
		}
		if req.FileSystemConfigs != nil {
			fn.FileSystemConfigs = append([]map[string]any(nil), (*req.FileSystemConfigs)...)
		}
		if req.ImageConfig != nil {
			fn.ImageConfig = req.ImageConfig
		}
		if req.KMSKeyArn != nil {
			fn.KMSKeyArn = *req.KMSKeyArn
		}
		if req.LoggingConfig != nil {
			fn.LoggingConfig = req.LoggingConfig
		}
		if req.SnapStart != nil {
			fn.SnapStart = req.SnapStart
		}
		if req.TracingConfig != nil {
			fn.TracingConfig = req.TracingConfig
		}
		fn.LastModified = time.Now().UTC().Format(time.RFC3339)
		fn.LastUpdateStatus = "Successful"
		fn.RevisionId = generateUUID()
	})

	if !found {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}

	fn, _ := lambdaFunctions.Get(name)
	sim.WriteJSON(w, http.StatusOK, lambdaConfiguration(fn))
}

// handleLambdaInvoke implements the AWS Lambda Invoke API. For Image
// package-type functions, it routes through the Runtime API slice
// (see lambda_runtime.go): the simulator stands up a
// per-invocation Runtime API listener, launches the container with
// AWS_LAMBDA_RUNTIME_API pointing at it, and returns whatever the
// handler posts back to /response (or /error → X-Amz-Function-Error:
// Unhandled). Matches real Lambda; no synthetic stdout capture.
func handleLambdaInvoke(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if name == "" {
		AWSError(w, "InvalidParameterValueException", "Function name is required", http.StatusBadRequest)
		return
	}

	queryQualifier := r.URL.Query().Get("Qualifier")
	fn, executedVersion, ok := lambdaResolveInvocationTarget(name, queryQualifier)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function or version not found: %s", name)
		return
	}

	// Determine invocation type
	invocationType := r.Header.Get("X-Amz-Invocation-Type")
	if invocationType == "" {
		invocationType = "RequestResponse"
	}

	w.Header().Set("X-Amz-Executed-Version", executedVersion)

	if strings.EqualFold(invocationType, "DryRun") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if ce := r.Header.Get("Content-Encoding"); ce != "" {
		AWSError(w, "InvalidRequest",
			fmt.Sprintf("Content-Encoding %q not supported on Lambda Invoke", ce),
			http.StatusUnsupportedMediaType)
		return
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		AWSError(w, "RequestBodyInvalid",
			"failed to read invocation payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	durableARN := ""
	if fn.DurableConfig != nil {
		if !lambdaInvocationHasQualifier(name, queryQualifier) {
			AWSError(w, "InvalidParameterValueException",
				"Durable functions must be invoked with a version, alias, or $LATEST qualifier",
				http.StatusBadRequest)
			return
		}
		var durableErr string
		durableARN, _, durableErr = lambdaBeginDurableExecution(
			fn,
			executedVersion,
			r.Header.Get("X-Amz-Durable-Execution-Name"),
			payload,
		)
		if durableErr != "" {
			AWSError(w, "DurableExecutionAlreadyStartedException", durableErr, http.StatusConflict)
			return
		}
		w.Header().Set("X-Amz-Durable-Execution-Arn", durableARN)
	}

	switch strings.ToLower(invocationType) {
	case "event":
		if durableARN != "" {
			lambdaStartDurableCoordinator(durableARN, fn)
		} else {
			// Async invocation runs the function for real in the background,
			// producing the same Runtime API callbacks and logs as a
			// synchronous invocation.
			lambdaInvokeAsynchronously(fn, payload, lambdaAsyncQualifier(name, queryQualifier))
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		// RequestResponse runs both image and ZIP deployment packages through
		// the same Lambda Runtime API contract their managed runtime speaks.
		//
		// aws-sdk-go-v2 gates request-compression via the
		// `requestcompression` trait; Lambda Invoke isn't in that
		// set today but has been flagged for future inclusion. If a
		// caller sends a `Content-Encoding`, fail loud rather than
		// forward the gzipped envelope into the runtime.
		var responseBody []byte
		var unhandled bool
		if durableARN != "" {
			lambdaStartDurableCoordinator(durableARN, fn)
			responseBody, unhandled = lambdaWaitForDurableExecution(r.Context(), durableARN)
		} else {
			responseBody, unhandled, _ = invokeLambdaViaRuntimeAPI(fn, payload)
		}
		if unhandled {
			w.Header().Set("X-Amz-Function-Error", "Unhandled")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}
}

func handleLambdaListFunctions(w http.ResponseWriter, r *http.Request) {
	stored := lambdaFunctions.List()
	sortBy(stored, func(f LambdaFunction) string { return f.FunctionName })

	marker := r.URL.Query().Get("Marker")
	maxItems := 50
	if raw := r.URL.Query().Get("MaxItems"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxItems = n
		}
	}
	allVersions := strings.EqualFold(r.URL.Query().Get("FunctionVersion"), "ALL")

	// Build the full result list. Default (no FunctionVersion) returns
	// $LATEST for each function; FunctionVersion=ALL additionally
	// includes every published version with a version-qualified ARN.
	var all []lambdaFunctionConfiguration
	for _, fn := range stored {
		all = append(all, lambdaConfiguration(fn))
		if allVersions {
			published, _ := lambdaVersions.Get(fn.FunctionName)
			for _, v := range published {
				all = append(all, lambdaConfigurationFromVersion(v))
			}
		}
	}

	page, next := awsPage(all, marker, maxItems, 50)

	out := map[string]any{"Functions": page}
	if next != "" {
		out["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func lambdaConfigurationFromVersion(v LambdaVersion) lambdaFunctionConfiguration {
	return lambdaFunctionConfiguration{
		FunctionName:           v.FunctionName,
		FunctionArn:            v.FunctionArn,
		Runtime:                v.Runtime,
		Role:                   v.Role,
		Handler:                v.Handler,
		CodeSha256:             v.CodeSha256,
		CodeSize:               v.CodeSize,
		Description:            v.Description,
		MemorySize:             v.MemorySize,
		Timeout:                v.Timeout,
		State:                  v.State,
		LastUpdateStatus:       v.LastUpdateStatus,
		LastModified:           v.LastModified,
		RevisionId:             v.RevisionId,
		Version:                v.Version,
		PackageType:            v.PackageType,
		Architectures:          v.Architectures,
		Environment:            v.Environment,
		ImageConfigResponse:    v.ImageConfigResponse,
		VpcConfig:              v.VpcConfig,
		Layers:                 v.Layers,
		CapacityProviderConfig: v.CapacityProviderConfig,
		DeadLetterConfig:       v.DeadLetterConfig,
		DurableConfig:          v.DurableConfig,
		EphemeralStorage:       v.EphemeralStorage,
		FileSystemConfigs:      v.FileSystemConfigs,
		KMSKeyArn:              v.KMSKeyArn,
		LoggingConfig:          v.LoggingConfig,
		SnapStart:              v.SnapStart,
		TracingConfig:          v.TracingConfig,
	}
}

func handleLambdaListTags(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	// Extract function name from ARN
	name := arn
	if strings.Contains(arn, ":function:") {
		parts := strings.SplitN(arn, ":function:", 2)
		if len(parts) == 2 {
			name = parts[1]
		}
	}

	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}

	tags := fn.Tags
	if tags == nil {
		tags = map[string]string{}
	}

	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Tags": tags,
	})
}

// lambdaLogSink implements sim.LogSink and writes log lines to CloudWatch
// for Lambda function invocations.
type lambdaLogSink struct {
	logGroup  string
	logStream string
}

func (s *lambdaLogSink) WriteLog(line sim.LogLine) {
	cwIngestWorkloadLogLine(s.logGroup, s.logStream, line.Text)
}

// handleLambdaUntagResource implements DELETE /2017-03-31/tags/{arn}?tagKeys=a&tagKeys=b.
// Real Lambda's UntagResource takes ARN + list of tag keys to remove;
// idempotent (deleting a non-existent key returns 204). The sim
// previously had only GET + POST for this path; sockerless's pool
// release path calls UntagResource and was getting a generic 405 from
// the default mux, surfaced as a JSON-decode error in the SDK.
//
// Pattern carry-forward: ARN parsing is lenient — the function-name
// portion is the key, and any account-ID embedded in the ARN is
// ignored. Matches handleLambdaTagResource's behaviour so a single
// sim can serve calls with different operator-supplied account IDs
// (the sim's lambdaArn helper hardcodes 123456789012).
func handleLambdaUntagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	name := arn
	if strings.Contains(arn, ":function:") {
		parts := strings.SplitN(arn, ":function:", 2)
		if len(parts) == 2 {
			name = parts[1]
		}
	}

	tagKeys := r.URL.Query()["tagKeys"]

	found := lambdaFunctions.Update(name, func(fn *LambdaFunction) {
		if fn.Tags == nil {
			return
		}
		for _, k := range tagKeys {
			delete(fn.Tags, k)
		}
	})

	if !found {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func handleLambdaTagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("arn")
	name := arn
	if strings.Contains(arn, ":function:") {
		parts := strings.SplitN(arn, ":function:", 2)
		if len(parts) == 2 {
			name = parts[1]
		}
	}

	var req struct {
		Tags map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}

	found := lambdaFunctions.Update(name, func(fn *LambdaFunction) {
		if fn.Tags == nil {
			fn.Tags = make(map[string]string)
		}
		for k, v := range req.Tags {
			fn.Tags[k] = v
		}
	})

	if !found {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
