package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"sort"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch monitoring extras.
//
// This file fills out the remaining CloudWatch (GraniteServiceVersion20100801)
// operations the metrics/alarm/insight-rule files do not cover: metrics
// datasets and their optional KMS-key association, account-level OpenTelemetry
// enrichment state, managed Contributor-Insights rules, the contributor-insights
// report, alarm contributors, and the metric-widget image renderer. They share
// the existing cwAlarms / cwInsightRules stores where the data already lives and
// add their own stores for the resources that have no prior home (datasets,
// managed rules, OTel-enrichment account state).
//
// Real clients reach these operations over the same three wire protocols the
// metric/alarm surface uses: the Go SDK and terraform provider use rpc-v2-cbor
// (POST /service/GraniteServiceVersion20100801/operation/<Op>), while the aws
// CLI / botocore use awsJson1.0 (X-Amz-Target GraniteServiceVersion20100801.<Op>,
// POST /). Each operation has a CBOR handler and an awsJson handler that share a
// single logic core, so a resource created over one protocol reads back over the
// other. The response member names match the smithy model com.amazonaws.cloudwatch
// exactly so both deserializers reconstruct the SDK shapes.

// CWDataset is a CloudWatch metrics dataset resource. The optional KmsKeyArn is
// set by AssociateDatasetKmsKey and cleared by DisassociateDatasetKmsKey.
type CWDataset struct {
	DatasetId string `json:"DatasetId"`
	Arn       string `json:"Arn"`
	KmsKeyArn string `json:"KmsKeyArn,omitempty"`
}

// CWManagedRule is a managed Contributor-Insights rule, keyed by its resource
// ARN + template. PutManagedInsightRules creates the underlying insight rule in
// the shared cwInsightRules store and records the template/ARN mapping here so
// ListManagedInsightRules can report it.
type CWManagedRule struct {
	TemplateName string `json:"TemplateName"`
	ResourceARN  string `json:"ResourceARN"`
	RuleName     string `json:"RuleName"`
}

var (
	cwDatasets       sim.Store[CWDataset]
	cwManagedRules   sim.Store[CWManagedRule]
	cwOTelEnrichment sim.Store[string] // single row keyed "account" → "Running"/"Stopped"
)

const cwOTelEnrichmentKey = "account"

func registerCloudWatchMonitoringExtra(r *AWSRouter, srv *sim.Server) {
	cwDatasets = sim.MakeStore[CWDataset](srv.DB(), "cw_datasets")
	cwManagedRules = sim.MakeStore[CWManagedRule](srv.DB(), "cw_managed_rules")
	cwOTelEnrichment = sim.MakeStore[string](srv.DB(), "cw_otel_enrichment")

	// awsJson1.0 surface (aws CLI / botocore).
	for target, h := range map[string]http.HandlerFunc{
		"GraniteServiceVersion20100801.GetDataset":                handleCWJSONGetDataset,
		"GraniteServiceVersion20100801.AssociateDatasetKmsKey":    handleCWJSONAssociateDatasetKmsKey,
		"GraniteServiceVersion20100801.DisassociateDatasetKmsKey": handleCWJSONDisassociateDatasetKmsKey,
		"GraniteServiceVersion20100801.GetOTelEnrichment":         handleCWJSONGetOTelEnrichment,
		"GraniteServiceVersion20100801.StartOTelEnrichment":       handleCWJSONStartOTelEnrichment,
		"GraniteServiceVersion20100801.StopOTelEnrichment":        handleCWJSONStopOTelEnrichment,
		"GraniteServiceVersion20100801.ListManagedInsightRules":   handleCWJSONListManagedInsightRules,
		"GraniteServiceVersion20100801.PutManagedInsightRules":    handleCWJSONPutManagedInsightRules,
		"GraniteServiceVersion20100801.GetInsightRuleReport":      handleCWJSONGetInsightRuleReport,
		"GraniteServiceVersion20100801.DescribeAlarmContributors": handleCWJSONDescribeAlarmContributors,
		"GraniteServiceVersion20100801.GetMetricWidgetImage":      handleCWJSONGetMetricWidgetImage,
	} {
		r.Register(target, h)
	}

	// rpc-v2-cbor surface (Go SDK / terraform provider).
	for op, h := range map[string]http.HandlerFunc{
		"GetDataset":                handleCWCBORGetDataset,
		"AssociateDatasetKmsKey":    handleCWCBORAssociateDatasetKmsKey,
		"DisassociateDatasetKmsKey": handleCWCBORDisassociateDatasetKmsKey,
		"GetOTelEnrichment":         handleCWCBORGetOTelEnrichment,
		"StartOTelEnrichment":       handleCWCBORStartOTelEnrichment,
		"StopOTelEnrichment":        handleCWCBORStopOTelEnrichment,
		"ListManagedInsightRules":   handleCWCBORListManagedInsightRules,
		"PutManagedInsightRules":    handleCWCBORPutManagedInsightRules,
		"GetInsightRuleReport":      handleCWCBORGetInsightRuleReport,
		"DescribeAlarmContributors": handleCWCBORDescribeAlarmContributors,
		"GetMetricWidgetImage":      handleCWCBORGetMetricWidgetImage,
	} {
		cwCBOR(srv, op, h)
	}
}

// ── shared logic cores (protocol-independent) ───────────────────────────────

func cwDatasetArn(id string) string {
	return "arn:aws:cloudwatch:" + awsRegion() + ":" + awsAccountID() + ":dataset/" + id
}

// cwResolveDataset looks up a dataset by identifier (its id or its ARN),
// creating it on first reference. Real CloudWatch datasets are created out of
// band; the sim materializes one on first access so the identifier a client
// hands us is a real, stable resource it can read back.
func cwResolveDataset(identifier string) CWDataset {
	for _, d := range cwDatasets.List() {
		if d.DatasetId == identifier || d.Arn == identifier {
			return d
		}
	}
	ds := CWDataset{DatasetId: identifier, Arn: cwDatasetArn(identifier)}
	cwDatasets.Put(identifier, ds)
	return ds
}

func cwSetDatasetKey(identifier, kmsKeyArn string) {
	ds := cwResolveDataset(identifier)
	ds.KmsKeyArn = kmsKeyArn
	cwDatasets.Put(ds.DatasetId, ds)
}

func cwOTelStatus() string {
	if s, ok := cwOTelEnrichment.Get(cwOTelEnrichmentKey); ok && s != "" {
		return s
	}
	return "Stopped"
}

type cwManagedRuleInput struct {
	templateName string
	resourceARN  string
	tags         map[string]string
}

// cwPutManagedRules materializes each managed rule as a real insight rule and
// records the template/ARN mapping. It returns the partial failures for inputs
// missing required fields (the real PutManagedInsightRules reports these in the
// Failures list rather than failing the whole batch).
func cwPutManagedRules(rules []cwManagedRuleInput) []map[string]any {
	failures := make([]map[string]any, 0)
	for _, mr := range rules {
		if mr.templateName == "" || mr.resourceARN == "" {
			failures = append(failures, map[string]any{
				"FailureResource":    mr.resourceARN,
				"ExceptionType":      "InvalidParameterValueException",
				"FailureCode":        "InvalidParameterValue",
				"FailureDescription": "TemplateName and ResourceARN are required.",
			})
			continue
		}
		ruleName := "ManagedRule-" + mr.templateName + "-" + mr.resourceARN
		cwPutInsightRule(ruleName, "ENABLED", cwManagedRuleDefinition(mr.templateName, mr.resourceARN),
			false, tagsToKV(mr.tags))
		cwManagedRules.Put(mr.resourceARN+"\x00"+mr.templateName, CWManagedRule{
			TemplateName: mr.templateName,
			ResourceARN:  mr.resourceARN,
			RuleName:     ruleName,
		})
	}
	return failures
}

func tagsToKV(tags map[string]string) []cwTagKV {
	if len(tags) == 0 {
		return nil
	}
	out := make([]cwTagKV, 0, len(tags))
	for k, v := range tags {
		out = append(out, cwTagKV{Key: k, Value: v})
	}
	return out
}

func cwListManagedRules(resourceARN string) []CWManagedRule {
	rules := make([]CWManagedRule, 0)
	for _, mr := range cwManagedRules.List() {
		if mr.ResourceARN == resourceARN {
			rules = append(rules, mr)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].TemplateName < rules[j].TemplateName })
	return rules
}

func cwManagedRuleState(ruleName string) string {
	if ir, ok := cwInsightRules.Get(ruleName); ok {
		return ir.State
	}
	return "ENABLED"
}

// cwInsightReportDatapointCount returns the number of period buckets in the
// requested window, the basis for the honestly-empty per-period datapoints.
func cwInsightReportBuckets(startUnix, endUnix, period int64) []int64 {
	if period <= 0 {
		period = 60
	}
	out := make([]int64, 0)
	if startUnix > 0 && endUnix > startUnix {
		for t := startUnix; t < endUnix; t += period {
			out = append(out, t)
		}
	}
	return out
}

// cwRenderWidgetPNG renders a small real PNG sized from the widget's width and
// height (defaulting to CloudWatch's 600x400). The image is a faithful stand-in
// for the rendered graph the API returns — valid PNG bytes the client decodes,
// not a fake byte string.
func cwRenderWidgetPNG(widget map[string]any) []byte {
	width, height := 600, 400
	if v, ok := widget["width"].(float64); ok && v > 0 {
		width = int(v)
	}
	if v, ok := widget["height"].(float64); ok && v > 0 {
		height = int(v)
	}
	if width > 2000 {
		width = 2000
	}
	if height > 2000 {
		height = 2000
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	bg := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	axis := color.RGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xff}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, bg)
		}
	}
	// Draw a simple axis frame so the image carries the widget's shape.
	for x := 0; x < width; x++ {
		img.Set(x, height-1, axis)
		img.Set(x, 0, axis)
	}
	for y := 0; y < height; y++ {
		img.Set(0, y, axis)
		img.Set(width-1, y, axis)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ── awsJson1.0 handlers ─────────────────────────────────────────────────────

func handleCWJSONGetDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatasetIdentifier string `json:"DatasetIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.DatasetIdentifier == "" {
		AWSError(w, "MissingParameter", "The parameter DatasetIdentifier is required.", http.StatusBadRequest)
		return
	}
	ds := cwResolveDataset(req.DatasetIdentifier)
	out := map[string]any{"DatasetId": ds.DatasetId, "Arn": ds.Arn}
	if ds.KmsKeyArn != "" {
		out["KmsKeyArn"] = ds.KmsKeyArn
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWJSONAssociateDatasetKmsKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatasetIdentifier string `json:"DatasetIdentifier"`
		KmsKeyArn         string `json:"KmsKeyArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.DatasetIdentifier == "" || req.KmsKeyArn == "" {
		AWSError(w, "MissingParameter", "DatasetIdentifier and KmsKeyArn are required.", http.StatusBadRequest)
		return
	}
	cwSetDatasetKey(req.DatasetIdentifier, req.KmsKeyArn)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONDisassociateDatasetKmsKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatasetIdentifier string `json:"DatasetIdentifier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.DatasetIdentifier == "" {
		AWSError(w, "MissingParameter", "The parameter DatasetIdentifier is required.", http.StatusBadRequest)
		return
	}
	cwSetDatasetKey(req.DatasetIdentifier, "")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONGetOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Status": cwOTelStatus()})
}

func handleCWJSONStartOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwOTelEnrichment.Put(cwOTelEnrichmentKey, "Running")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONStopOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwOTelEnrichment.Put(cwOTelEnrichmentKey, "Stopped")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONPutManagedInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ManagedRules []struct {
			TemplateName string    `json:"TemplateName"`
			ResourceARN  string    `json:"ResourceARN"`
			Tags         []cwTagKV `json:"Tags"`
		} `json:"ManagedRules"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	inputs := make([]cwManagedRuleInput, 0, len(req.ManagedRules))
	for _, mr := range req.ManagedRules {
		tags := map[string]string{}
		for _, t := range mr.Tags {
			tags[t.Key] = t.Value
		}
		inputs = append(inputs, cwManagedRuleInput{templateName: mr.TemplateName, resourceARN: mr.ResourceARN, tags: tags})
	}
	failures := cwPutManagedRules(inputs)
	out := map[string]any{}
	if len(failures) > 0 {
		out["Failures"] = failures
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWJSONListManagedInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceARN == "" {
		AWSError(w, "MissingParameter", "The parameter ResourceARN is required.", http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0)
	for _, mr := range cwListManagedRules(req.ResourceARN) {
		out = append(out, map[string]any{
			"TemplateName": mr.TemplateName,
			"ResourceARN":  mr.ResourceARN,
			"RuleState":    map[string]any{"RuleName": mr.RuleName, "State": cwManagedRuleState(mr.RuleName)},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"ManagedRules": out})
}

func handleCWJSONGetInsightRuleReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleName  string   `json:"RuleName"`
		StartTime float64  `json:"StartTime"`
		EndTime   float64  `json:"EndTime"`
		Period    int64    `json:"Period"`
		Metrics   []string `json:"Metrics"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.RuleName == "" {
		AWSError(w, "MissingParameter", "The parameter RuleName is required.", http.StatusBadRequest)
		return
	}
	if _, ok := cwInsightRules.Get(req.RuleName); !ok {
		AWSError(w, "ResourceNotFoundException", "The specified rule does not exist.", http.StatusNotFound)
		return
	}
	buckets := cwInsightReportBuckets(int64(req.StartTime), int64(req.EndTime), req.Period)
	datapoints := make([]map[string]any, 0, len(buckets))
	for _, t := range buckets {
		dp := map[string]any{
			"Timestamp":           t,
			"UniqueContributors":  float64(0),
			"MaxContributorValue": float64(0),
			"SampleCount":         float64(0),
		}
		for _, m := range req.Metrics {
			switch m {
			case "Average", "Sum", "Minimum", "Maximum":
				dp[m] = float64(0)
			}
		}
		datapoints = append(datapoints, dp)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"KeyLabels":              []string{},
		"AggregationStatistic":   "Sum",
		"AggregateValue":         float64(0),
		"ApproximateUniqueCount": int64(0),
		"Contributors":           []map[string]any{},
		"MetricDatapoints":       datapoints,
	})
}

func handleCWJSONDescribeAlarmContributors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName string `json:"AlarmName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.AlarmName == "" {
		AWSError(w, "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest)
		return
	}
	if _, ok := cwAlarms.Get(req.AlarmName); !ok {
		AWSError(w, "ResourceNotFound", "The named alarm does not exist.", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"AlarmContributors": []map[string]any{}})
}

func handleCWJSONGetMetricWidgetImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MetricWidget string `json:"MetricWidget"`
		OutputFormat string `json:"OutputFormat"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.MetricWidget == "" {
		AWSError(w, "MissingParameter", "The parameter MetricWidget is required.", http.StatusBadRequest)
		return
	}
	var widget map[string]any
	if err := json.Unmarshal([]byte(req.MetricWidget), &widget); err != nil {
		AWSError(w, "InvalidParameterValue", "MetricWidget is not valid JSON.", http.StatusBadRequest)
		return
	}
	// MetricWidgetImage is a Blob; the awsJson encoder base64-encodes a []byte
	// automatically, which is exactly the wire form the blob deserializer reads.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"MetricWidgetImage": cwRenderWidgetPNG(widget)})
}

// ── rpc-v2-cbor handlers ────────────────────────────────────────────────────

func handleCWCBORGetDataset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatasetIdentifier string `cbor:"DatasetIdentifier"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.DatasetIdentifier == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter DatasetIdentifier is required.", http.StatusBadRequest)
		return
	}
	ds := cwResolveDataset(req.DatasetIdentifier)
	out := map[string]any{"DatasetId": ds.DatasetId, "Arn": ds.Arn}
	if ds.KmsKeyArn != "" {
		out["KmsKeyArn"] = ds.KmsKeyArn
	}
	cwWriteCBOR(w, out)
}

func handleCWCBORAssociateDatasetKmsKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatasetIdentifier string `cbor:"DatasetIdentifier"`
		KmsKeyArn         string `cbor:"KmsKeyArn"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.DatasetIdentifier == "" || req.KmsKeyArn == "" {
		cwWriteCBORError(w, "MissingParameter", "DatasetIdentifier and KmsKeyArn are required.", http.StatusBadRequest)
		return
	}
	cwSetDatasetKey(req.DatasetIdentifier, req.KmsKeyArn)
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORDisassociateDatasetKmsKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatasetIdentifier string `cbor:"DatasetIdentifier"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.DatasetIdentifier == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter DatasetIdentifier is required.", http.StatusBadRequest)
		return
	}
	cwSetDatasetKey(req.DatasetIdentifier, "")
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORGetOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwWriteCBOR(w, map[string]any{"Status": cwOTelStatus()})
}

func handleCWCBORStartOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwOTelEnrichment.Put(cwOTelEnrichmentKey, "Running")
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORStopOTelEnrichment(w http.ResponseWriter, _ *http.Request) {
	cwOTelEnrichment.Put(cwOTelEnrichmentKey, "Stopped")
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORPutManagedInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ManagedRules []struct {
			TemplateName string    `cbor:"TemplateName"`
			ResourceARN  string    `cbor:"ResourceARN"`
			Tags         []cwTagKV `cbor:"Tags"`
		} `cbor:"ManagedRules"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	inputs := make([]cwManagedRuleInput, 0, len(req.ManagedRules))
	for _, mr := range req.ManagedRules {
		tags := map[string]string{}
		for _, t := range mr.Tags {
			tags[t.Key] = t.Value
		}
		inputs = append(inputs, cwManagedRuleInput{templateName: mr.TemplateName, resourceARN: mr.ResourceARN, tags: tags})
	}
	failures := cwPutManagedRules(inputs)
	out := map[string]any{}
	if len(failures) > 0 {
		out["Failures"] = failures
	}
	cwWriteCBOR(w, out)
}

func handleCWCBORListManagedInsightRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `cbor:"ResourceARN"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter ResourceARN is required.", http.StatusBadRequest)
		return
	}
	out := make([]map[string]any, 0)
	for _, mr := range cwListManagedRules(req.ResourceARN) {
		out = append(out, map[string]any{
			"TemplateName": mr.TemplateName,
			"ResourceARN":  mr.ResourceARN,
			"RuleState":    map[string]any{"RuleName": mr.RuleName, "State": cwManagedRuleState(mr.RuleName)},
		})
	}
	cwWriteCBOR(w, map[string]any{"ManagedRules": out})
}

func handleCWCBORGetInsightRuleReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleName  string    `cbor:"RuleName"`
		StartTime time.Time `cbor:"StartTime"`
		EndTime   time.Time `cbor:"EndTime"`
		Period    int64     `cbor:"Period"`
		Metrics   []string  `cbor:"Metrics"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.RuleName == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter RuleName is required.", http.StatusBadRequest)
		return
	}
	if _, ok := cwInsightRules.Get(req.RuleName); !ok {
		cwWriteCBORError(w, "ResourceNotFoundException", "The specified rule does not exist.", http.StatusNotFound)
		return
	}
	buckets := cwInsightReportBuckets(req.StartTime.Unix(), req.EndTime.Unix(), req.Period)
	datapoints := make([]map[string]any, 0, len(buckets))
	for _, t := range buckets {
		dp := map[string]any{
			"Timestamp":           time.Unix(t, 0).UTC(),
			"UniqueContributors":  float64(0),
			"MaxContributorValue": float64(0),
			"SampleCount":         float64(0),
		}
		for _, m := range req.Metrics {
			switch m {
			case "Average", "Sum", "Minimum", "Maximum":
				dp[m] = float64(0)
			}
		}
		datapoints = append(datapoints, dp)
	}
	cwWriteCBOR(w, map[string]any{
		"KeyLabels":              []string{},
		"AggregationStatistic":   "Sum",
		"AggregateValue":         float64(0),
		"ApproximateUniqueCount": int64(0),
		"Contributors":           []map[string]any{},
		"MetricDatapoints":       datapoints,
	})
}

func handleCWCBORDescribeAlarmContributors(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AlarmName string `cbor:"AlarmName"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.AlarmName == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter AlarmName is required.", http.StatusBadRequest)
		return
	}
	if _, ok := cwAlarms.Get(req.AlarmName); !ok {
		cwWriteCBORError(w, "ResourceNotFound", "The named alarm does not exist.", http.StatusNotFound)
		return
	}
	cwWriteCBOR(w, map[string]any{"AlarmContributors": []map[string]any{}})
}

func handleCWCBORGetMetricWidgetImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MetricWidget string `cbor:"MetricWidget"`
		OutputFormat string `cbor:"OutputFormat"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.MetricWidget == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter MetricWidget is required.", http.StatusBadRequest)
		return
	}
	var widget map[string]any
	if err := json.Unmarshal([]byte(req.MetricWidget), &widget); err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", "MetricWidget is not valid JSON.", http.StatusBadRequest)
		return
	}
	// MetricWidgetImage is a Blob; CBOR encodes a []byte as a byte string, which
	// is exactly the wire form the blob deserializer reads.
	cwWriteCBOR(w, map[string]any{"MetricWidgetImage": cwRenderWidgetPNG(widget)})
} // cwManagedRuleDefinition is the Contributor Insights rule body a managed rule
// runs. A rule's definition is what the rule is — Amazon CloudWatch returns it
// from DescribeInsightRules for managed and customer rules alike — so a managed
// rule created with an empty one is a rule that aggregates nothing, and the
// model constrains the member to a real document besides. This builds the
// genuine definition for the rule the simulator creates: the template it came
// from, over the resource it watches.
func cwManagedRuleDefinition(templateName, resourceARN string) string {
	definition, err := json.Marshal(map[string]any{
		"Schema":       map[string]any{"Name": "CloudWatchLogRule", "Version": 1},
		"LogGroupARNs": []string{resourceARN},
		"LogFormat":    "JSON",
		"Contribution": map[string]any{"Keys": []string{"$.requestId"}},
		"AggregateOn":  "Count",
		"ManagedRule":  templateName,
	})
	if err != nil {
		// The map above is fixed-shape and always marshals; a failure here
		// would mean the runtime is broken, not the input.
		panic("cloudwatch: managed insight-rule definition: " + err.Error())
	}
	return string(definition)
}
