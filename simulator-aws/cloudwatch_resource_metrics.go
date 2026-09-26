package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Resource metrics configurations — detailed metric collection for one
// resource — and the include/exclude filters of vended-metric OTel enrichment.
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_CreateResourceMetricsConfiguration.html
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_UpdateOTelEnrichment.html

// CWResourceMetricsConfiguration is the one configuration a resource may have.
type CWResourceMetricsConfiguration struct {
	ResourceArn    string
	IncludeMetrics []string // nil: every available detailed metric
	CreatedAt      int64
	UpdatedAt      int64
}

// CWOTelSelector selects metrics of one namespace; no metric names selects the
// whole namespace.
type CWOTelSelector struct {
	Namespace   string   `json:"Namespace" cbor:"Namespace"`
	MetricNames []string `json:"MetricNames,omitempty" cbor:"MetricNames,omitempty"`
}

// CWOTelEnrichment is the account's enrichment: whether it runs, and the
// filters that choose what it enriches.
type CWOTelEnrichment struct {
	Status         string
	IncludeFilters []CWOTelSelector
	ExcludeFilters []CWOTelSelector
	CreatedAt      int64
	UpdatedAt      int64
}

var (
	cwResourceMetricsConfigs sim.Store[CWResourceMetricsConfiguration]
	cwOTelEnrichmentState    sim.Store[CWOTelEnrichment]
)

const (
	cwOTelRunning         = "Running"
	cwOTelStopped         = "Stopped"
	cwOTelMaxSelectors    = 100
	cwOTelMaxMetricNames  = 100
	cwMaxIncludeMetrics   = 500
	cwResourceArnMinBytes = 20
	cwResourceArnMaxBytes = 2048
)

var cwResourceArnPattern = regexp.MustCompile(`^arn:[a-zA-Z0-9-]+:[a-zA-Z0-9-]+:[a-zA-Z0-9-]*:\d{12}:.+$`)

// cwFailure is a refusal, independent of the protocol that reports it.
type cwFailure struct {
	code    string
	message string
	status  int
}

func cwFail(code string, status int, format string, args ...any) *cwFailure {
	return &cwFailure{code: code, message: fmt.Sprintf(format, args...), status: status}
}

func cwValidation(format string, args ...any) *cwFailure {
	return cwFail("ValidationException", http.StatusBadRequest, format, args...)
}

func registerCloudWatchResourceMetrics(r *AWSRouter, srv *sim.Server) {
	cwResourceMetricsConfigs = sim.MakeStore[CWResourceMetricsConfiguration](srv.DB(), "cw_resource_metrics_configurations")
	cwOTelEnrichmentState = sim.MakeStore[CWOTelEnrichment](srv.DB(), "cw_otel_enrichment_state")
	for op, handlers := range map[string][2]http.HandlerFunc{
		"CreateResourceMetricsConfiguration": {handleCWJSONCreateResourceMetricsConfiguration, handleCWCBORCreateResourceMetricsConfiguration},
		"GetResourceMetricsConfiguration":    {handleCWJSONGetResourceMetricsConfiguration, handleCWCBORGetResourceMetricsConfiguration},
		"UpdateResourceMetricsConfiguration": {handleCWJSONUpdateResourceMetricsConfiguration, handleCWCBORUpdateResourceMetricsConfiguration},
		"DeleteResourceMetricsConfiguration": {handleCWJSONDeleteResourceMetricsConfiguration, handleCWCBORDeleteResourceMetricsConfiguration},
		"GetOTelEnrichment":                  {handleCWJSONGetOTelEnrichment, handleCWCBORGetOTelEnrichment},
		"StartOTelEnrichment":                {handleCWJSONStartOTelEnrichment, handleCWCBORStartOTelEnrichment},
		"StopOTelEnrichment":                 {handleCWJSONStopOTelEnrichment, handleCWCBORStopOTelEnrichment},
		"UpdateOTelEnrichment":               {handleCWJSONUpdateOTelEnrichment, handleCWCBORUpdateOTelEnrichment},
	} {
		r.Register("GraniteServiceVersion20100801."+op, handlers[0])
		cwCBOR(srv, op, handlers[1])
	}
}

// cwResourceExists reports whether the simulator holds the resource an ARN
// names. The simulator is the whole account, so a resource of a type it does
// not model, or in another account or Region, does not exist here.
func cwResourceExists(arn string) bool {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[3] != awsRegion() || parts[4] != awsAccountID() {
		return false
	}
	service, resource := parts[2], parts[5]
	kind, id, _ := strings.Cut(resource, "/")
	switch service {
	case "ec2":
		switch kind {
		case "instance":
			_, ok := ec2Instances.Get(id)
			return ok
		case "volume":
			_, ok := ec2Volumes.Get(id)
			return ok
		}
	case "lambda":
		if name, ok := strings.CutPrefix(resource, "function:"); ok {
			name, _, _ = strings.Cut(name, ":")
			_, found := lambdaFunctions.Get(name)
			return found
		}
	case "dynamodb":
		if kind == "table" && !strings.Contains(id, "/") {
			_, ok := ddbTables.Get(id)
			return ok
		}
	case "kinesis":
		if kind == "stream" && !strings.Contains(id, "/") {
			_, ok := kinesisStreams.Get(id)
			return ok
		}
	case "sqs":
		_, ok := sqsQueues.Get(resource)
		return ok
	case "sns":
		_, ok := snsTopics.Get(resource)
		return ok
	case "ecs":
		if kind == "cluster" {
			_, ok := ecsClusters.Get(id)
			return ok
		}
	case "rds":
		if name, ok := strings.CutPrefix(resource, "db:"); ok {
			_, found := rdsInstances.Get(name)
			return found
		}
		if name, ok := strings.CutPrefix(resource, "cluster:"); ok {
			_, found := rdsClusters.Get(name)
			return found
		}
	}
	return false
}

type cwMetricSelection struct {
	IncludeMetrics []string `json:"IncludeMetrics" cbor:"IncludeMetrics"`
}

func cwCheckResourceArn(arn string) *cwFailure {
	if len(arn) < cwResourceArnMinBytes || len(arn) > cwResourceArnMaxBytes || !cwResourceArnPattern.MatchString(arn) {
		return cwValidation("1 validation error detected: Value '%s' at 'resourceArn' failed to satisfy constraint: Member must be a valid ARN", arn)
	}
	return nil
}

// cwIncludeMetrics reads MetricSelections: absent is every metric, and present
// it holds exactly one selection of 1 to 500 metric names.
func cwIncludeMetrics(selections []cwMetricSelection) ([]string, *cwFailure) {
	if selections == nil {
		return nil, nil
	}
	if len(selections) != 1 {
		return nil, cwValidation("MetricSelections must contain exactly one selection.")
	}
	names := selections[0].IncludeMetrics
	if len(names) < 1 || len(names) > cwMaxIncludeMetrics {
		return nil, cwValidation("IncludeMetrics must contain between 1 and %d metric names.", cwMaxIncludeMetrics)
	}
	return append([]string(nil), names...), nil
}

func cwCreateResourceMetricsConfiguration(arn string, selections []cwMetricSelection) (CWResourceMetricsConfiguration, *cwFailure) {
	if failure := cwCheckResourceArn(arn); failure != nil {
		return CWResourceMetricsConfiguration{}, failure
	}
	include, failure := cwIncludeMetrics(selections)
	if failure != nil {
		return CWResourceMetricsConfiguration{}, failure
	}
	if !cwResourceExists(arn) {
		return CWResourceMetricsConfiguration{}, cwFail("ResourceNotFoundException", http.StatusNotFound, "The resource %s does not exist.", arn)
	}
	if _, exists := cwResourceMetricsConfigs.Get(arn); exists {
		return CWResourceMetricsConfiguration{}, cwFail("ConflictException", http.StatusConflict,
			"A resource metrics configuration already exists for %s.", arn)
	}
	now := time.Now().UTC().Unix()
	config := CWResourceMetricsConfiguration{ResourceArn: arn, IncludeMetrics: include, CreatedAt: now, UpdatedAt: now}
	cwResourceMetricsConfigs.Put(arn, config)
	return config, nil
}

func cwExistingResourceMetricsConfiguration(arn string) (CWResourceMetricsConfiguration, *cwFailure) {
	if failure := cwCheckResourceArn(arn); failure != nil {
		return CWResourceMetricsConfiguration{}, failure
	}
	config, ok := cwResourceMetricsConfigs.Get(arn)
	if !ok {
		return CWResourceMetricsConfiguration{}, cwFail("ResourceNotFoundException", http.StatusNotFound,
			"No resource metrics configuration exists for %s.", arn)
	}
	return config, nil
}

// cwUpdateResourceMetricsConfiguration replaces the selections; omitting them
// removes the filter and collects every available metric again.
func cwUpdateResourceMetricsConfiguration(arn string, selections []cwMetricSelection) (CWResourceMetricsConfiguration, *cwFailure) {
	config, failure := cwExistingResourceMetricsConfiguration(arn)
	if failure != nil {
		return config, failure
	}
	include, failure := cwIncludeMetrics(selections)
	if failure != nil {
		return config, failure
	}
	config.IncludeMetrics = include
	config.UpdatedAt = time.Now().UTC().Unix()
	cwResourceMetricsConfigs.Put(arn, config)
	return config, nil
}

func cwDeleteResourceMetricsConfiguration(arn string) *cwFailure {
	if _, failure := cwExistingResourceMetricsConfiguration(arn); failure != nil {
		return failure
	}
	cwResourceMetricsConfigs.Delete(arn)
	return nil
}

func cwOTel() CWOTelEnrichment {
	if state, ok := cwOTelEnrichmentState.Get(cwOTelEnrichmentKey); ok {
		return state
	}
	return CWOTelEnrichment{Status: cwOTelStopped}
}

func cwCheckOTelFilters(include, exclude []CWOTelSelector) *cwFailure {
	if len(include)+len(exclude) > cwOTelMaxSelectors {
		return cwValidation("IncludeFilters and ExcludeFilters together may hold at most %d selectors.", cwOTelMaxSelectors)
	}
	for _, selector := range append(append([]CWOTelSelector(nil), include...), exclude...) {
		if selector.Namespace == "" {
			return cwValidation("Every selector requires a Namespace.")
		}
		if len(selector.MetricNames) > cwOTelMaxMetricNames {
			return cwValidation("A selector may name at most %d metrics.", cwOTelMaxMetricNames)
		}
	}
	return nil
}

func cwStartOTelEnrichment(include, exclude []CWOTelSelector) (CWOTelEnrichment, *cwFailure) {
	if failure := cwCheckOTelFilters(include, exclude); failure != nil {
		return CWOTelEnrichment{}, failure
	}
	state, now := cwOTel(), time.Now().UTC().Unix()
	if state.Status != cwOTelRunning {
		state.CreatedAt = now
	}
	state.Status, state.IncludeFilters, state.ExcludeFilters, state.UpdatedAt = cwOTelRunning, include, exclude, now
	cwOTelEnrichmentState.Put(cwOTelEnrichmentKey, state)
	return state, nil
}

func cwStopOTelEnrichment() {
	cwOTelEnrichmentState.Put(cwOTelEnrichmentKey, CWOTelEnrichment{Status: cwOTelStopped})
}

// cwUpdateOTelEnrichment replaces both filter lists as a pair; enrichment must
// already be running.
func cwUpdateOTelEnrichment(include, exclude []CWOTelSelector) (CWOTelEnrichment, *cwFailure) {
	state := cwOTel()
	if state.Status != cwOTelRunning {
		return state, cwFail("ResourceNotFoundException", http.StatusNotFound,
			"OTel enrichment is not running for this account. Start it with StartOTelEnrichment.")
	}
	if failure := cwCheckOTelFilters(include, exclude); failure != nil {
		return state, failure
	}
	state.IncludeFilters, state.ExcludeFilters, state.UpdatedAt = include, exclude, time.Now().UTC().Unix()
	cwOTelEnrichmentState.Put(cwOTelEnrichmentKey, state)
	return state, nil
}

// ── awsJson1.0 surface ─────────────────────────────────────────────────────

func cwJSONFail(w http.ResponseWriter, failure *cwFailure) {
	AWSError(w, failure.code, failure.message, failure.status)
}

func cwJSONResourceMetrics(c CWResourceMetricsConfiguration) map[string]any {
	out := map[string]any{"ResourceArn": c.ResourceArn, "CreatedAt": float64(c.CreatedAt), "UpdatedAt": float64(c.UpdatedAt)}
	if c.IncludeMetrics != nil {
		out["MetricSelections"] = []cwMetricSelection{{IncludeMetrics: c.IncludeMetrics}}
	}
	return map[string]any{"ResourceMetricsConfiguration": out}
}

func cwJSONOTel(s CWOTelEnrichment, withStatus bool) map[string]any {
	out := map[string]any{}
	if withStatus {
		out["Status"] = s.Status
	}
	if s.Status == cwOTelRunning {
		out["IncludeFilters"] = cwSelectorsOrEmpty(s.IncludeFilters)
		out["ExcludeFilters"] = cwSelectorsOrEmpty(s.ExcludeFilters)
		out["CreatedAt"] = float64(s.CreatedAt)
		out["UpdatedAt"] = float64(s.UpdatedAt)
	}
	return out
}

func cwSelectorsOrEmpty(s []CWOTelSelector) []CWOTelSelector {
	if s == nil {
		return []CWOTelSelector{}
	}
	return s
}

type cwJSONResourceMetricsRequest struct {
	ResourceArn      string              `json:"ResourceArn"`
	MetricSelections []cwMetricSelection `json:"MetricSelections"`
}

type cwJSONOTelRequest struct {
	IncludeFilters []CWOTelSelector `json:"IncludeFilters"`
	ExcludeFilters []CWOTelSelector `json:"ExcludeFilters"`
}

func cwJSONRead(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := sim.ReadJSON(r, v); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func handleCWJSONCreateResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwJSONResourceMetricsRequest
	if !cwJSONRead(w, r, &req) {
		return
	}
	config, failure := cwCreateResourceMetricsConfiguration(req.ResourceArn, req.MetricSelections)
	if failure != nil {
		cwJSONFail(w, failure)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cwJSONResourceMetrics(config))
}

func handleCWJSONGetResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwJSONResourceMetricsRequest
	if !cwJSONRead(w, r, &req) {
		return
	}
	config, failure := cwExistingResourceMetricsConfiguration(req.ResourceArn)
	if failure != nil {
		cwJSONFail(w, failure)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cwJSONResourceMetrics(config))
}

func handleCWJSONUpdateResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwJSONResourceMetricsRequest
	if !cwJSONRead(w, r, &req) {
		return
	}
	config, failure := cwUpdateResourceMetricsConfiguration(req.ResourceArn, req.MetricSelections)
	if failure != nil {
		cwJSONFail(w, failure)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cwJSONResourceMetrics(config))
}

func handleCWJSONDeleteResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwJSONResourceMetricsRequest
	if !cwJSONRead(w, r, &req) {
		return
	}
	if failure := cwDeleteResourceMetricsConfiguration(req.ResourceArn); failure != nil {
		cwJSONFail(w, failure)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONGetOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, cwJSONOTel(cwOTel(), true))
}

func handleCWJSONStartOTelEnrichment(w http.ResponseWriter, r *http.Request) {
	var req cwJSONOTelRequest
	if !cwJSONRead(w, r, &req) {
		return
	}
	state, failure := cwStartOTelEnrichment(req.IncludeFilters, req.ExcludeFilters)
	if failure != nil {
		cwJSONFail(w, failure)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cwJSONOTel(state, false))
}

func handleCWJSONStopOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwStopOTelEnrichment()
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONUpdateOTelEnrichment(w http.ResponseWriter, r *http.Request) {
	var req cwJSONOTelRequest
	if !cwJSONRead(w, r, &req) {
		return
	}
	state, failure := cwUpdateOTelEnrichment(req.IncludeFilters, req.ExcludeFilters)
	if failure != nil {
		cwJSONFail(w, failure)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cwJSONOTel(state, false))
}

// ── rpc-v2-cbor surface ────────────────────────────────────────────────────

func cwCBORFail(w http.ResponseWriter, failure *cwFailure) {
	cwWriteCBORError(w, failure.code, failure.message, failure.status)
}

func cwCBORResourceMetrics(c CWResourceMetricsConfiguration) map[string]any {
	out := map[string]any{
		"ResourceArn": c.ResourceArn,
		"CreatedAt":   time.Unix(c.CreatedAt, 0).UTC(),
		"UpdatedAt":   time.Unix(c.UpdatedAt, 0).UTC(),
	}
	if c.IncludeMetrics != nil {
		out["MetricSelections"] = []cwMetricSelection{{IncludeMetrics: c.IncludeMetrics}}
	}
	return map[string]any{"ResourceMetricsConfiguration": out}
}

func cwCBOROTel(s CWOTelEnrichment, withStatus bool) map[string]any {
	out := map[string]any{}
	if withStatus {
		out["Status"] = s.Status
	}
	if s.Status == cwOTelRunning {
		out["IncludeFilters"] = cwSelectorsOrEmpty(s.IncludeFilters)
		out["ExcludeFilters"] = cwSelectorsOrEmpty(s.ExcludeFilters)
		out["CreatedAt"] = time.Unix(s.CreatedAt, 0).UTC()
		out["UpdatedAt"] = time.Unix(s.UpdatedAt, 0).UTC()
	}
	return out
}

type cwCBORResourceMetricsRequest struct {
	ResourceArn      string              `cbor:"ResourceArn"`
	MetricSelections []cwMetricSelection `cbor:"MetricSelections"`
}

type cwCBOROTelRequest struct {
	IncludeFilters []CWOTelSelector `cbor:"IncludeFilters"`
	ExcludeFilters []CWOTelSelector `cbor:"ExcludeFilters"`
}

func handleCWCBORCreateResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwCBORResourceMetricsRequest
	if !cwReadCBOR(w, r, &req) {
		return
	}
	config, failure := cwCreateResourceMetricsConfiguration(req.ResourceArn, req.MetricSelections)
	if failure != nil {
		cwCBORFail(w, failure)
		return
	}
	cwWriteCBOR(w, cwCBORResourceMetrics(config))
}

func handleCWCBORGetResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwCBORResourceMetricsRequest
	if !cwReadCBOR(w, r, &req) {
		return
	}
	config, failure := cwExistingResourceMetricsConfiguration(req.ResourceArn)
	if failure != nil {
		cwCBORFail(w, failure)
		return
	}
	cwWriteCBOR(w, cwCBORResourceMetrics(config))
}

func handleCWCBORUpdateResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwCBORResourceMetricsRequest
	if !cwReadCBOR(w, r, &req) {
		return
	}
	config, failure := cwUpdateResourceMetricsConfiguration(req.ResourceArn, req.MetricSelections)
	if failure != nil {
		cwCBORFail(w, failure)
		return
	}
	cwWriteCBOR(w, cwCBORResourceMetrics(config))
}

func handleCWCBORDeleteResourceMetricsConfiguration(w http.ResponseWriter, r *http.Request) {
	var req cwCBORResourceMetricsRequest
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if failure := cwDeleteResourceMetricsConfiguration(req.ResourceArn); failure != nil {
		cwCBORFail(w, failure)
		return
	}
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORGetOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwWriteCBOR(w, cwCBOROTel(cwOTel(), true))
}

func handleCWCBORStartOTelEnrichment(w http.ResponseWriter, r *http.Request) {
	var req cwCBOROTelRequest
	if !cwReadCBOR(w, r, &req) {
		return
	}
	state, failure := cwStartOTelEnrichment(req.IncludeFilters, req.ExcludeFilters)
	if failure != nil {
		cwCBORFail(w, failure)
		return
	}
	cwWriteCBOR(w, cwCBOROTel(state, false))
}

func handleCWCBORStopOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwStopOTelEnrichment()
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORUpdateOTelEnrichment(w http.ResponseWriter, r *http.Request) {
	var req cwCBOROTelRequest
	if !cwReadCBOR(w, r, &req) {
		return
	}
	state, failure := cwUpdateOTelEnrichment(req.IncludeFilters, req.ExcludeFilters)
	if failure != nil {
		cwCBORFail(w, failure)
		return
	}
	cwWriteCBOR(w, cwCBOROTel(state, false))
}
