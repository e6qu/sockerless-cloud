package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

type APIGWMethodSetting struct {
	MetricsEnabled                         bool    `json:"metricsEnabled"`
	LoggingLevel                           string  `json:"loggingLevel,omitempty"`
	DataTraceEnabled                       bool    `json:"dataTraceEnabled"`
	ThrottlingBurstLimit                   int     `json:"throttlingBurstLimit"`
	ThrottlingRateLimit                    float64 `json:"throttlingRateLimit"`
	CachingEnabled                         bool    `json:"cachingEnabled"`
	CacheTtlInSeconds                      int     `json:"cacheTtlInSeconds"`
	CacheDataEncrypted                     bool    `json:"cacheDataEncrypted"`
	RequireAuthorizationForCacheControl    bool    `json:"requireAuthorizationForCacheControl"`
	UnauthorizedCacheControlHeaderStrategy string  `json:"unauthorizedCacheControlHeaderStrategy,omitempty"`
}

type APIGWAccessLogSettings struct {
	Format         string `json:"format,omitempty"`
	DestinationArn string `json:"destinationArn,omitempty"`
}

type APIGWCanarySettings struct {
	PercentTraffic         float64           `json:"percentTraffic"`
	DeploymentId           string            `json:"deploymentId,omitempty"`
	StageVariableOverrides map[string]string `json:"stageVariableOverrides,omitempty"`
	UseStageCache          bool              `json:"useStageCache"`
}

type APIGWStage struct {
	StageName            string                        `json:"stageName"`
	RestApiId            string                        `json:"-"`
	DeploymentId         string                        `json:"deploymentId"`
	ClientCertificateId  string                        `json:"clientCertificateId,omitempty"`
	Description          string                        `json:"description,omitempty"`
	CacheClusterEnabled  bool                          `json:"cacheClusterEnabled"`
	CacheClusterSize     string                        `json:"cacheClusterSize,omitempty"`
	CacheClusterStatus   string                        `json:"cacheClusterStatus,omitempty"`
	MethodSettings       map[string]APIGWMethodSetting `json:"methodSettings,omitempty"`
	Variables            map[string]string             `json:"variables,omitempty"`
	DocumentationVersion string                        `json:"documentationVersion,omitempty"`
	AccessLogSettings    *APIGWAccessLogSettings       `json:"accessLogSettings,omitempty"`
	CanarySettings       *APIGWCanarySettings          `json:"canarySettings,omitempty"`
	TracingEnabled       bool                          `json:"tracingEnabled"`
	Tags                 map[string]string             `json:"tags,omitempty"`
	CreatedDate          int64                         `json:"createdDate"`
	LastUpdatedDate      int64                         `json:"lastUpdatedDate"`
}

// apigwCacheClusterSizes lists the CacheClusterSize enum of the Smithy model.
var apigwCacheClusterSizes = map[string]bool{
	"0.5": true, "1.6": true, "6.1": true, "13.5": true,
	"28.4": true, "58.2": true, "118": true, "237": true,
}

func apigwStageKey(restApiId, stageName string) string {
	return restApiId + "/" + stageName
}

func apigwDeploymentExists(apiId, deploymentId string) bool {
	_, ok := apigwDeployments.Get(apigwDeploymentKey(apiId, deploymentId))
	return ok
}

// apigwSetCacheCluster records a cache cluster change. The stage reports
// CREATE_IN_PROGRESS or DELETE_IN_PROGRESS until the returned settle, which
// the caller runs once it has stored the stage, provisions the cluster.
func apigwSetCacheCluster(key string, s *APIGWStage, enabled bool) (settle func()) {
	settle = func() {}
	if enabled && s.CacheClusterSize == "" {
		s.CacheClusterSize = "0.5"
	}
	switch {
	case enabled && !s.CacheClusterEnabled:
		s.CacheClusterStatus = "CREATE_IN_PROGRESS"
		settle = func() { go apigwSettleCacheCluster(key, "CREATE_IN_PROGRESS", "AVAILABLE") }
	case !enabled && s.CacheClusterEnabled:
		s.CacheClusterStatus = "DELETE_IN_PROGRESS"
		settle = func() { go apigwSettleCacheCluster(key, "DELETE_IN_PROGRESS", "NOT_AVAILABLE") }
	case !enabled && s.CacheClusterStatus == "":
		s.CacheClusterStatus = "NOT_AVAILABLE"
	}
	s.CacheClusterEnabled = enabled
	return settle
}

func apigwSettleCacheCluster(key, from, to string) {
	apigwStages.Update(key, func(s *APIGWStage) {
		if s.CacheClusterStatus == from {
			s.CacheClusterStatus = to
		}
	})
}

func handleAPIGWCreateStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	var req struct {
		StageName            string               `json:"stageName"`
		DeploymentId         string               `json:"deploymentId"`
		Description          string               `json:"description"`
		CacheClusterEnabled  bool                 `json:"cacheClusterEnabled"`
		CacheClusterSize     string               `json:"cacheClusterSize"`
		Variables            map[string]string    `json:"variables"`
		DocumentationVersion string               `json:"documentationVersion"`
		CanarySettings       *APIGWCanarySettings `json:"canarySettings"`
		TracingEnabled       bool                 `json:"tracingEnabled"`
		Tags                 map[string]string    `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "BadRequestException", err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := apigwRestApis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	if req.StageName == "" || req.DeploymentId == "" {
		AWSError(w, "BadRequestException", "stageName and deploymentId are required", http.StatusBadRequest)
		return
	}
	if !apigwDeploymentExists(apiId, req.DeploymentId) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Deployment identifier specified")
		return
	}
	if req.CacheClusterSize != "" && !apigwCacheClusterSizes[req.CacheClusterSize] {
		AWSError(w, "BadRequestException", "Invalid cache cluster size specified: "+req.CacheClusterSize, http.StatusBadRequest)
		return
	}
	if req.CanarySettings != nil && req.CanarySettings.DeploymentId != "" && !apigwDeploymentExists(apiId, req.CanarySettings.DeploymentId) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Deployment identifier specified")
		return
	}
	key := apigwStageKey(apiId, req.StageName)
	if _, exists := apigwStages.Get(key); exists {
		AWSError(w, "ConflictException", "Stage already exists", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	s := APIGWStage{
		StageName:            req.StageName,
		RestApiId:            apiId,
		DeploymentId:         req.DeploymentId,
		Description:          req.Description,
		CacheClusterSize:     req.CacheClusterSize,
		Variables:            req.Variables,
		DocumentationVersion: req.DocumentationVersion,
		CanarySettings:       req.CanarySettings,
		TracingEnabled:       req.TracingEnabled,
		Tags:                 req.Tags,
		CreatedDate:          now,
		LastUpdatedDate:      now,
	}
	settle := apigwSetCacheCluster(key, &s, req.CacheClusterEnabled)
	apigwStages.Put(key, s)
	settle()
	sim.WriteJSON(w, http.StatusCreated, s)
}

func handleAPIGWListStages(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	if _, ok := apigwRestApis.Get(apiId); !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Rest API identifier specified")
		return
	}
	deploymentId := r.URL.Query().Get("deploymentId")
	out := []APIGWStage{}
	for _, s := range apigwStages.List() {
		if s.RestApiId == apiId && (deploymentId == "" || s.DeploymentId == deploymentId) {
			out = append(out, s)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"item": out})
}

func handleAPIGWGetStage(w http.ResponseWriter, r *http.Request) {
	s, ok := apigwStages.Get(apigwStageKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "stageName")))
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIGWDeleteStage(w http.ResponseWriter, r *http.Request) {
	if !apigwStages.Delete(apigwStageKey(sim.PathParam(r, "restApiId"), sim.PathParam(r, "stageName"))) {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// apigwStagePatchError names an UpdateStage path and op pair that
// specs/cloud-api/aws/api-gateway-patch-operations does not support.
func apigwStagePatchError(op apigwPatchOp) string {
	return "Invalid patch path '" + op.Path + "' specified for op '" + op.Op + "'"
}

func handleAPIGWUpdateStage(w http.ResponseWriter, r *http.Request) {
	apiId := sim.PathParam(r, "restApiId")
	key := apigwStageKey(apiId, sim.PathParam(r, "stageName"))
	s, ok := apigwStages.Get(key)
	if !ok {
		AWSErrorf(w, "NotFoundException", http.StatusNotFound, "Invalid Stage identifier specified")
		return
	}
	ops, ok := apigwReadPatches(w, r)
	if !ok {
		return
	}
	cacheEnabled := s.CacheClusterEnabled
	for _, op := range ops {
		if msg := apigwPatchStage(apiId, &s, &cacheEnabled, op); msg != "" {
			AWSError(w, "BadRequestException", msg, http.StatusBadRequest)
			return
		}
	}
	settle := apigwSetCacheCluster(key, &s, cacheEnabled)
	s.LastUpdatedDate = time.Now().Unix()
	apigwStages.Put(key, s)
	settle()
	sim.WriteJSON(w, http.StatusOK, s)
}

// apigwPatchStage applies one UpdateStage operation and returns "" or the
// BadRequestException message.
func apigwPatchStage(apiId string, s *APIGWStage, cacheEnabled *bool, op apigwPatchOp) string {
	replace := op.Op == "replace"
	settable := replace || op.Op == "add"
	path := op.Path
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case path == "/deploymentId" && op.Op == "copy":
		if op.From != "/canarySettings/deploymentId" || s.CanarySettings == nil || s.CanarySettings.DeploymentId == "" {
			return apigwStagePatchError(op)
		}
		s.DeploymentId = s.CanarySettings.DeploymentId
	case path == "/deploymentId" && replace:
		if !apigwDeploymentExists(apiId, op.Value) {
			return "Invalid Deployment identifier specified"
		}
		s.DeploymentId = op.Value
	case path == "/description" && replace:
		s.Description = op.Value
	case path == "/clientCertificateId" && replace:
		if op.Value != "" {
			if _, ok := apigwClientCertificates.Get(op.Value); !ok {
				return "Invalid client certificate identifier specified"
			}
		}
		s.ClientCertificateId = op.Value
	case path == "/documentationVersion" && replace:
		s.DocumentationVersion = op.Value
	case path == "/cacheClusterEnabled" && replace:
		*cacheEnabled = op.Value == "true"
	case path == "/cacheClusterSize" && replace:
		if !apigwCacheClusterSizes[op.Value] {
			return "Invalid cache cluster size specified: " + op.Value
		}
		s.CacheClusterSize = op.Value
	case path == "/tracingEnabled" && replace:
		s.TracingEnabled = op.Value == "true"
	case path == "/variables" && op.Op == "remove":
		s.Variables = nil
	case segs[0] == "variables" && len(segs) == 2:
		switch {
		case settable:
			if s.Variables == nil {
				s.Variables = map[string]string{}
			}
			s.Variables[segs[1]] = op.Value
		case op.Op == "remove":
			delete(s.Variables, segs[1])
		default:
			return apigwStagePatchError(op)
		}
	case path == "/accessLogSettings" && op.Op == "remove":
		s.AccessLogSettings = nil
	case segs[0] == "accessLogSettings" && len(segs) == 2 && (segs[1] == "destinationArn" || segs[1] == "format"):
		switch {
		case settable:
			if s.AccessLogSettings == nil {
				s.AccessLogSettings = &APIGWAccessLogSettings{}
			}
			if segs[1] == "destinationArn" {
				s.AccessLogSettings.DestinationArn = op.Value
			} else {
				s.AccessLogSettings.Format = op.Value
			}
		case op.Op == "remove":
			if s.AccessLogSettings != nil {
				if segs[1] == "destinationArn" {
					s.AccessLogSettings.DestinationArn = ""
				} else {
					s.AccessLogSettings.Format = ""
				}
				if *s.AccessLogSettings == (APIGWAccessLogSettings{}) {
					s.AccessLogSettings = nil
				}
			}
		default:
			return apigwStagePatchError(op)
		}
	case path == "/canarySettings" && op.Op == "remove":
		s.CanarySettings = nil
	case segs[0] == "canarySettings" && replace:
		return apigwPatchCanary(apiId, s, segs[1:], op)
	case len(segs) >= 4 && replace:
		return apigwPatchMethodSetting(s, path, op.Value)
	case len(segs) >= 2 && op.Op == "remove":
		mkey := strings.TrimPrefix(path, "/")
		if _, ok := s.MethodSettings[mkey]; !ok {
			return apigwStagePatchError(op)
		}
		delete(s.MethodSettings, mkey)
	default:
		return apigwStagePatchError(op)
	}
	return ""
}

func apigwPatchCanary(apiId string, s *APIGWStage, segs []string, op apigwPatchOp) string {
	if s.CanarySettings == nil {
		s.CanarySettings = &APIGWCanarySettings{}
	}
	c := s.CanarySettings
	switch {
	case len(segs) == 1 && segs[0] == "deploymentId":
		if !apigwDeploymentExists(apiId, op.Value) {
			return "Invalid Deployment identifier specified"
		}
		c.DeploymentId = op.Value
	case len(segs) == 1 && segs[0] == "percentTraffic":
		v, err := strconv.ParseFloat(op.Value, 64)
		if err != nil || v < 0 || v > 100 {
			return "Invalid value for percentTraffic: " + op.Value
		}
		c.PercentTraffic = v
	case len(segs) == 1 && segs[0] == "useStageCache":
		c.UseStageCache = op.Value == "true"
	case len(segs) == 2 && segs[0] == "stageVariableOverrides":
		if c.StageVariableOverrides == nil {
			c.StageVariableOverrides = map[string]string{}
		}
		c.StageVariableOverrides[segs[1]] = op.Value
	default:
		return apigwStagePatchError(op)
	}
	return ""
}

// apigwPatchMethodSetting sets one method setting. The key is the path between
// the leading slash and the two-segment setting name, kept exactly as sent.
func apigwPatchMethodSetting(s *APIGWStage, path, value string) string {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.Split(trimmed, "/")
	setting := parts[len(parts)-2] + "/" + parts[len(parts)-1]
	mkey := strings.Join(parts[:len(parts)-2], "/")
	m := s.MethodSettings[mkey]
	bad := "Invalid patch path '" + path + "' specified for op 'replace'"
	asBool := func(dst *bool) string {
		if value != "true" && value != "false" {
			return "Invalid boolean value for " + setting + ": " + value
		}
		*dst = value == "true"
		return ""
	}
	asInt := func(dst *int) string {
		v, err := strconv.Atoi(value)
		if err != nil {
			return "Invalid integer value for " + setting + ": " + value
		}
		*dst = v
		return ""
	}
	var msg string
	switch setting {
	case "metrics/enabled":
		msg = asBool(&m.MetricsEnabled)
	case "logging/loglevel":
		if value != "OFF" && value != "ERROR" && value != "INFO" {
			return "Invalid logging level: " + value
		}
		m.LoggingLevel = value
	case "logging/dataTrace":
		msg = asBool(&m.DataTraceEnabled)
	case "throttling/burstLimit":
		msg = asInt(&m.ThrottlingBurstLimit)
	case "throttling/rateLimit":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "Invalid double value for " + setting + ": " + value
		}
		m.ThrottlingRateLimit = v
	case "caching/enabled":
		msg = asBool(&m.CachingEnabled)
	case "caching/ttlInSeconds":
		msg = asInt(&m.CacheTtlInSeconds)
	case "caching/dataEncrypted":
		msg = asBool(&m.CacheDataEncrypted)
	case "caching/requireAuthorizationForCacheControl":
		msg = asBool(&m.RequireAuthorizationForCacheControl)
	case "caching/unauthorizedCacheControlHeaderStrategy":
		switch value {
		case "FAIL_WITH_403", "SUCCEED_WITH_RESPONSE_HEADER", "SUCCEED_WITHOUT_RESPONSE_HEADER":
			m.UnauthorizedCacheControlHeaderStrategy = value
		default:
			return "Invalid unauthorizedCacheControlHeaderStrategy: " + value
		}
	default:
		return bad
	}
	if msg != "" {
		return msg
	}
	if s.MethodSettings == nil {
		s.MethodSettings = map[string]APIGWMethodSetting{}
	}
	s.MethodSettings[mkey] = m
	return ""
}
