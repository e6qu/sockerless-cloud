package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/kvstore"
	"github.com/google/uuid"
)

// CloudWatch Logs anomaly detection, as the CloudWatch Logs User Guide's "Log
// anomaly detection" describes it. A new detector trains on the past two
// weeks of its log group's events (INITIALIZING, then TRAINING), then
// analyzes the events ingested since its last run once per
// evaluationFrequency (ANALYZING). Pattern recognition splits each event's
// first 1500 characters into static and dynamic tokens; a pattern not seen
// before, a large change in a known pattern's count, and a new value of a
// dynamic token with a discrete set of usual values are anomalies. An anomaly
// stays visible for anomalyVisibilityTime days, after which its behaviour is
// baseline.

const (
	cwLogAnomalyTrainingWindow = 14 * 24 * time.Hour
	cwLogPatternChars          = 1500
	cwLogTokenValuesKept       = 10
	cwLogAnomalyDefaultDays    = 21
)

var cwLogEvaluationFrequencies = map[string]time.Duration{
	"ONE_MIN":     time.Minute,
	"FIVE_MIN":    5 * time.Minute,
	"TEN_MIN":     10 * time.Minute,
	"FIFTEEN_MIN": 15 * time.Minute,
	"THIRTY_MIN":  30 * time.Minute,
	"ONE_HOUR":    time.Hour,
}

type CWLogPatternToken struct {
	DynamicTokenPosition int              `json:"dynamicTokenPosition"`
	IsDynamic            bool             `json:"isDynamic"`
	TokenString          string           `json:"tokenString"`
	Enumerations         map[string]int64 `json:"enumerations,omitempty"`
	InferredTokenName    string           `json:"inferredTokenName,omitempty"`
	// Overflow records values past the ten kept, so a token with more than ten
	// distinct values is not a discrete one.
	Overflow bool `json:"overflow,omitempty"`
}

type CWLogPattern struct {
	PatternID     string              `json:"patternId"`
	PatternString string              `json:"patternString"`
	PatternRegex  string              `json:"patternRegex"`
	Tokens        []CWLogPatternToken `json:"tokens"`
	Count         int64               `json:"count"`
	Severity      string              `json:"severity"`
}

// CWLogAnomalyModel is a detector's trained baseline, keyed by detector ARN.
type CWLogAnomalyModel struct {
	DetectorArn string                   `json:"detectorArn"`
	Patterns    map[string]*CWLogPattern `json:"patterns"`
	SpanStartMs int64                    `json:"spanStartMs"`
	// SuppressedPatterns maps a pattern ID to when its suppression ends; 0 is
	// INFINITE.
	SuppressedPatterns map[string]int64 `json:"suppressedPatterns,omitempty"`
}

type CWLogAnomaly struct {
	AnomalyID                 string              `json:"anomalyId"`
	Key                       string              `json:"key"`
	PatternID                 string              `json:"patternId"`
	AnomalyDetectorArn        string              `json:"anomalyDetectorArn"`
	PatternString             string              `json:"patternString"`
	PatternRegex              string              `json:"patternRegex"`
	Priority                  string              `json:"priority"`
	FirstSeen                 int64               `json:"firstSeen"`
	LastSeen                  int64               `json:"lastSeen"`
	Description               string              `json:"description"`
	Active                    bool                `json:"active"`
	State                     string              `json:"state"`
	Histogram                 map[string]int64    `json:"histogram"`
	LogSamples                []CWLogEvent        `json:"logSamples"`
	PatternTokens             []CWLogPatternToken `json:"patternTokens"`
	LogGroupArnList           []string            `json:"logGroupArnList"`
	Suppressed                bool                `json:"suppressed"`
	SuppressedDate            int64               `json:"suppressedDate,omitempty"`
	SuppressedUntil           int64               `json:"suppressedUntil,omitempty"`
	IsPatternLevelSuppression bool                `json:"isPatternLevelSuppression"`
}

var (
	cwLogAnomalyModels sim.Store[CWLogAnomalyModel]
	cwLogAnomalies     sim.Store[CWLogAnomaly]
)

func registerCloudWatchLogsAnomalyDetection(srv *sim.Server) {
	cwLogAnomalyModels = sim.MakeStore[CWLogAnomalyModel](srv.DB(), "cw_log_anomaly_models")
	cwLogAnomalies = sim.MakeStore[CWLogAnomaly](srv.DB(), "cw_log_anomalies")
	kvstore.StartSweeper(srv, "CloudWatch Logs anomaly detectors", time.Second, cwRunLogAnomalyDetectors)
}

func cwRunLogAnomalyDetectors(now time.Time) {
	for _, d := range cwLogAnomalyDetectors.List() {
		cwRunLogAnomalyDetector(d.AnomalyDetectorArn, now)
	}
}

func cwRunLogAnomalyDetector(arn string, now time.Time) {
	d, ok := cwLogAnomalyDetectors.Get(arn)
	if !ok {
		return
	}
	switch d.AnomalyDetectorStatus {
	case "INITIALIZING":
		cwLogAnomalyDetectors.Update(arn, func(x *CWLogAnomalyDetector) {
			if x.AnomalyDetectorStatus == "INITIALIZING" {
				x.AnomalyDetectorStatus = "TRAINING"
			}
		})
	case "TRAINING":
		cwTrainLogAnomalyDetector(d, now)
	case "ANALYZING":
		freq := cwLogEvaluationFrequencies[d.EvaluationFrequency]
		if freq == 0 || now.UnixMilli()-d.LastEvaluated < freq.Milliseconds() {
			return
		}
		cwAnalyzeLogAnomalyDetector(d, now)
	}
}

func cwLogAnomalyGroups(d CWLogAnomalyDetector) ([]string, bool) {
	var names []string
	for _, arn := range d.LogGroupArnList {
		name, ok := cwLogGroupByArn(arn)
		if !ok {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

// cwLogAnomalyEvents lists the group's events, matching the detector's
// filter pattern, that satisfy keep.
func cwLogAnomalyEvents(d CWLogAnomalyDetector, groups []string, keep func(CWLogEvent) bool) []CWLogEvent {
	var filter *cwCompiledPattern
	if d.FilterPattern != "" {
		filter, _ = cwCompileLogPattern(d.FilterPattern)
	}
	var out []CWLogEvent
	for _, group := range groups {
		for _, stream := range cwLogStreams.Filter(func(s CWLogStream) bool { return s.LogGroupName == group }) {
			events, _ := cwStreamEvents(cwEventsKey(group, stream.LogStreamName))
			for _, e := range events {
				if !keep(e) || (filter != nil && !filter.match(e.processedMessage())) {
					continue
				}
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

func cwTrainLogAnomalyDetector(d CWLogAnomalyDetector, now time.Time) {
	groups, ok := cwLogAnomalyGroups(d)
	if !ok {
		cwLogAnomalyDetectors.Update(d.AnomalyDetectorArn, func(x *CWLogAnomalyDetector) { x.AnomalyDetectorStatus = "FAILED" })
		return
	}
	nowMs := now.UnixMilli()
	from := now.Add(-cwLogAnomalyTrainingWindow).UnixMilli()
	events := cwLogAnomalyEvents(d, groups, func(e CWLogEvent) bool { return e.Timestamp >= from && e.IngestionTime <= nowMs })
	model := CWLogAnomalyModel{DetectorArn: d.AnomalyDetectorArn, Patterns: map[string]*CWLogPattern{}, SpanStartMs: nowMs}
	for _, e := range events {
		model.SpanStartMs = min(model.SpanStartMs, e.Timestamp)
		cwLearnPattern(model.Patterns, cwExtractPattern(e.processedMessage()))
	}
	if existing, ok := cwLogAnomalyModels.Get(d.AnomalyDetectorArn); ok {
		model.SuppressedPatterns = existing.SuppressedPatterns
	}
	cwLogAnomalyModels.Put(d.AnomalyDetectorArn, model)
	cwLogAnomalyDetectors.Update(d.AnomalyDetectorArn, func(x *CWLogAnomalyDetector) {
		if x.AnomalyDetectorStatus == "TRAINING" {
			x.AnomalyDetectorStatus = "ANALYZING"
			x.LastEvaluated = nowMs
		}
	})
}

type cwExtractedPattern struct {
	pattern CWLogPattern
	values  []string
}

var (
	cwTokenDate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T[\d:.]+Z?)?$|^\d{1,2}/\d{1,2}/\d{2,4}$`)
	cwTokenTime     = regexp.MustCompile(`^\d{1,2}:\d{2}(:\d{2}([.,]\d+)?)?$`)
	cwTokenIP       = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}(:\d+)?$`)
	cwTokenUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	cwTokenNumber   = regexp.MustCompile(`^[-+]?\d+(\.\d+)?$`)
	cwTokenHex      = regexp.MustCompile(`^(0x)?[0-9a-fA-F]{8,}$`)
	cwTokenKeyStrip = regexp.MustCompile(`[^A-Za-z0-9]+`)
	cwSeverityWords = []string{"FATAL", "ERROR", "WARN"}
)

// cwExtractPattern splits a message on whitespace: a token holding a digit is
// dynamic, named after the data it holds (Date, Time, IPAddress, UUID,
// Number) or the key before it ("ResourceID:"), else Token; any other token
// is static.
func cwExtractPattern(message string) cwExtractedPattern {
	if len(message) > cwLogPatternChars {
		message = message[:cwLogPatternChars]
	}
	fields := strings.Fields(message)
	var p cwExtractedPattern
	parts := make([]string, 0, len(fields))
	regexParts := make([]string, 0, len(fields))
	dynamic := 0
	for i, f := range fields {
		if !strings.ContainsAny(f, "0123456789") {
			p.pattern.Tokens = append(p.pattern.Tokens, CWLogPatternToken{TokenString: f})
			parts = append(parts, f)
			regexParts = append(regexParts, regexp.QuoteMeta(f))
			continue
		}
		dynamic++
		name := "Token"
		switch {
		case cwTokenDate.MatchString(f):
			name = "Date"
		case cwTokenTime.MatchString(f):
			name = "Time"
		case cwTokenIP.MatchString(f):
			name = "IPAddress"
		case cwTokenUUID.MatchString(f):
			name = "UUID"
		case cwTokenNumber.MatchString(f):
			name = "Number"
		case i > 0 && (strings.HasSuffix(fields[i-1], ":") || strings.HasSuffix(fields[i-1], "=")) && !strings.ContainsAny(fields[i-1], "0123456789"):
			if key := cwTokenKeyStrip.ReplaceAllString(fields[i-1], ""); key != "" {
				name = key
			}
		case cwTokenHex.MatchString(f):
			name = "HexValue"
		}
		p.pattern.Tokens = append(p.pattern.Tokens, CWLogPatternToken{
			IsDynamic: true, TokenString: "<*>", DynamicTokenPosition: dynamic,
			InferredTokenName: fmt.Sprintf("%s-%d", name, dynamic),
		})
		p.values = append(p.values, f)
		parts = append(parts, "<*>")
		regexParts = append(regexParts, `\S+`)
	}
	p.pattern.PatternString = strings.Join(parts, " ")
	p.pattern.PatternRegex = strings.Join(regexParts, `\s+`)
	sum := sha256.Sum256([]byte(p.pattern.PatternString))
	p.pattern.PatternID = hex.EncodeToString(sum[:16])
	p.pattern.Severity = "NONE"
	upper := strings.ToUpper(message)
	for _, word := range cwSeverityWords {
		if strings.Contains(upper, word) {
			p.pattern.Severity = word
			break
		}
	}
	return p
}

func cwLearnPattern(patterns map[string]*CWLogPattern, ex cwExtractedPattern) {
	known := patterns[ex.pattern.PatternID]
	if known == nil {
		copied := ex.pattern
		copied.Tokens = append([]CWLogPatternToken(nil), ex.pattern.Tokens...)
		known = &copied
		patterns[ex.pattern.PatternID] = known
	}
	known.Count++
	v := 0
	for i := range known.Tokens {
		if !known.Tokens[i].IsDynamic {
			continue
		}
		cwCountTokenValue(&known.Tokens[i], ex.values[v])
		v++
	}
}

func cwCountTokenValue(t *CWLogPatternToken, value string) {
	if t.Enumerations == nil {
		t.Enumerations = map[string]int64{}
	}
	if _, ok := t.Enumerations[value]; ok || len(t.Enumerations) < cwLogTokenValuesKept {
		t.Enumerations[value]++
		return
	}
	t.Overflow = true
}

type cwLogFinding struct {
	key         string
	pattern     CWLogPattern
	description string
	priority    string
	samples     []CWLogEvent
	groups      map[string]bool
	count       int64
}

func cwSeverityPriority(severity string) string {
	switch severity {
	case "FATAL", "ERROR":
		return "HIGH"
	case "WARN":
		return "MEDIUM"
	}
	return "LOW"
}

// cwAnalyzeLogAnomalyDetector runs one evaluation over the events ingested
// since the last run.
func cwAnalyzeLogAnomalyDetector(d CWLogAnomalyDetector, now time.Time) {
	groups, ok := cwLogAnomalyGroups(d)
	if !ok {
		cwLogAnomalyDetectors.Update(d.AnomalyDetectorArn, func(x *CWLogAnomalyDetector) { x.AnomalyDetectorStatus = "FAILED" })
		return
	}
	nowMs := now.UnixMilli()
	model, ok := cwLogAnomalyModels.Get(d.AnomalyDetectorArn)
	if !ok {
		model = CWLogAnomalyModel{DetectorArn: d.AnomalyDetectorArn, Patterns: map[string]*CWLogPattern{}, SpanStartMs: nowMs}
	}
	if model.Patterns == nil {
		model.Patterns = map[string]*CWLogPattern{}
	}
	visibility := d.AnomalyVisibilityTime
	if visibility == 0 {
		visibility = cwLogAnomalyDefaultDays
	}
	visibleMs := visibility * 24 * int64(time.Hour/time.Millisecond)
	existing := map[string]CWLogAnomaly{}
	for _, a := range cwLogAnomalies.Filter(func(a CWLogAnomaly) bool { return a.AnomalyDetectorArn == d.AnomalyDetectorArn }) {
		existing[a.Key] = a
	}
	// An anomaly past its visibility time, or marked baseline, is normal
	// behaviour the model learns from.
	baseline := func(key string) bool {
		a, ok := existing[key]
		return ok && (a.State == "Baseline" || nowMs-a.FirstSeen >= visibleMs)
	}

	events := cwLogAnomalyEvents(d, groups, func(e CWLogEvent) bool {
		return e.IngestionTime > d.LastEvaluated && e.IngestionTime <= nowMs
	})
	windowMs := max(nowMs-d.LastEvaluated, 1)
	spanMs := max(d.LastEvaluated-model.SpanStartMs, windowMs)
	observed := map[string][]cwExtractedPattern{}
	var order []string
	sampleOf := map[string][]CWLogEvent{}
	groupsOf := map[string]map[string]bool{}
	for _, e := range events {
		ex := cwExtractPattern(e.processedMessage())
		id := ex.pattern.PatternID
		if _, seen := observed[id]; !seen {
			order = append(order, id)
		}
		observed[id] = append(observed[id], ex)
		if len(sampleOf[id]) < 5 {
			sampleOf[id] = append(sampleOf[id], CWLogEvent{Timestamp: e.Timestamp, Message: e.processedMessage()})
		}
		if groupsOf[id] == nil {
			groupsOf[id] = map[string]bool{}
		}
		for _, arn := range d.LogGroupArnList {
			groupsOf[id][arn] = true
		}
	}

	var findings []cwLogFinding
	for _, id := range order {
		occurrences := observed[id]
		ex := occurrences[0]
		count := int64(len(occurrences))
		known := model.Patterns[id]
		seenNow := map[string]*CWLogPattern{}
		for _, o := range occurrences {
			cwLearnPattern(seenNow, o)
		}
		finding := func(key, description, priority string) cwLogFinding {
			return cwLogFinding{key: key, pattern: *seenNow[id], description: description, priority: priority, samples: sampleOf[id], groups: groupsOf[id], count: count}
		}
		if known == nil {
			key := id + "|new"
			if baseline(key) {
				for _, o := range occurrences {
					cwLearnPattern(model.Patterns, o)
				}
				continue
			}
			priority := "MEDIUM"
			if cwSeverityPriority(ex.pattern.Severity) == "HIGH" {
				priority = "HIGH"
			}
			findings = append(findings, finding(key, "New log pattern: "+ex.pattern.PatternString, priority))
			continue
		}
		expected := float64(known.Count) * float64(windowMs) / float64(spanMs)
		countKey := id + "|count"
		if float64(count) > 5*expected && count >= 10 && !baseline(countKey) {
			findings = append(findings, finding(countKey, fmt.Sprintf("Log pattern count rose to %d from an expected %.1f: %s", count, expected, ex.pattern.PatternString), "HIGH"))
		}
		v := 0
		for i, t := range known.Tokens {
			if !t.IsDynamic {
				continue
			}
			discrete := !t.Overflow && len(t.Enumerations) > 0 && known.Count >= 2*int64(len(t.Enumerations))
			seen := map[string]bool{}
			for _, o := range occurrences {
				value := o.values[v]
				if _, usual := t.Enumerations[value]; usual || !discrete || seen[value] {
					continue
				}
				seen[value] = true
				key := id + "|token|" + strconv.Itoa(i) + "|" + value
				if baseline(key) {
					continue
				}
				findings = append(findings, finding(key, fmt.Sprintf("New value %q for %s in log pattern: %s", value, t.InferredTokenName, ex.pattern.PatternString),
					cwSeverityPriority(ex.pattern.Severity)))
			}
			v++
		}
		for _, o := range occurrences {
			cwLearnPattern(model.Patterns, o)
		}
	}

	runKey := strconv.FormatInt(nowMs, 10)
	reported := map[string]bool{}
	priorities := map[string]float64{}
	for _, f := range findings {
		if until, ok := model.SuppressedPatterns[f.pattern.PatternID]; ok && (until == 0 || until > nowMs) {
			cwRecordLogAnomaly(d, f, nowMs, runKey, true)
			continue
		}
		if cwRecordLogAnomaly(d, f, nowMs, runKey, false) {
			reported[f.key] = true
			priorities[f.priority]++
		}
	}
	for key, a := range existing {
		if a.Active && !reported[key] {
			cwLogAnomalies.Update(a.AnomalyID, func(x *CWLogAnomaly) { x.Active = false })
		}
	}
	for priority, n := range priorities {
		cwStoreDatum(CWMetricDatum{
			Namespace:  "AWS/Logs",
			MetricName: "AnomalyCount",
			Dimensions: []CWDimension{{Name: "LogAnomalyDetector", Value: d.DetectorName}, {Name: "LogAnomalyPriority", Value: priority}},
			Value:      n,
			Timestamp:  float64(now.Unix()),
			Unit:       "Count",
		})
	}
	cwLogAnomalyModels.Put(d.AnomalyDetectorArn, model)
	cwLogAnomalyDetectors.Update(d.AnomalyDetectorArn, func(x *CWLogAnomalyDetector) {
		if x.AnomalyDetectorStatus == "ANALYZING" {
			x.LastEvaluated = nowMs
		}
	})
}

// cwRecordLogAnomaly records or updates the anomaly a finding describes and
// reports whether it counts as reported in this run. A suppressed anomaly,
// or one under a suppressed pattern, takes no new data.
func cwRecordLogAnomaly(d CWLogAnomalyDetector, f cwLogFinding, nowMs int64, runKey string, patternSuppressed bool) bool {
	if known := cwLogAnomalies.Filter(func(a CWLogAnomaly) bool {
		return a.AnomalyDetectorArn == d.AnomalyDetectorArn && a.Key == f.key
	}); len(known) > 0 {
		a := known[0]
		if patternSuppressed || a.Suppressed && (a.SuppressedUntil == 0 || a.SuppressedUntil > nowMs) {
			return false
		}
		cwLogAnomalies.Update(a.AnomalyID, func(x *CWLogAnomaly) {
			if x.Suppressed {
				x.Suppressed, x.SuppressedDate, x.SuppressedUntil, x.IsPatternLevelSuppression, x.State = false, 0, 0, false, "Active"
			}
			x.LastSeen, x.Active = nowMs, true
			x.Histogram[runKey] += f.count
			x.LogSamples = f.samples
			x.Priority = f.priority
		})
		return true
	}
	groups := make([]string, 0, len(f.groups))
	for g := range f.groups {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	a := CWLogAnomaly{
		AnomalyID:          uuid.New().String(),
		Key:                f.key,
		PatternID:          f.pattern.PatternID,
		AnomalyDetectorArn: d.AnomalyDetectorArn,
		PatternString:      f.pattern.PatternString,
		PatternRegex:       f.pattern.PatternRegex,
		Priority:           f.priority,
		FirstSeen:          nowMs,
		LastSeen:           nowMs,
		Description:        f.description,
		Active:             true,
		State:              "Active",
		Histogram:          map[string]int64{runKey: f.count},
		LogSamples:         f.samples,
		PatternTokens:      f.pattern.Tokens,
		LogGroupArnList:    groups,
	}
	if patternSuppressed {
		a.Suppressed, a.State, a.IsPatternLevelSuppression, a.SuppressedDate = true, "Suppressed", true, nowMs
		a.Active = false
	}
	cwLogAnomalies.Put(a.AnomalyID, a)
	return !patternSuppressed
}

func (a CWLogAnomaly) suppressedAt(nowMs int64) bool {
	return a.Suppressed && (a.SuppressedUntil == 0 || a.SuppressedUntil > nowMs)
}

func (a CWLogAnomaly) wire(nowMs int64) map[string]any {
	samples := make([]map[string]any, 0, len(a.LogSamples))
	for _, s := range a.LogSamples {
		samples = append(samples, map[string]any{"timestamp": s.Timestamp, "message": s.Message})
	}
	tokens := make([]map[string]any, 0, len(a.PatternTokens))
	for _, t := range a.PatternTokens {
		token := map[string]any{"dynamicTokenPosition": t.DynamicTokenPosition, "isDynamic": t.IsDynamic, "tokenString": t.TokenString}
		if t.IsDynamic {
			token["inferredTokenName"] = t.InferredTokenName
			token["enumerations"] = t.Enumerations
		}
		tokens = append(tokens, token)
	}
	state := a.State
	suppressed := a.suppressedAt(nowMs)
	if a.Suppressed && !suppressed {
		state = "Active"
	}
	out := map[string]any{
		"anomalyId":                 a.AnomalyID,
		"patternId":                 a.PatternID,
		"anomalyDetectorArn":        a.AnomalyDetectorArn,
		"patternString":             a.PatternString,
		"patternRegex":              a.PatternRegex,
		"priority":                  a.Priority,
		"firstSeen":                 a.FirstSeen,
		"lastSeen":                  a.LastSeen,
		"description":               a.Description,
		"active":                    a.Active,
		"state":                     state,
		"histogram":                 a.Histogram,
		"logSamples":                samples,
		"patternTokens":             tokens,
		"logGroupArnList":           a.LogGroupArnList,
		"suppressed":                suppressed,
		"isPatternLevelSuppression": suppressed && a.IsPatternLevelSuppression,
	}
	if suppressed {
		out["suppressedDate"] = a.SuppressedDate
		out["suppressedUntil"] = a.SuppressedUntil
	}
	return out
}

func handleCWListAnomalies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
		SuppressionState   string `json:"suppressionState"`
		Limit              int    `json:"limit"`
		NextToken          string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AnomalyDetectorArn != "" {
		if _, ok := cwLogAnomalyDetectors.Get(req.AnomalyDetectorArn); !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
			return
		}
	}
	if req.SuppressionState != "" && req.SuppressionState != "SUPPRESSED" && req.SuppressionState != "UNSUPPRESSED" {
		AWSError(w, "InvalidParameterException", "suppressionState must be SUPPRESSED or UNSUPPRESSED", http.StatusBadRequest)
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 50 {
		AWSError(w, "InvalidParameterException", "limit must be between 1 and 50", http.StatusBadRequest)
		return
	}
	nowMs := time.Now().UnixMilli()
	var matched []CWLogAnomaly
	for _, a := range cwLogAnomalies.List() {
		if req.AnomalyDetectorArn != "" && a.AnomalyDetectorArn != req.AnomalyDetectorArn {
			continue
		}
		d, ok := cwLogAnomalyDetectors.Get(a.AnomalyDetectorArn)
		if !ok {
			continue
		}
		days := d.AnomalyVisibilityTime
		if days == 0 {
			days = cwLogAnomalyDefaultDays
		}
		if nowMs-a.FirstSeen >= days*24*int64(time.Hour/time.Millisecond) {
			continue
		}
		suppressed := a.suppressedAt(nowMs)
		if req.SuppressionState == "SUPPRESSED" && !suppressed || req.SuppressionState == "UNSUPPRESSED" && suppressed {
			continue
		}
		matched = append(matched, a)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].LastSeen != matched[j].LastSeen {
			return matched[i].LastSeen > matched[j].LastSeen
		}
		return matched[i].AnomalyID < matched[j].AnomalyID
	})
	start := 0
	if req.NextToken != "" {
		n, err := strconv.Atoi(req.NextToken)
		if err != nil || n < 0 || n > len(matched) {
			AWSError(w, "InvalidParameterException", "The nextToken is not valid", http.StatusBadRequest)
			return
		}
		start = n
	}
	end := min(start+limit, len(matched))
	out := make([]map[string]any, 0, end-start)
	for _, a := range matched[start:end] {
		out = append(out, a.wire(nowMs))
	}
	resp := map[string]any{"anomalies": out}
	if end < len(matched) {
		resp["nextToken"] = strconv.Itoa(end)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// UpdateAnomaly suppresses one anomaly or a whole pattern, LIMITED for
// suppressionPeriod or INFINITE; with neither suppressionType nor
// suppressionPeriod it ends the suppression. baseline marks the behaviour
// normal.
func handleCWUpdateAnomaly(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
		AnomalyId          string `json:"anomalyId"`
		PatternId          string `json:"patternId"`
		SuppressionType    string `json:"suppressionType"`
		SuppressionPeriod  *struct {
			Value           int64  `json:"value"`
			SuppressionUnit string `json:"suppressionUnit"`
		} `json:"suppressionPeriod"`
		Baseline *bool `json:"baseline"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AnomalyDetectorArn == "" {
		AWSError(w, "InvalidParameterException", "anomalyDetectorArn is required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogAnomalyDetectors.Get(req.AnomalyDetectorArn); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified anomaly detector does not exist: %s", req.AnomalyDetectorArn)
		return
	}
	if (req.AnomalyId == "") == (req.PatternId == "") {
		AWSError(w, "InvalidParameterException", "Specify either anomalyId or patternId, but not both", http.StatusBadRequest)
		return
	}
	now := time.Now()
	nowMs := now.UnixMilli()
	var until int64
	suppress := req.SuppressionType != "" || req.SuppressionPeriod != nil
	switch req.SuppressionType {
	case "INFINITE":
	case "LIMITED", "":
		if !suppress {
			break
		}
		if req.SuppressionPeriod == nil || req.SuppressionPeriod.Value <= 0 {
			AWSError(w, "InvalidParameterException", "suppressionPeriod is required for a LIMITED suppression", http.StatusBadRequest)
			return
		}
		unit := map[string]time.Duration{"SECONDS": time.Second, "MINUTES": time.Minute, "HOURS": time.Hour}[req.SuppressionPeriod.SuppressionUnit]
		if unit == 0 {
			AWSError(w, "InvalidParameterException", "suppressionUnit must be SECONDS, MINUTES or HOURS", http.StatusBadRequest)
			return
		}
		until = now.Add(time.Duration(req.SuppressionPeriod.Value) * unit).UnixMilli()
	default:
		AWSError(w, "InvalidParameterException", "suppressionType must be LIMITED or INFINITE", http.StatusBadRequest)
		return
	}
	targets := cwLogAnomalies.Filter(func(a CWLogAnomaly) bool {
		return a.AnomalyDetectorArn == req.AnomalyDetectorArn &&
			(req.AnomalyId != "" && a.AnomalyID == req.AnomalyId || req.PatternId != "" && a.PatternID == req.PatternId)
	})
	if req.AnomalyId != "" && len(targets) == 0 {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest, "The specified anomaly does not exist: %s", req.AnomalyId)
		return
	}
	if req.PatternId != "" {
		cwLogAnomalyModels.Update(req.AnomalyDetectorArn, func(m *CWLogAnomalyModel) {
			if m.SuppressedPatterns == nil {
				m.SuppressedPatterns = map[string]int64{}
			}
			if suppress {
				m.SuppressedPatterns[req.PatternId] = until
			} else {
				delete(m.SuppressedPatterns, req.PatternId)
			}
		})
	}
	for _, a := range targets {
		cwLogAnomalies.Update(a.AnomalyID, func(x *CWLogAnomaly) {
			if req.Baseline != nil && *req.Baseline {
				x.State, x.Active = "Baseline", false
			}
			switch {
			case suppress:
				x.Suppressed, x.SuppressedDate, x.SuppressedUntil = true, nowMs, until
				x.IsPatternLevelSuppression = req.PatternId != ""
				if x.State != "Baseline" {
					x.State = "Suppressed"
				}
			case x.Suppressed:
				x.Suppressed, x.SuppressedDate, x.SuppressedUntil, x.IsPatternLevelSuppression = false, 0, 0, false
				if x.State == "Suppressed" {
					x.State = "Active"
				}
			}
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// cwDeleteLogAnomalyModel drops a deleted detector's model and anomalies.
func cwDeleteLogAnomalyModel(arn string) {
	cwLogAnomalyModels.Delete(arn)
	cwLogAnomalies.Prune(func(a CWLogAnomaly) bool { return a.AnomalyDetectorArn == arn })
}
