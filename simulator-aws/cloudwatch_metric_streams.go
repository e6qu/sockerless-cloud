package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch metric streams: a named stream resource (ARN, RUNNING/STOPPED
// state) that continuously delivers metrics to Amazon Data Firehose. Served on
// awsJson1.0, rpc-v2-cbor, and query.

// CWMetricStreamFilter is an include/exclude namespace filter on a stream.
type CWMetricStreamFilter struct {
	Namespace   string   `json:"Namespace" cbor:"Namespace"`
	MetricNames []string `json:"MetricNames,omitempty" cbor:"MetricNames,omitempty"`
}

// CWMetricStream holds a metric stream's configuration and state.
type CWMetricStream struct {
	Name           string                 `json:"Name"`
	Arn            string                 `json:"Arn"`
	FirehoseArn    string                 `json:"FirehoseArn"`
	RoleArn        string                 `json:"RoleArn"`
	OutputFormat   string                 `json:"OutputFormat"`
	State          string                 `json:"State"`
	IncludeFilters []CWMetricStreamFilter `json:"IncludeFilters,omitempty"`
	ExcludeFilters []CWMetricStreamFilter `json:"ExcludeFilters,omitempty"`
	CreationDate   int64                  `json:"-"`
	LastUpdateDate int64                  `json:"-"`
	Tags           map[string]string      `json:"-"`
}

var cwMetricStreams sim.Store[CWMetricStream]

func cwMetricStreamArn(name string) string {
	return "arn:aws:cloudwatch:" + awsRegion() + ":" + awsAccountID() + ":metric-stream/" + name
}

func cwMetricStreamByArn(arn string) (CWMetricStream, bool) {
	for _, s := range cwMetricStreams.List() {
		if s.Arn == arn {
			return s, true
		}
	}
	return CWMetricStream{}, false
}

func cwMetricStreamFilterMatches(filters []CWMetricStreamFilter, datum CWMetricDatum) bool {
	for _, filter := range filters {
		if filter.Namespace != datum.Namespace {
			continue
		}
		if len(filter.MetricNames) == 0 {
			return true
		}
		for _, name := range filter.MetricNames {
			if name == datum.MetricName {
				return true
			}
		}
	}
	return false
}

func cwMetricStreamAccepts(stream CWMetricStream, datum CWMetricDatum) bool {
	if len(stream.IncludeFilters) > 0 && !cwMetricStreamFilterMatches(stream.IncludeFilters, datum) {
		return false
	}
	return !cwMetricStreamFilterMatches(stream.ExcludeFilters, datum)
}

// cwDeliverMetricDatum renders the documented CloudWatch metric-stream JSON
// record and sends it through the configured Firehose stream. Every object is
// newline terminated because one Firehose record can carry multiple metric
// objects separated by newlines.
func cwDeliverMetricDatum(datum CWMetricDatum) {
	for _, stream := range cwMetricStreams.List() {
		if stream.State != "running" || !cwMetricStreamAccepts(stream, datum) {
			continue
		}
		if stream.OutputFormat != "json" {
			cwEvalLogger.Error().Str("metricStream", stream.Name).Str("outputFormat", stream.OutputFormat).
				Msg("CloudWatch metric stream output format cannot be delivered")
			continue
		}
		dimensions := map[string]string{}
		for _, dimension := range datum.Dimensions {
			dimensions[dimension.Name] = dimension.Value
		}
		unit := datum.Unit
		if unit == "" {
			unit = "None"
		}
		record, err := json.Marshal(map[string]any{
			"metric_stream_name": stream.Name,
			"account_id":         awsAccountID(),
			"region":             awsRegion(),
			"namespace":          datum.Namespace,
			"metric_name":        datum.MetricName,
			"dimensions":         dimensions,
			"timestamp":          int64(datum.Timestamp * 1000),
			"value": map[string]float64{
				"count": 1,
				"sum":   datum.Value,
				"max":   datum.Value,
				"min":   datum.Value,
			},
			"unit": unit,
		})
		if err != nil {
			cwEvalLogger.Error().Err(err).Str("metricStream", stream.Name).Msg("CloudWatch metric stream record encoding failed")
			continue
		}
		if err := iamValidateServiceRole(stream.RoleArn, "streams.metrics.cloudwatch.amazonaws.com", map[string]string{
			"firehose:PutRecord": stream.FirehoseArn,
		}); err != nil {
			cwEvalLogger.Error().Err(err).Str("metricStream", stream.Name).Str("firehoseARN", stream.FirehoseArn).
				Msg("CloudWatch cannot assume its Amazon Data Firehose delivery role")
			continue
		}
		record = append(record, '\n')
		if err := firehosePutServiceRecord(stream.FirehoseArn, record); err != nil {
			cwEvalLogger.Error().Err(err).Str("metricStream", stream.Name).Str("firehoseARN", stream.FirehoseArn).
				Msg("CloudWatch metric stream delivery failed")
		}
	}
}

// cwPutMetricStream creates or updates a stream, defaulting OutputFormat to JSON
// and starting in the RUNNING state (real PutMetricStream creates a running
// stream). Returns the stream's ARN.
func cwPutMetricStream(name, firehoseArn, roleArn, outputFormat string, include, exclude []CWMetricStreamFilter, tags []cwTagKV) (string, error) {
	if outputFormat == "" {
		outputFormat = "json"
	}
	if _, ok := firehoseStreamByARN(firehoseArn); !ok {
		return "", fmt.Errorf("delivery stream %q does not exist", firehoseArn)
	}
	if err := iamValidateServiceRole(roleArn, "streams.metrics.cloudwatch.amazonaws.com", map[string]string{
		"firehose:PutRecord": firehoseArn,
	}); err != nil {
		return "", err
	}
	if outputFormat != "json" {
		return "", fmt.Errorf("output format %q is not supported by the configured Firehose destination", outputFormat)
	}
	now := time.Now().UTC().Unix()
	existing, exists := cwMetricStreams.Get(name)
	s := CWMetricStream{
		Name:           name,
		Arn:            cwMetricStreamArn(name),
		FirehoseArn:    firehoseArn,
		RoleArn:        roleArn,
		OutputFormat:   outputFormat,
		State:          "running",
		IncludeFilters: include,
		ExcludeFilters: exclude,
		LastUpdateDate: now,
	}
	if exists {
		s.CreationDate = existing.CreationDate
		s.State = existing.State
		s.Tags = existing.Tags
	} else {
		s.CreationDate = now
	}
	if len(tags) > 0 {
		s.Tags = map[string]string{}
		for _, t := range tags {
			s.Tags[t.Key] = t.Value
		}
	}
	cwMetricStreams.Put(name, s)
	return s.Arn, nil
}

func cwSetMetricStreamState(names []string, state string) {
	for _, n := range names {
		cwMetricStreams.Update(n, func(s *CWMetricStream) {
			s.State = state
			s.LastUpdateDate = time.Now().UTC().Unix()
		})
	}
}

func cwListMetricStreams() []CWMetricStream {
	out := cwMetricStreams.List()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func cwJSONStreamFilters(f []CWMetricStreamFilter) []map[string]any {
	if len(f) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(f))
	for _, fl := range f {
		m := map[string]any{"Namespace": fl.Namespace}
		if len(fl.MetricNames) > 0 {
			m["MetricNames"] = fl.MetricNames
		}
		out = append(out, m)
	}
	return out
}

// ── awsJson1.0 surface ──────────────────────────────────────────────────────

func registerCloudWatchMetricStreamsJSON(r *AWSRouter) {
	r.Register("GraniteServiceVersion20100801.PutMetricStream", handleCWJSONPutMetricStream)
	r.Register("GraniteServiceVersion20100801.GetMetricStream", handleCWJSONGetMetricStream)
	r.Register("GraniteServiceVersion20100801.DeleteMetricStream", handleCWJSONDeleteMetricStream)
	r.Register("GraniteServiceVersion20100801.ListMetricStreams", handleCWJSONListMetricStreams)
	r.Register("GraniteServiceVersion20100801.StartMetricStreams", handleCWJSONStartMetricStreams)
	r.Register("GraniteServiceVersion20100801.StopMetricStreams", handleCWJSONStopMetricStreams)
}

func handleCWJSONPutMetricStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string                 `json:"Name"`
		FirehoseArn    string                 `json:"FirehoseArn"`
		RoleArn        string                 `json:"RoleArn"`
		OutputFormat   string                 `json:"OutputFormat"`
		IncludeFilters []CWMetricStreamFilter `json:"IncludeFilters"`
		ExcludeFilters []CWMetricStreamFilter `json:"ExcludeFilters"`
		Tags           []cwTagKV              `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "MissingParameter", "The parameter Name is required.", http.StatusBadRequest)
		return
	}
	if req.FirehoseArn == "" || req.RoleArn == "" {
		AWSError(w, "MissingParameter", "FirehoseArn and RoleArn are required.", http.StatusBadRequest)
		return
	}
	arn, err := cwPutMetricStream(req.Name, req.FirehoseArn, req.RoleArn, req.OutputFormat, req.IncludeFilters, req.ExcludeFilters, req.Tags)
	if err != nil {
		AWSError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Arn": arn})
}

func handleCWJSONGetMetricStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	s, ok := cwMetricStreams.Get(req.Name)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Metric stream %s does not exist", req.Name)
		return
	}
	out := map[string]any{
		"Arn":            s.Arn,
		"Name":           s.Name,
		"FirehoseArn":    s.FirehoseArn,
		"RoleArn":        s.RoleArn,
		"OutputFormat":   s.OutputFormat,
		"State":          s.State,
		"CreationDate":   float64(s.CreationDate),
		"LastUpdateDate": float64(s.LastUpdateDate),
	}
	if f := cwJSONStreamFilters(s.IncludeFilters); f != nil {
		out["IncludeFilters"] = f
	}
	if f := cwJSONStreamFilters(s.ExcludeFilters); f != nil {
		out["ExcludeFilters"] = f
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWJSONDeleteMetricStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	// DeleteMetricStream is idempotent — deleting an absent stream succeeds.
	cwMetricStreams.Delete(req.Name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONListMetricStreams(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	entries := make([]map[string]any, 0)
	for _, s := range cwListMetricStreams() {
		entries = append(entries, map[string]any{
			"Arn":            s.Arn,
			"Name":           s.Name,
			"FirehoseArn":    s.FirehoseArn,
			"State":          s.State,
			"OutputFormat":   s.OutputFormat,
			"CreationDate":   float64(s.CreationDate),
			"LastUpdateDate": float64(s.LastUpdateDate),
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Entries": entries})
}

func handleCWJSONStartMetricStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"Names"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	cwSetMetricStreamState(req.Names, "running")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWJSONStopMetricStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"Names"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "invalid request body", http.StatusBadRequest)
		return
	}
	cwSetMetricStreamState(req.Names, "stopped")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ── rpc-v2-cbor surface (Go SDK) ────────────────────────────────────────────

func registerCloudWatchMetricStreamsCBOR(srv *sim.Server) {
	cwCBOR(srv, "PutMetricStream", handleCWCBORPutMetricStream)
	cwCBOR(srv, "GetMetricStream", handleCWCBORGetMetricStream)
	cwCBOR(srv, "DeleteMetricStream", handleCWCBORDeleteMetricStream)
	cwCBOR(srv, "ListMetricStreams", handleCWCBORListMetricStreams)
	cwCBOR(srv, "StartMetricStreams", handleCWCBORStartMetricStreams)
	cwCBOR(srv, "StopMetricStreams", handleCWCBORStopMetricStreams)
}

func handleCWCBORPutMetricStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           string                 `cbor:"Name"`
		FirehoseArn    string                 `cbor:"FirehoseArn"`
		RoleArn        string                 `cbor:"RoleArn"`
		OutputFormat   string                 `cbor:"OutputFormat"`
		IncludeFilters []CWMetricStreamFilter `cbor:"IncludeFilters"`
		ExcludeFilters []CWMetricStreamFilter `cbor:"ExcludeFilters"`
		Tags           []cwTagKV              `cbor:"Tags"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	if req.Name == "" {
		cwWriteCBORError(w, "MissingParameter", "The parameter Name is required.", http.StatusBadRequest)
		return
	}
	if req.FirehoseArn == "" || req.RoleArn == "" {
		cwWriteCBORError(w, "MissingParameter", "FirehoseArn and RoleArn are required.", http.StatusBadRequest)
		return
	}
	arn, err := cwPutMetricStream(req.Name, req.FirehoseArn, req.RoleArn, req.OutputFormat, req.IncludeFilters, req.ExcludeFilters, req.Tags)
	if err != nil {
		cwWriteCBORError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return
	}
	cwWriteCBOR(w, map[string]any{"Arn": arn})
}

type cborMetricStreamFilter struct {
	Namespace   string   `cbor:"Namespace"`
	MetricNames []string `cbor:"MetricNames,omitempty"`
}

func cwCBORStreamFilters(f []CWMetricStreamFilter) []cborMetricStreamFilter {
	if len(f) == 0 {
		return nil
	}
	out := make([]cborMetricStreamFilter, 0, len(f))
	for _, fl := range f {
		out = append(out, cborMetricStreamFilter(fl))
	}
	return out
}

func handleCWCBORGetMetricStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `cbor:"Name"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	s, ok := cwMetricStreams.Get(req.Name)
	if !ok {
		cwWriteCBORErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "Metric stream %s does not exist", req.Name)
		return
	}
	cwWriteCBOR(w, map[string]any{
		"Arn":            s.Arn,
		"Name":           s.Name,
		"FirehoseArn":    s.FirehoseArn,
		"RoleArn":        s.RoleArn,
		"OutputFormat":   s.OutputFormat,
		"State":          s.State,
		"CreationDate":   time.Unix(s.CreationDate, 0).UTC(),
		"LastUpdateDate": time.Unix(s.LastUpdateDate, 0).UTC(),
		"IncludeFilters": cwCBORStreamFilters(s.IncludeFilters),
		"ExcludeFilters": cwCBORStreamFilters(s.ExcludeFilters),
	})
}

func handleCWCBORDeleteMetricStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `cbor:"Name"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwMetricStreams.Delete(req.Name)
	cwWriteCBOR(w, map[string]any{})
}

type cborMetricStreamEntry struct {
	Arn            string    `cbor:"Arn"`
	Name           string    `cbor:"Name"`
	FirehoseArn    string    `cbor:"FirehoseArn"`
	State          string    `cbor:"State"`
	OutputFormat   string    `cbor:"OutputFormat"`
	CreationDate   time.Time `cbor:"CreationDate"`
	LastUpdateDate time.Time `cbor:"LastUpdateDate"`
}

func handleCWCBORListMetricStreams(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	entries := make([]cborMetricStreamEntry, 0)
	for _, s := range cwListMetricStreams() {
		entries = append(entries, cborMetricStreamEntry{
			Arn:            s.Arn,
			Name:           s.Name,
			FirehoseArn:    s.FirehoseArn,
			State:          s.State,
			OutputFormat:   s.OutputFormat,
			CreationDate:   time.Unix(s.CreationDate, 0).UTC(),
			LastUpdateDate: time.Unix(s.LastUpdateDate, 0).UTC(),
		})
	}
	cwWriteCBOR(w, map[string]any{"Entries": entries})
}

func handleCWCBORStartMetricStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `cbor:"Names"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwSetMetricStreamState(req.Names, "running")
	cwWriteCBOR(w, map[string]any{})
}

func handleCWCBORStopMetricStreams(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `cbor:"Names"`
	}
	if !cwReadCBOR(w, r, &req) {
		return
	}
	cwSetMetricStreamState(req.Names, "stopped")
	cwWriteCBOR(w, map[string]any{})
}

// ── query surface (older aws CLI) ───────────────────────────────────────────

func registerCloudWatchMetricStreamsQuery(r *AWSQueryRouter) {
	r.Register("PutMetricStream", handleCWQueryPutMetricStream)
	r.Register("GetMetricStream", handleCWQueryGetMetricStream)
	r.Register("DeleteMetricStream", handleCWQueryDeleteMetricStream)
	r.Register("ListMetricStreams", handleCWQueryListMetricStreams)
	r.Register("StartMetricStreams", handleCWQueryStartMetricStreams)
	r.Register("StopMetricStreams", handleCWQueryStopMetricStreams)
}

// cwQueryStreamFilters parses include/exclude filter members from the query form.
func cwQueryStreamFilters(r *http.Request, prefix string) []CWMetricStreamFilter {
	var filters []CWMetricStreamFilter
	for i := 1; ; i++ {
		ns := r.FormValue(prefix + ".member." + strconv.Itoa(i) + ".Namespace")
		if ns == "" {
			break
		}
		f := CWMetricStreamFilter{Namespace: ns}
		for j := 1; ; j++ {
			mn := r.FormValue(prefix + ".member." + strconv.Itoa(i) + ".MetricNames.member." + strconv.Itoa(j))
			if mn == "" {
				break
			}
			f.MetricNames = append(f.MetricNames, mn)
		}
		filters = append(filters, f)
	}
	return filters
}

func handleCWQueryPutMetricStream(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		cwQueryError(w, "MissingParameter", "The parameter Name is required.")
		return
	}
	firehose := r.FormValue("FirehoseArn")
	role := r.FormValue("RoleArn")
	if firehose == "" || role == "" {
		cwQueryError(w, "MissingParameter", "FirehoseArn and RoleArn are required.")
		return
	}
	arn, err := cwPutMetricStream(name, firehose, role, r.FormValue("OutputFormat"),
		cwQueryStreamFilters(r, "IncludeFilters"), cwQueryStreamFilters(r, "ExcludeFilters"), nil)
	if err != nil {
		cwQueryError(w, "InvalidParameterValue", err.Error())
		return
	}
	cwQueryResult(w, "PutMetricStream", "<Arn>"+xmlEscape(arn)+"</Arn>")
}

func handleCWQueryGetMetricStream(w http.ResponseWriter, r *http.Request) {
	s, ok := cwMetricStreams.Get(r.FormValue("Name"))
	if !ok {
		cwQueryError(w, "ResourceNotFoundException", "Metric stream "+r.FormValue("Name")+" does not exist")
		return
	}
	var b []byte
	b = cwQueryAppendf(b, "<Arn>%s</Arn><Name>%s</Name>", xmlEscape(s.Arn), xmlEscape(s.Name))
	b = cwQueryAppendf(b, "<FirehoseArn>%s</FirehoseArn><RoleArn>%s</RoleArn>", xmlEscape(s.FirehoseArn), xmlEscape(s.RoleArn))
	b = cwQueryAppendf(b, "<OutputFormat>%s</OutputFormat><State>%s</State>", xmlEscape(s.OutputFormat), xmlEscape(s.State))
	b = cwQueryAppendf(b, "<CreationDate>%s</CreationDate><LastUpdateDate>%s</LastUpdateDate>",
		time.Unix(s.CreationDate, 0).UTC().Format(time.RFC3339), time.Unix(s.LastUpdateDate, 0).UTC().Format(time.RFC3339))
	b = append(b, cwQueryStreamFiltersXML("IncludeFilters", s.IncludeFilters)...)
	b = append(b, cwQueryStreamFiltersXML("ExcludeFilters", s.ExcludeFilters)...)
	cwQueryResult(w, "GetMetricStream", string(b))
}

func cwQueryStreamFiltersXML(tag string, filters []CWMetricStreamFilter) []byte {
	if len(filters) == 0 {
		return nil
	}
	var b []byte
	b = cwQueryAppendf(b, "<%s>", tag)
	for _, f := range filters {
		b = cwQueryAppendf(b, "<member><Namespace>%s</Namespace>", xmlEscape(f.Namespace))
		if len(f.MetricNames) > 0 {
			b = append(b, []byte("<MetricNames>")...)
			for _, mn := range f.MetricNames {
				b = cwQueryAppendf(b, "<member>%s</member>", xmlEscape(mn))
			}
			b = append(b, []byte("</MetricNames>")...)
		}
		b = append(b, []byte("</member>")...)
	}
	b = cwQueryAppendf(b, "</%s>", tag)
	return b
}

func handleCWQueryDeleteMetricStream(w http.ResponseWriter, r *http.Request) {
	cwMetricStreams.Delete(r.FormValue("Name"))
	cwQueryResult(w, "DeleteMetricStream", "")
}

func handleCWQueryListMetricStreams(w http.ResponseWriter, r *http.Request) {
	var b []byte
	b = append(b, []byte("<Entries>")...)
	for _, s := range cwListMetricStreams() {
		b = append(b, []byte("<member>")...)
		b = cwQueryAppendf(b, "<Arn>%s</Arn><Name>%s</Name><FirehoseArn>%s</FirehoseArn>",
			xmlEscape(s.Arn), xmlEscape(s.Name), xmlEscape(s.FirehoseArn))
		b = cwQueryAppendf(b, "<State>%s</State><OutputFormat>%s</OutputFormat>", xmlEscape(s.State), xmlEscape(s.OutputFormat))
		b = cwQueryAppendf(b, "<CreationDate>%s</CreationDate><LastUpdateDate>%s</LastUpdateDate>",
			time.Unix(s.CreationDate, 0).UTC().Format(time.RFC3339), time.Unix(s.LastUpdateDate, 0).UTC().Format(time.RFC3339))
		b = append(b, []byte("</member>")...)
	}
	b = append(b, []byte("</Entries>")...)
	cwQueryResult(w, "ListMetricStreams", string(b))
}

func handleCWQueryStartMetricStreams(w http.ResponseWriter, r *http.Request) {
	cwSetMetricStreamState(cwQueryStringList(r, "Names"), "running")
	cwQueryResult(w, "StartMetricStreams", "")
}

func handleCWQueryStopMetricStreams(w http.ResponseWriter, r *http.Request) {
	cwSetMetricStreamState(cwQueryStringList(r, "Names"), "stopped")
	cwQueryResult(w, "StopMetricStreams", "")
}
