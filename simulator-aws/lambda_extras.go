package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// LambdaEventSourceMapping stores the EventSourceMappingConfiguration members
// the simulator models and echoes the rest of the configuration from the
// request, so a read returns what was written.
type LambdaEventSourceMapping struct {
	UUID                           string         `json:"UUID"`
	EventSourceMappingArn          string         `json:"EventSourceMappingArn"`
	EventSourceArn                 string         `json:"EventSourceArn,omitempty"`
	FunctionArn                    string         `json:"FunctionArn"`
	State                          string         `json:"State"`
	StateTransitionReason          string         `json:"StateTransitionReason,omitempty"`
	LastModified                   float64        `json:"LastModified"`
	LastProcessingResult           string         `json:"LastProcessingResult,omitempty"`
	BatchSize                      *int           `json:"BatchSize,omitempty"`
	MaximumBatchingWindowInSeconds *int           `json:"MaximumBatchingWindowInSeconds,omitempty"`
	ParallelizationFactor          *int           `json:"ParallelizationFactor,omitempty"`
	StartingPosition               string         `json:"StartingPosition,omitempty"`
	Topics                         []string       `json:"Topics,omitempty"`
	Queues                         []string       `json:"Queues,omitempty"`
	FunctionResponseTypes          []string       `json:"FunctionResponseTypes,omitempty"`
	FilterCriteria                 map[string]any `json:"FilterCriteria,omitempty"`
	// The stream settings an Amazon Kinesis mapping reads its shards by.
	StartingPositionTimestamp  *float64                 `json:"StartingPositionTimestamp,omitempty"`
	MaximumRetryAttempts       *int                     `json:"MaximumRetryAttempts,omitempty"`
	MaximumRecordAgeInSeconds  *int                     `json:"MaximumRecordAgeInSeconds,omitempty"`
	BisectBatchOnFunctionError *bool                    `json:"BisectBatchOnFunctionError,omitempty"`
	DestinationConfig          *lambdaDestinationConfig `json:"DestinationConfig,omitempty"`
}

// lambdaESMStreamSettings are the stream settings CreateEventSourceMapping and
// UpdateEventSourceMapping take.
type lambdaESMStreamSettings struct {
	StartingPositionTimestamp  *float64                 `json:"StartingPositionTimestamp"`
	MaximumRetryAttempts       *int                     `json:"MaximumRetryAttempts"`
	MaximumRecordAgeInSeconds  *int                     `json:"MaximumRecordAgeInSeconds"`
	BisectBatchOnFunctionError *bool                    `json:"BisectBatchOnFunctionError"`
	DestinationConfig          *lambdaDestinationConfig `json:"DestinationConfig"`
}

func (s lambdaESMStreamSettings) apply(esm *LambdaEventSourceMapping) {
	if s.StartingPositionTimestamp != nil {
		esm.StartingPositionTimestamp = s.StartingPositionTimestamp
	}
	if s.MaximumRetryAttempts != nil {
		esm.MaximumRetryAttempts = s.MaximumRetryAttempts
	}
	if s.MaximumRecordAgeInSeconds != nil {
		esm.MaximumRecordAgeInSeconds = s.MaximumRecordAgeInSeconds
	}
	if s.BisectBatchOnFunctionError != nil {
		esm.BisectBatchOnFunctionError = s.BisectBatchOnFunctionError
	}
	if s.DestinationConfig != nil {
		esm.DestinationConfig = s.DestinationConfig
	}
}

// lambdaESMStreamDefaults fills the settings a Kinesis mapping reports when
// the request leaves them out, from the defaults AWS documents for them
// (services-kinesis-parameters.md.gz): BatchSize 100, MaximumRetryAttempts and
// MaximumRecordAgeInSeconds -1, BisectBatchOnFunctionError false,
// ParallelizationFactor 1, MaximumBatchingWindowInSeconds 0.
func lambdaESMStreamDefaults(esm *LambdaEventSourceMapping) {
	value := func(n int) *int { return &n }
	if esm.BatchSize == nil {
		esm.BatchSize = value(100)
	}
	if esm.MaximumRetryAttempts == nil {
		esm.MaximumRetryAttempts = value(-1)
	}
	if esm.MaximumRecordAgeInSeconds == nil {
		esm.MaximumRecordAgeInSeconds = value(-1)
	}
	if esm.ParallelizationFactor == nil {
		esm.ParallelizationFactor = value(1)
	}
	if esm.MaximumBatchingWindowInSeconds == nil {
		esm.MaximumBatchingWindowInSeconds = value(0)
	}
	if esm.BisectBatchOnFunctionError == nil {
		off := false
		esm.BisectBatchOnFunctionError = &off
	}
	if esm.DestinationConfig == nil {
		esm.DestinationConfig = &lambdaDestinationConfig{OnFailure: &lambdaDestination{}}
	}
}

var lambdaESMs sim.Store[LambdaEventSourceMapping] // keyed by UUID

// lambdaESMTags holds each mapping's tags, which ListTags reads and the
// mapping's configuration does not carry.
var lambdaESMTags sim.Store[map[string]string]

func lambdaESMArn(uuid string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:event-source-mapping:%s", awsRegion(), awsAccountID(), uuid)
}

// lambdaResolveFunctionArn turns a FunctionName into the canonical function ARN;
// AWS Lambda accepts a bare name, a partial ARN or a full ARN.
func lambdaResolveFunctionArn(functionName string) (string, bool) {
	name := functionName
	if strings.Contains(functionName, ":function:") {
		parts := strings.SplitN(functionName, ":function:", 2)
		if len(parts) == 2 {
			name = parts[1]
		}
		// Strip a trailing :qualifier (version/alias).
		if i := strings.Index(name, ":"); i >= 0 {
			name = name[:i]
		}
	}
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		return "", false
	}
	return fn.FunctionArn, true
}

func handleLambdaCreateEventSourceMapping(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FunctionName                   string            `json:"FunctionName"`
		EventSourceArn                 string            `json:"EventSourceArn"`
		Enabled                        *bool             `json:"Enabled"`
		BatchSize                      *int              `json:"BatchSize"`
		MaximumBatchingWindowInSeconds *int              `json:"MaximumBatchingWindowInSeconds"`
		ParallelizationFactor          *int              `json:"ParallelizationFactor"`
		StartingPosition               string            `json:"StartingPosition"`
		Topics                         []string          `json:"Topics"`
		Queues                         []string          `json:"Queues"`
		FunctionResponseTypes          []string          `json:"FunctionResponseTypes"`
		FilterCriteria                 map[string]any    `json:"FilterCriteria"`
		Tags                           map[string]string `json:"Tags"`
		lambdaESMStreamSettings
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.FunctionName == "" {
		AWSError(w, "InvalidParameterValueException", "FunctionName is required", http.StatusBadRequest)
		return
	}
	functionArn, ok := lambdaResolveFunctionArn(req.FunctionName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", req.FunctionName)
		return
	}
	if _, err := lambdaESMFilters(req.FilterCriteria); err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(req.EventSourceArn, "arn:aws:sqs:") {
		queueName := snsTopicNameFromARN(req.EventSourceArn)
		if _, exists := sqsQueues.Get(queueName); !exists {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Event source does not exist: %s", req.EventSourceArn)
			return
		}
	}
	kinesis := strings.HasPrefix(req.EventSourceArn, "arn:aws:kinesis:")
	if kinesis {
		if _, exists := kinesisStreamByNameOrARN("", req.EventSourceArn); !exists {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Event source does not exist: %s", req.EventSourceArn)
			return
		}
		switch req.StartingPosition {
		case "TRIM_HORIZON", "LATEST", "AT_TIMESTAMP":
		default:
			AWSError(w, "InvalidParameterValueException",
				"StartingPosition must be TRIM_HORIZON, LATEST or AT_TIMESTAMP for an Amazon Kinesis event source", http.StatusBadRequest)
			return
		}
	}

	uuid := sim.NewUUID()
	state := "Enabled"
	if req.Enabled != nil && !*req.Enabled {
		state = "Disabled"
	}
	esm := LambdaEventSourceMapping{
		UUID:                           uuid,
		EventSourceMappingArn:          lambdaESMArn(uuid),
		EventSourceArn:                 req.EventSourceArn,
		FunctionArn:                    functionArn,
		State:                          state,
		StateTransitionReason:          "USER_INITIATED",
		LastModified:                   lambdaNowEpoch(),
		LastProcessingResult:           "No records processed",
		BatchSize:                      req.BatchSize,
		MaximumBatchingWindowInSeconds: req.MaximumBatchingWindowInSeconds,
		ParallelizationFactor:          req.ParallelizationFactor,
		StartingPosition:               req.StartingPosition,
		Topics:                         req.Topics,
		Queues:                         req.Queues,
		FunctionResponseTypes:          req.FunctionResponseTypes,
		FilterCriteria:                 req.FilterCriteria,
	}
	req.apply(&esm)
	if kinesis {
		lambdaESMStreamDefaults(&esm)
	}
	if len(req.Tags) > 0 {
		lambdaESMTags.Put(uuid, req.Tags)
	}
	lambdaESMs.Put(uuid, esm)
	sim.WriteJSON(w, http.StatusAccepted, esm)
}

func handleLambdaGetEventSourceMapping(w http.ResponseWriter, r *http.Request) {
	uuid := sim.PathParam(r, "uuid")
	esm, ok := lambdaESMs.Get(uuid)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Event source mapping not found: %s", uuid)
		return
	}
	sim.WriteJSON(w, http.StatusOK, esm)
}

func handleLambdaListEventSourceMappings(w http.ResponseWriter, r *http.Request) {
	functionName := r.URL.Query().Get("FunctionName")
	eventSourceArn := r.URL.Query().Get("EventSourceArn")

	var wantFunctionArn string
	if functionName != "" {
		arn, ok := lambdaResolveFunctionArn(functionName)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function not found: %s", functionName)
			return
		}
		wantFunctionArn = arn
	}

	all := make([]LambdaEventSourceMapping, 0, lambdaESMs.Len())
	for _, esm := range lambdaESMs.List() {
		if wantFunctionArn != "" && esm.FunctionArn != wantFunctionArn {
			continue
		}
		if eventSourceArn != "" && esm.EventSourceArn != eventSourceArn {
			continue
		}
		all = append(all, esm)
	}

	marker := r.URL.Query().Get("Marker")
	maxItems := 100
	if raw := r.URL.Query().Get("MaxItems"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxItems = n
		}
	}
	page, next, pageOK := awsKeyPage(w, lambdaBadToken, all, func(it LambdaEventSourceMapping) string { return it.UUID }, marker, maxItems, 100)
	if !pageOK {
		return
	}
	out := map[string]any{"EventSourceMappings": page}
	if next != "" {
		out["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleLambdaUpdateEventSourceMapping(w http.ResponseWriter, r *http.Request) {
	uuid := sim.PathParam(r, "uuid")
	var req struct {
		FunctionName                   string         `json:"FunctionName"`
		Enabled                        *bool          `json:"Enabled"`
		BatchSize                      *int           `json:"BatchSize"`
		MaximumBatchingWindowInSeconds *int           `json:"MaximumBatchingWindowInSeconds"`
		ParallelizationFactor          *int           `json:"ParallelizationFactor"`
		FunctionResponseTypes          []string       `json:"FunctionResponseTypes"`
		FilterCriteria                 map[string]any `json:"FilterCriteria"`
		lambdaESMStreamSettings
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	esm, ok := lambdaESMs.Get(uuid)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Event source mapping not found: %s", uuid)
		return
	}
	if _, err := lambdaESMFilters(req.FilterCriteria); err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.FunctionName != "" {
		arn, ok := lambdaResolveFunctionArn(req.FunctionName)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
				"Function not found: %s", req.FunctionName)
			return
		}
		esm.FunctionArn = arn
	}
	if req.Enabled != nil {
		if *req.Enabled {
			esm.State = "Enabled"
		} else {
			esm.State = "Disabled"
		}
	}
	if req.BatchSize != nil {
		esm.BatchSize = req.BatchSize
	}
	if req.MaximumBatchingWindowInSeconds != nil {
		esm.MaximumBatchingWindowInSeconds = req.MaximumBatchingWindowInSeconds
	}
	if req.ParallelizationFactor != nil {
		esm.ParallelizationFactor = req.ParallelizationFactor
	}
	if req.FunctionResponseTypes != nil {
		esm.FunctionResponseTypes = req.FunctionResponseTypes
	}
	if req.FilterCriteria != nil {
		esm.FilterCriteria = req.FilterCriteria
	}
	req.apply(&esm)
	esm.LastModified = lambdaNowEpoch()
	esm.StateTransitionReason = "USER_INITIATED"
	lambdaESMs.Put(uuid, esm)
	sim.WriteJSON(w, http.StatusAccepted, esm)
}

func handleLambdaDeleteEventSourceMapping(w http.ResponseWriter, r *http.Request) {
	uuid := sim.PathParam(r, "uuid")
	esm, ok := lambdaESMs.Get(uuid)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Event source mapping not found: %s", uuid)
		return
	}
	lambdaESMs.Delete(uuid)
	lambdaESMTags.Delete(uuid)
	for _, shard := range lambdaESMShards.ListPrefix(uuid + "\x00") {
		lambdaESMShards.Delete(shard.ID)
	}
	esm.State = "Deleting"
	esm.StateTransitionReason = "USER_INITIATED"
	esm.LastModified = lambdaNowEpoch()
	sim.WriteJSON(w, http.StatusAccepted, esm)
}

// lambdaNowEpoch returns the current time as fractional epoch-seconds, the
// restJson1 default encoding for the Lambda `Date` timestamp shape.
func lambdaNowEpoch() float64 {
	return float64(time.Now().UnixMilli()) / 1000.0
}

// AWS Lambda numbers a layer's versions from 1, per layer name.
type LambdaLayerVersion struct {
	LayerName               string
	Version                 int64
	Description             string
	CreatedDate             string // ISO-8601 (Lambda `Timestamp` is a string shape)
	CompatibleRuntimes      []string
	CompatibleArchitectures []string
	LicenseInfo             string
	CodeSha256              string
	CodeSize                int64
	Content                 []byte
}

type lambdaLayerContentInput struct {
	S3Bucket        string `json:"S3Bucket,omitempty"`
	S3Key           string `json:"S3Key,omitempty"`
	S3ObjectVersion string `json:"S3ObjectVersion,omitempty"`
	ZipFile         string `json:"ZipFile,omitempty"`
}

var lambdaLayers sim.Store[[]LambdaLayerVersion] // keyed by layer name

func lambdaLayerArn(name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:layer:%s", awsRegion(), awsAccountID(), name)
}

func lambdaLayerVersionArn(name string, version int64) string {
	return fmt.Sprintf("%s:%d", lambdaLayerArn(name), version)
}

func lambdaLayerContentOutput(r *http.Request, lv LambdaLayerVersion) (map[string]any, error) {
	key := fmt.Sprintf("layers/%s/%d.zip", lv.LayerName, lv.Version)
	if err := lambdaPutArtifact(key, lv.Content); err != nil {
		return nil, err
	}
	return map[string]any{
		"Location": presignedS3URLBase(
			awsRequestURLBase(r), lambdaArtifactBucketName(), key, http.MethodGet,
		),
		"CodeSha256": lv.CodeSha256,
		"CodeSize":   lv.CodeSize,
	}, nil
}

func lambdaLayerVersionResponse(r *http.Request, lv LambdaLayerVersion) (map[string]any, error) {
	content, err := lambdaLayerContentOutput(r, lv)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"Content":         content,
		"LayerArn":        lambdaLayerArn(lv.LayerName),
		"LayerVersionArn": lambdaLayerVersionArn(lv.LayerName, lv.Version),
		"Version":         lv.Version,
		"CreatedDate":     lv.CreatedDate,
	}
	if lv.Description != "" {
		out["Description"] = lv.Description
	}
	if len(lv.CompatibleRuntimes) > 0 {
		out["CompatibleRuntimes"] = lv.CompatibleRuntimes
	}
	if len(lv.CompatibleArchitectures) > 0 {
		out["CompatibleArchitectures"] = lv.CompatibleArchitectures
	}
	if lv.LicenseInfo != "" {
		out["LicenseInfo"] = lv.LicenseInfo
	}
	return out, nil
}

func lambdaLayerVersionsListItem(lv LambdaLayerVersion) map[string]any {
	out := map[string]any{
		"LayerVersionArn": lambdaLayerVersionArn(lv.LayerName, lv.Version),
		"Version":         lv.Version,
		"CreatedDate":     lv.CreatedDate,
	}
	if lv.Description != "" {
		out["Description"] = lv.Description
	}
	if len(lv.CompatibleRuntimes) > 0 {
		out["CompatibleRuntimes"] = lv.CompatibleRuntimes
	}
	if len(lv.CompatibleArchitectures) > 0 {
		out["CompatibleArchitectures"] = lv.CompatibleArchitectures
	}
	if lv.LicenseInfo != "" {
		out["LicenseInfo"] = lv.LicenseInfo
	}
	return out
}

func handleLambdaPublishLayerVersion(w http.ResponseWriter, r *http.Request) {
	layerName := sim.PathParam(r, "layer")
	if layerName == "" {
		AWSError(w, "InvalidParameterValueException", "LayerName is required", http.StatusBadRequest)
		return
	}
	var req struct {
		Description             string                   `json:"Description"`
		Content                 *lambdaLayerContentInput `json:"Content"`
		CompatibleRuntimes      []string                 `json:"CompatibleRuntimes"`
		CompatibleArchitectures []string                 `json:"CompatibleArchitectures"`
		LicenseInfo             string                   `json:"LicenseInfo"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Content == nil {
		AWSError(w, "InvalidParameterValueException", "Content is required", http.StatusBadRequest)
		return
	}
	code := &LambdaFunctionCode{
		S3Bucket:        req.Content.S3Bucket,
		S3Key:           req.Content.S3Key,
		S3ObjectVersion: req.Content.S3ObjectVersion,
		ZipFile:         req.Content.ZipFile,
	}
	content, err := lambdaDeploymentPackageBytes(code)
	if err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateLambdaDeploymentPackage(code); err != nil {
		AWSError(w, "InvalidParameterValueException", err.Error(), http.StatusBadRequest)
		return
	}

	var lv LambdaLayerVersion
	lambdaLayers.Upsert(layerName, func(existing *[]LambdaLayerVersion) {
		lv = LambdaLayerVersion{
			LayerName:               layerName,
			Version:                 int64(len(*existing) + 1),
			Description:             req.Description,
			CreatedDate:             time.Now().UTC().Format("2006-01-02T15:04:05.000-0700"),
			CompatibleRuntimes:      req.CompatibleRuntimes,
			CompatibleArchitectures: req.CompatibleArchitectures,
			LicenseInfo:             req.LicenseInfo,
			CodeSha256:              lambdaCodeSha256(code),
			CodeSize:                int64(len(content)),
			Content:                 append([]byte(nil), content...),
		}
		*existing = append(*existing, lv)
	})

	response, err := lambdaLayerVersionResponse(r, lv)
	if err != nil {
		AWSError(w, "ServiceException", err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusCreated, response)
}

func handleLambdaListLayerVersions(w http.ResponseWriter, r *http.Request) {
	layerName := sim.PathParam(r, "layer")
	versions, _ := lambdaLayers.Get(layerName)
	// Newest first, as AWS Lambda lists them.
	items := make([]map[string]any, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		items = append(items, lambdaLayerVersionsListItem(versions[i]))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"LayerVersions": items})
}

func handleLambdaGetLayerVersion(w http.ResponseWriter, r *http.Request) {
	layerName := sim.PathParam(r, "layer")
	versionStr := sim.PathParam(r, "version")
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		AWSError(w, "InvalidParameterValueException", "VersionNumber must be an integer", http.StatusBadRequest)
		return
	}
	lv, ok := lambdaFindLayerVersion(layerName, version)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Layer version %s:%d not found", layerName, version)
		return
	}
	response, err := lambdaLayerVersionResponse(r, lv)
	if err != nil {
		AWSError(w, "ServiceException", err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func handleLambdaDeleteLayerVersion(w http.ResponseWriter, r *http.Request) {
	layerName := sim.PathParam(r, "layer")
	versionStr := sim.PathParam(r, "version")
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil {
		AWSError(w, "InvalidParameterValueException", "VersionNumber must be an integer", http.StatusBadRequest)
		return
	}
	// DeleteLayerVersion is idempotent: a missing version still returns 204.
	lambdaLayers.Update(layerName, func(versions *[]LambdaLayerVersion) {
		out := make([]LambdaLayerVersion, 0, len(*versions))
		for _, lv := range *versions {
			if lv.Version != version {
				out = append(out, lv)
			}
		}
		*versions = out
	})
	w.WriteHeader(http.StatusNoContent)
}

func lambdaFindLayerVersion(name string, version int64) (LambdaLayerVersion, bool) {
	versions, _ := lambdaLayers.Get(name)
	for _, lv := range versions {
		if lv.Version == version {
			return lv, true
		}
	}
	return LambdaLayerVersion{}, false
}

func lambdaLayerVersionByARN(arn string) (LambdaLayerVersion, bool) {
	lastColon := strings.LastIndexByte(arn, ':')
	layerMarker := strings.Index(arn, ":layer:")
	if layerMarker < 0 || lastColon <= layerMarker+len(":layer:") {
		return LambdaLayerVersion{}, false
	}
	version, err := strconv.ParseInt(arn[lastColon+1:], 10, 64)
	if err != nil {
		return LambdaLayerVersion{}, false
	}
	return lambdaFindLayerVersion(arn[layerMarker+len(":layer:"):lastColon], version)
}

// lambdaLayersEventName composes the CloudTrail event name for the shared
// GET /2018-10-31/layers path: ListLayers normally, GetLayerVersionByArn when
// the find=LayerVersion query selector is present.
func lambdaLayersEventName(r *http.Request, _ []byte) string {
	if r.URL.Query().Get("find") == "LayerVersion" {
		return "GetLayerVersionByArn"
	}
	return "ListLayers"
}

// handleLambdaListLayers serves both ListLayers and GetLayerVersionByArn (the
// latter selected by ?find=LayerVersion&Arn=<layer-version-arn>).
func handleLambdaListLayers(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("find") == "LayerVersion" {
		handleLambdaGetLayerVersionByArn(w, r)
		return
	}
	names := make([]string, 0, lambdaLayers.Len())
	snapshot := map[string]LambdaLayerVersion{}
	for _, versions := range lambdaLayers.List() {
		if len(versions) > 0 {
			name := versions[0].LayerName
			names = append(names, name)
			snapshot[name] = versions[len(versions)-1]
		}
	}

	sort.Strings(names)
	layers := make([]map[string]any, 0, len(names))
	for _, name := range names {
		latest := snapshot[name]
		layers = append(layers, map[string]any{
			"LayerName":             name,
			"LayerArn":              lambdaLayerArn(name),
			"LatestMatchingVersion": lambdaLayerVersionsListItem(latest),
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Layers": layers})
}

func handleLambdaGetLayerVersionByArn(w http.ResponseWriter, r *http.Request) {
	arn := r.URL.Query().Get("Arn")
	if arn == "" {
		AWSError(w, "InvalidParameterValueException", "Arn is required", http.StatusBadRequest)
		return
	}
	// arn:aws:lambda:<region>:<account>:layer:<name>:<version>
	idx := strings.Index(arn, ":layer:")
	if idx < 0 {
		AWSError(w, "ValidationException", "Arn is not a layer-version ARN", http.StatusBadRequest)
		return
	}
	tail := arn[idx+len(":layer:"):]
	parts := strings.SplitN(tail, ":", 2)
	if len(parts) != 2 {
		AWSError(w, "ValidationException", "Arn must include a layer version", http.StatusBadRequest)
		return
	}
	name := parts[0]
	version, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		AWSError(w, "InvalidParameterValueException", "Layer version must be an integer", http.StatusBadRequest)
		return
	}
	lv, ok := lambdaFindLayerVersion(name, version)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Layer version %s:%d not found", name, version)
		return
	}
	response, err := lambdaLayerVersionResponse(r, lv)
	if err != nil {
		AWSError(w, "ServiceException", err.Error(), http.StatusInternalServerError)
		return
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func handleLambdaPutFunctionConcurrency(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	var req struct {
		ReservedConcurrentExecutions *int `json:"ReservedConcurrentExecutions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValueException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ReservedConcurrentExecutions == nil {
		AWSError(w, "InvalidParameterValueException",
			"ReservedConcurrentExecutions is required", http.StatusBadRequest)
		return
	}
	reserved := *req.ReservedConcurrentExecutions
	if reserved < 0 {
		AWSError(w, "InvalidParameterValueException",
			"1 validation error detected: Value at 'reservedConcurrentExecutions' failed to satisfy constraint: Member must have value greater than or equal to 0",
			http.StatusBadRequest)
		return
	}
	current, _ := lambdaConcurrency.Get(name)
	if lambdaUnreservedConcurrency()+current-reserved < lambdaMinimumUnreservedConcurrency {
		AWSErrorf(w, "InvalidParameterValueException", http.StatusBadRequest,
			"Specified ReservedConcurrentExecutions for function decreases account's UnreservedConcurrentExecution below its minimum value of [%d].",
			lambdaMinimumUnreservedConcurrency)
		return
	}
	// "It's possible to allocate both reserved concurrency and provisioned
	// concurrency for the same function. In such cases, the provisioned
	// concurrency cannot exceed the reserved concurrency."
	provisioned := 0
	for _, n := range lambdaProvisionedTargets(name) {
		provisioned += n
	}
	if reserved < provisioned {
		AWSErrorf(w, "InvalidParameterValueException", http.StatusBadRequest,
			"Specified ReservedConcurrentExecutions for function is less than the %d units of provisioned concurrency configured on its versions and aliases.",
			provisioned)
		return
	}
	lambdaConcurrency.Put(name, reserved)
	lambdaEnvironments.reconcile(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ReservedConcurrentExecutions": reserved,
	})
}

func handleLambdaGetFunctionConcurrency(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	reserved, ok := lambdaConcurrency.Get(name)
	out := map[string]any{}
	// Real Lambda omits ReservedConcurrentExecutions entirely when none is set.
	if ok {
		out["ReservedConcurrentExecutions"] = reserved
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleLambdaDeleteFunctionConcurrency(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	lambdaConcurrency.Delete(name)
	lambdaEnvironments.reconcile(name)
	w.WriteHeader(http.StatusNoContent)
}

func handleLambdaListFunctionUrlConfigs(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := lambdaFunctions.Get(name); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusNotFound,
			"Function not found: %s", lambdaArn(name))
		return
	}
	cfg, ok := lambdaURLConfigs.Get(name)
	configs := []LambdaFunctionUrlConfig{}
	if ok {
		configs = append(configs, cfg)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"FunctionUrlConfigs": configs})
}
