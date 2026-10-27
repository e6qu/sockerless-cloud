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

// Each operation has a CBOR and an awsJson handler over one logic core, with
// response member names exactly as the com.amazonaws.cloudwatch model.

type CWDataset struct {
	DatasetId string            `json:"DatasetId"`
	Arn       string            `json:"Arn"`
	KmsKeyArn string            `json:"KmsKeyArn,omitempty"`
	Tags      map[string]string `json:"Tags,omitempty"`
}

// The one dataset CloudWatch supports; every account has it in every Region.
const cwDefaultDataset = "default"

// PutManagedInsightRules creates the insight rule in cwInsightRules and
// records the template and ARN here for ListManagedInsightRules.
type CWManagedRule struct {
	TemplateName string `json:"TemplateName"`
	ResourceARN  string `json:"ResourceARN"`
	RuleName     string `json:"RuleName"`
}

var (
	cwDatasets     sim.Store[CWDataset]
	cwManagedRules sim.Store[CWManagedRule]
)

const cwOTelEnrichmentKey = "account"

func registerCloudWatchMonitoringExtra(r *AWSRouter, srv *sim.Server) {
	cwDatasets = sim.MakeStore[CWDataset](srv.DB(), "cw_datasets")
	cwManagedRules = sim.MakeStore[CWManagedRule](srv.DB(), "cw_managed_rules")
	registerCloudWatchResourceMetrics(r, srv)

	for target, h := range map[string]http.HandlerFunc{
		"GraniteServiceVersion20100801.GetDataset":                handleCWJSONGetDataset,
		"GraniteServiceVersion20100801.AssociateDatasetKmsKey":    handleCWJSONAssociateDatasetKmsKey,
		"GraniteServiceVersion20100801.DisassociateDatasetKmsKey": handleCWJSONDisassociateDatasetKmsKey,
		"GraniteServiceVersion20100801.ListManagedInsightRules":   handleCWJSONListManagedInsightRules,
		"GraniteServiceVersion20100801.PutManagedInsightRules":    handleCWJSONPutManagedInsightRules,
		"GraniteServiceVersion20100801.GetInsightRuleReport":      handleCWJSONGetInsightRuleReport,
		"GraniteServiceVersion20100801.DescribeAlarmContributors": handleCWJSONDescribeAlarmContributors,
		"GraniteServiceVersion20100801.GetMetricWidgetImage":      handleCWJSONGetMetricWidgetImage,
	} {
		r.Register(target, h)
	}

	for op, h := range map[string]http.HandlerFunc{
		"GetDataset":                handleCWCBORGetDataset,
		"AssociateDatasetKmsKey":    handleCWCBORAssociateDatasetKmsKey,
		"DisassociateDatasetKmsKey": handleCWCBORDisassociateDatasetKmsKey,
		"ListManagedInsightRules":   handleCWCBORListManagedInsightRules,
		"PutManagedInsightRules":    handleCWCBORPutManagedInsightRules,
		"GetInsightRuleReport":      handleCWCBORGetInsightRuleReport,
		"DescribeAlarmContributors": handleCWCBORDescribeAlarmContributors,
		"GetMetricWidgetImage":      handleCWCBORGetMetricWidgetImage,
	} {
		cwCBOR(srv, op, h)
	}
}

func cwDatasetArn(id string) string {
	return "arn:aws:cloudwatch:" + awsRegion() + ":" + awsAccountID() + ":dataset/" + id
}

// Only the implicit default dataset exists; its stored record holds what a
// caller set.
func cwResolveDataset(identifier string) (CWDataset, bool) {
	if identifier != cwDefaultDataset && identifier != cwDatasetArn(cwDefaultDataset) {
		return CWDataset{}, false
	}
	if ds, ok := cwDatasets.Get(cwDefaultDataset); ok {
		return ds, true
	}
	return CWDataset{DatasetId: cwDefaultDataset, Arn: cwDatasetArn(cwDefaultDataset)}, true
}

func cwUpdateDataset(identifier string, mutate func(*CWDataset)) bool {
	ds, ok := cwResolveDataset(identifier)
	if !ok {
		return false
	}
	mutate(&ds)
	cwDatasets.Put(ds.DatasetId, ds)
	return true
}

func cwDatasetNotFound(identifier string) string {
	return "Dataset " + identifier + " does not exist."
}

type cwManagedRuleInput struct {
	templateName string
	resourceARN  string
	tags         map[string]string
}

// Inputs missing required fields go in Failures instead of failing the batch.
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

// The report's per-period datapoints are empty.
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

// Defaults to CloudWatch's 600x400.
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
	ds, ok := cwResolveDataset(req.DatasetIdentifier)
	if !ok {
		AWSError(w, "ResourceNotFoundException", cwDatasetNotFound(req.DatasetIdentifier), http.StatusNotFound)
		return
	}
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
	if !cwUpdateDataset(req.DatasetIdentifier, func(ds *CWDataset) { ds.KmsKeyArn = req.KmsKeyArn }) {
		AWSError(w, "ResourceNotFoundException", cwDatasetNotFound(req.DatasetIdentifier), http.StatusNotFound)
		return
	}
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
	if !cwUpdateDataset(req.DatasetIdentifier, func(ds *CWDataset) { ds.KmsKeyArn = "" }) {
		AWSError(w, "ResourceNotFoundException", cwDatasetNotFound(req.DatasetIdentifier), http.StatusNotFound)
		return
	}
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
	// The awsJson encoder base64-encodes a []byte, the blob wire form.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"MetricWidgetImage": cwRenderWidgetPNG(widget)})
}

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
	ds, ok := cwResolveDataset(req.DatasetIdentifier)
	if !ok {
		cwWriteCBORError(w, "ResourceNotFoundException", cwDatasetNotFound(req.DatasetIdentifier), http.StatusNotFound)
		return
	}
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
	if !cwUpdateDataset(req.DatasetIdentifier, func(ds *CWDataset) { ds.KmsKeyArn = req.KmsKeyArn }) {
		cwWriteCBORError(w, "ResourceNotFoundException", cwDatasetNotFound(req.DatasetIdentifier), http.StatusNotFound)
		return
	}
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
	if !cwUpdateDataset(req.DatasetIdentifier, func(ds *CWDataset) { ds.KmsKeyArn = "" }) {
		cwWriteCBORError(w, "ResourceNotFoundException", cwDatasetNotFound(req.DatasetIdentifier), http.StatusNotFound)
		return
	}
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
	// CBOR encodes a []byte as a byte string, the blob wire form.
	cwWriteCBOR(w, map[string]any{"MetricWidgetImage": cwRenderWidgetPNG(widget)})
}

// cwManagedRuleDefinition builds the rule body from the template and the
// watched resource: DescribeInsightRules returns it, and the model requires a
// real document.
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
		// The map is fixed-shape and always marshals.
		panic("cloudwatch: managed insight-rule definition: " + err.Error())
	}
	return string(definition)
}
