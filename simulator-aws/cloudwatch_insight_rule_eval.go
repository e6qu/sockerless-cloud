package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Contributor Insights evaluates a rule over the log events its log groups
// ingest while the rule is ENABLED, as the User Guide's "Contributor
// Insights rule syntax" defines the rule: LogGroupNames (a trailing *
// matches a prefix) or LogGroupARNs, LogFormat JSON or CLF with Fields
// aliases, up to four Keys, an optional ValueOf, up to four Filters joined by
// AND, and AggregateOn Count or Sum.

// CWInsightWindow is a span a rule was ENABLED, in epoch milliseconds; To 0
// is still open.
type CWInsightWindow struct {
	From int64 `json:"From"`
	To   int64 `json:"To,omitempty"`
}

type cwInsightRuleDefinition struct {
	Schema struct {
		Name    string `json:"Name"`
		Version int    `json:"Version"`
	} `json:"Schema"`
	LogGroupNames []string          `json:"LogGroupNames"`
	LogGroupARNs  []string          `json:"LogGroupARNs"`
	LogFormat     string            `json:"LogFormat"`
	Fields        map[string]string `json:"Fields"`
	Contribution  struct {
		Keys    []string                     `json:"Keys"`
		ValueOf string                       `json:"ValueOf"`
		Filters []map[string]json.RawMessage `json:"Filters"`
	} `json:"Contribution"`
	AggregateOn string `json:"AggregateOn"`
}

var cwInsightFilterOperators = []string{"In", "NotIn", "StartsWith", "GreaterThan", "LessThan", "EqualTo", "NotEqualTo", "IsPresent"}

func cwParseInsightRuleDefinition(definition string) (cwInsightRuleDefinition, error) {
	var d cwInsightRuleDefinition
	if err := json.Unmarshal([]byte(definition), &d); err != nil {
		return d, cwRuleProblem("The rule definition is not valid JSON: %v", err)
	}
	if d.Schema.Name != "CloudWatchLogRule" || d.Schema.Version != 1 {
		return d, cwRuleProblem(`The rule's Schema must be {"Name": "CloudWatchLogRule", "Version": 1}.`)
	}
	if (len(d.LogGroupNames) == 0) == (len(d.LogGroupARNs) == 0) {
		return d, cwRuleProblem("The rule must specify either LogGroupNames or LogGroupARNs, but not both.")
	}
	if d.LogFormat != "JSON" && d.LogFormat != "CLF" {
		return d, cwRuleProblem("The rule's LogFormat must be JSON or CLF.")
	}
	if n := len(d.Contribution.Keys); n < 1 || n > 4 {
		return d, cwRuleProblem("The rule's Contribution must have one to four Keys.")
	}
	if len(d.Contribution.Filters) > 4 {
		return d, cwRuleProblem("The rule's Contribution can have at most four Filters.")
	}
	for _, f := range d.Contribution.Filters {
		if _, ok := f["Match"]; !ok {
			return d, cwRuleProblem("Each filter must include a Match field.")
		}
		ops := 0
		for _, op := range cwInsightFilterOperators {
			if _, ok := f[op]; ok {
				ops++
			}
		}
		if ops != 1 {
			return d, cwRuleProblem("Each filter must include exactly one of %s.", strings.Join(cwInsightFilterOperators, ", "))
		}
	}
	switch d.AggregateOn {
	case "Count":
	case "Sum":
		if d.Contribution.ValueOf == "" {
			return d, cwRuleProblem("A rule that aggregates on Sum must specify ValueOf.")
		}
	default:
		return d, cwRuleProblem("The rule's AggregateOn must be Count or Sum.")
	}
	return d, nil
}

func (d cwInsightRuleDefinition) groups() []string {
	var out []string
	for _, g := range cwLogGroups.List() {
		if d.selects(g.LogGroupName) {
			out = append(out, g.LogGroupName)
		}
	}
	sort.Strings(out)
	return out
}

func (d cwInsightRuleDefinition) selects(group string) bool {
	for _, pattern := range d.LogGroupNames {
		if prefix, ok := strings.CutSuffix(pattern, "*"); ok && strings.HasPrefix(group, prefix) || pattern == group {
			return true
		}
	}
	for _, arn := range d.LogGroupARNs {
		parts := strings.SplitN(arn, ":", 6)
		if len(parts) != 6 || parts[2] != "logs" || parts[3] != awsRegion() || (parts[4] != "*" && parts[4] != awsAccountID()) {
			continue
		}
		name, ok := strings.CutPrefix(parts[5], "log-group:")
		if !ok {
			name, ok = strings.CutPrefix(parts[5], "log-group/")
		}
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, ":*")
		if prefix, ok := strings.CutSuffix(name, "*"); ok && strings.HasPrefix(group, prefix) || name == group {
			return true
		}
	}
	return false
}

// cwInsightFields reads an event's fields: a JSON event by property path, a
// CLF event by its 1-based field position or the Fields alias for it.
type cwInsightFields func(path string) (string, bool)

func (d cwInsightRuleDefinition) fields(message string) (cwInsightFields, bool) {
	if d.LogFormat == "CLF" {
		tokens := cwSplitCLF(message)
		aliases := map[string]int{}
		for pos, alias := range d.Fields {
			if n, err := strconv.Atoi(pos); err == nil {
				aliases[alias] = n
			}
		}
		return func(path string) (string, bool) {
			n, ok := aliases[path]
			if !ok {
				var err error
				if n, err = strconv.Atoi(path); err != nil {
					return "", false
				}
			}
			if n < 1 || n > len(tokens) {
				return "", false
			}
			return tokens[n-1], true
		}, true
	}
	var doc any
	if json.Unmarshal([]byte(message), &doc) != nil {
		return nil, false
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, false
	}
	return func(path string) (string, bool) {
		v, ok := cwJSONPathValue(doc, path)
		if !ok {
			return "", false
		}
		switch x := v.(type) {
		case string:
			return x, true
		case nil:
			return "", false
		case map[string]any, []any:
			return "", false
		default:
			b, _ := json.Marshal(x)
			return string(b), true
		}
	}, true
}

// cwJSONPathValue follows $.a.b[0].c through a decoded JSON document.
func cwJSONPathValue(doc any, path string) (any, bool) {
	rest, ok := strings.CutPrefix(path, "$")
	if !ok {
		rest = "." + path
	}
	cur := doc
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = obj[rest[:end]]; !ok {
				return nil, false
			}
			rest = rest[end:]
		case '[':
			close := strings.IndexByte(rest, ']')
			if close < 0 {
				return nil, false
			}
			idx, err := strconv.Atoi(rest[1:close])
			arr, ok := cur.([]any)
			if err != nil || !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			rest = rest[close+1:]
		default:
			return nil, false
		}
	}
	return cur, true
}

// cwSplitCLF splits a Common Log Format line on spaces, keeping a quoted or
// bracketed field whole.
func cwSplitCLF(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		switch line[i] {
		case ' ':
			i++
		case '"', '[':
			closer := byte('"')
			if line[i] == '[' {
				closer = ']'
			}
			end := strings.IndexByte(line[i+1:], closer)
			if end < 0 {
				out = append(out, line[i+1:])
				return out
			}
			out = append(out, line[i+1:i+1+end])
			i += end + 2
		default:
			end := strings.IndexByte(line[i:], ' ')
			if end < 0 {
				out = append(out, line[i:])
				return out
			}
			out = append(out, line[i:i+end])
			i += end
		}
	}
	return out
}

func (d cwInsightRuleDefinition) admits(get cwInsightFields) bool {
	for _, f := range d.Contribution.Filters {
		var match string
		if json.Unmarshal(f["Match"], &match) != nil {
			return false
		}
		value, present := get(match)
		if !cwInsightFilterAdmits(f, value, present) {
			return false
		}
	}
	return true
}

func cwInsightFilterAdmits(f map[string]json.RawMessage, value string, present bool) bool {
	strs := func(op string) []string {
		var list []string
		_ = json.Unmarshal(f[op], &list)
		return list
	}
	num := func(op string) (float64, float64, bool) {
		var want float64
		if json.Unmarshal(f[op], &want) != nil || !present {
			return 0, 0, false
		}
		got, err := strconv.ParseFloat(value, 64)
		return got, want, err == nil
	}
	if raw, ok := f["IsPresent"]; ok {
		var want bool
		_ = json.Unmarshal(raw, &want)
		return present == want
	}
	if _, ok := f["In"]; ok {
		return present && cwStrIn(value, strs("In"))
	}
	if _, ok := f["NotIn"]; ok {
		return !present || !cwStrIn(value, strs("NotIn"))
	}
	if _, ok := f["StartsWith"]; ok {
		if !present {
			return false
		}
		for _, p := range strs("StartsWith") {
			if strings.HasPrefix(value, p) {
				return true
			}
		}
		return false
	}
	for _, op := range []string{"GreaterThan", "LessThan", "EqualTo", "NotEqualTo"} {
		if _, ok := f[op]; !ok {
			continue
		}
		got, want, ok := num(op)
		if !ok {
			return op == "NotEqualTo"
		}
		switch op {
		case "GreaterThan":
			return got > want
		case "LessThan":
			return got < want
		case "EqualTo":
			return got == want
		default:
			return got != want
		}
	}
	return false
}

func (ir CWInsightRule) enabledAt(ms int64) bool {
	for _, w := range ir.EnabledWindows {
		if ms >= w.From && (w.To == 0 || ms < w.To) {
			return true
		}
	}
	return false
}

type cwInsightContribution struct {
	keys  []string
	at    int64
	value float64
}

// cwInsightContributions reads every event, timestamped in [start, end), that
// the rule's log groups ingested while it was ENABLED.
func cwInsightContributions(ir CWInsightRule, d cwInsightRuleDefinition, start, end int64) []cwInsightContribution {
	var out []cwInsightContribution
	for _, group := range d.groups() {
		for _, stream := range cwLogStreams.Filter(func(s CWLogStream) bool { return s.LogGroupName == group }) {
			events, _ := cwStreamEvents(cwEventsKey(group, stream.LogStreamName))
			for _, e := range events {
				if e.Timestamp < start*1000 || e.Timestamp >= end*1000 || !ir.enabledAt(e.IngestionTime) {
					continue
				}
				message := e.Message
				if ir.ApplyOnTransformedLogs {
					message = e.processedMessage()
				}
				get, ok := d.fields(message)
				if !ok || !d.admits(get) {
					continue
				}
				keys := make([]string, 0, len(d.Contribution.Keys))
				for _, k := range d.Contribution.Keys {
					v, ok := get(k)
					if !ok {
						break
					}
					keys = append(keys, v)
				}
				if len(keys) != len(d.Contribution.Keys) {
					continue
				}
				value := 1.0
				if d.AggregateOn == "Sum" {
					raw, ok := get(d.Contribution.ValueOf)
					if !ok {
						continue
					}
					v, err := strconv.ParseFloat(raw, 64)
					if err != nil {
						continue
					}
					value = v
				}
				out = append(out, cwInsightContribution{keys: keys, at: e.Timestamp / 1000, value: value})
			}
		}
	}
	return out
}

type cwInsightContributorReport struct {
	Keys       []string
	Aggregate  float64
	Maximum    float64
	Datapoints []cwInsightPoint
}

type cwInsightPoint struct {
	Timestamp int64
	Value     float64
}

type cwInsightMetricPoint struct {
	Timestamp int64
	Values    map[string]float64
}

type cwInsightReport struct {
	KeyLabels         []string
	AggregationStat   string
	AggregateValue    float64
	UniqueCount       int64
	Contributors      []cwInsightContributorReport
	MetricDatapoints  []cwInsightMetricPoint
	RequestedMetrics  []string
	MaxContributorCnt int
}

var cwInsightReportMetrics = []string{"UniqueContributors", "MaxContributorValue", "SampleCount", "Average", "Sum", "Minimum", "Maximum"}

// cwBuildInsightReport answers GetInsightRuleReport over [start, end) in
// period buckets, with the top maxContributors ranked by orderBy (Sum, the
// default, or Maximum).
func cwBuildInsightReport(ir CWInsightRule, start, end int64, period int32, maxContributors int, orderBy string, metrics []string) (cwInsightReport, error) {
	d, err := cwParseInsightRuleDefinition(ir.Definition)
	if err != nil {
		return cwInsightReport{}, err
	}
	if period <= 0 {
		return cwInsightReport{}, cwRuleProblem("The parameter Period must be a positive number of seconds.")
	}
	if maxContributors == 0 {
		maxContributors = 10
	}
	if maxContributors < 1 || maxContributors > 100 {
		return cwInsightReport{}, cwRuleProblem("The parameter MaxContributorCount must be between 1 and 100.")
	}
	for _, m := range metrics {
		if !cwStrIn(m, cwInsightReportMetrics) {
			return cwInsightReport{}, cwRuleProblem("The metric %s is not one of %s.", m, strings.Join(cwInsightReportMetrics, ", "))
		}
	}
	p := int64(period)
	contributions := cwInsightContributions(ir, d, start, end)
	type acc struct {
		keys    []string
		total   float64
		maximum float64
		buckets map[int64]float64
	}
	byKey := map[string]*acc{}
	type bucketAcc struct {
		samples   float64
		sum       float64
		min, max  float64
		perContri map[string]float64
	}
	buckets := map[int64]*bucketAcc{}
	report := cwInsightReport{KeyLabels: d.Contribution.Keys, AggregationStat: d.AggregateOn, RequestedMetrics: metrics}
	for _, c := range contributions {
		k := strings.Join(c.keys, "\x00")
		a := byKey[k]
		if a == nil {
			a = &acc{keys: c.keys, buckets: map[int64]float64{}, maximum: math.Inf(-1)}
			byKey[k] = a
		}
		b := (c.at / p) * p
		a.total += c.value
		a.buckets[b] += c.value
		a.maximum = math.Max(a.maximum, a.buckets[b])
		ba := buckets[b]
		if ba == nil {
			ba = &bucketAcc{min: math.Inf(1), max: math.Inf(-1), perContri: map[string]float64{}}
			buckets[b] = ba
		}
		ba.samples++
		ba.sum += c.value
		ba.min, ba.max = math.Min(ba.min, c.value), math.Max(ba.max, c.value)
		ba.perContri[k] += c.value
		report.AggregateValue += c.value
	}
	report.UniqueCount = int64(len(byKey))
	all := make([]*acc, 0, len(byKey))
	for _, a := range byKey {
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool {
		x, y := all[i].total, all[j].total
		if orderBy == "Maximum" {
			x, y = all[i].maximum, all[j].maximum
		}
		if x != y {
			return x > y
		}
		return strings.Join(all[i].keys, "\x00") < strings.Join(all[j].keys, "\x00")
	})
	for i, a := range all {
		if i >= maxContributors {
			break
		}
		cr := cwInsightContributorReport{Keys: a.keys, Aggregate: a.total, Maximum: a.maximum}
		for t, v := range a.buckets {
			cr.Datapoints = append(cr.Datapoints, cwInsightPoint{Timestamp: t, Value: v})
		}
		sort.Slice(cr.Datapoints, func(i, j int) bool { return cr.Datapoints[i].Timestamp < cr.Datapoints[j].Timestamp })
		report.Contributors = append(report.Contributors, cr)
	}
	for t := (start / p) * p; t < end; t += p {
		if t < start {
			continue
		}
		point := cwInsightMetricPoint{Timestamp: t, Values: map[string]float64{}}
		ba := buckets[t]
		for _, m := range metrics {
			v := 0.0
			if ba != nil {
				switch m {
				case "UniqueContributors":
					v = float64(len(ba.perContri))
				case "MaxContributorValue":
					for _, cv := range ba.perContri {
						v = math.Max(v, cv)
					}
				case "SampleCount":
					v = ba.samples
				case "Average":
					v = ba.sum / ba.samples
				case "Sum":
					v = ba.sum
				case "Minimum":
					v = ba.min
				case "Maximum":
					v = ba.max
				}
			}
			point.Values[m] = v
		}
		report.MetricDatapoints = append(report.MetricDatapoints, point)
	}
	return report, nil
}

func cwInsightReportJSON(rep cwInsightReport) map[string]any {
	contributors := make([]map[string]any, 0, len(rep.Contributors))
	for _, c := range rep.Contributors {
		points := make([]map[string]any, 0, len(c.Datapoints))
		for _, p := range c.Datapoints {
			points = append(points, map[string]any{"Timestamp": float64(p.Timestamp), "ApproximateValue": cwJSONStat(p.Value)})
		}
		contributors = append(contributors, map[string]any{"Keys": c.Keys, "ApproximateAggregateValue": cwJSONStat(c.Aggregate), "Datapoints": points})
	}
	datapoints := make([]map[string]any, 0, len(rep.MetricDatapoints))
	for _, p := range rep.MetricDatapoints {
		dp := map[string]any{"Timestamp": float64(p.Timestamp)}
		for k, v := range p.Values {
			dp[k] = cwJSONStat(v)
		}
		datapoints = append(datapoints, dp)
	}
	return map[string]any{
		"KeyLabels":              rep.KeyLabels,
		"AggregationStatistic":   rep.AggregationStat,
		"AggregateValue":         cwJSONStat(rep.AggregateValue),
		"ApproximateUniqueCount": rep.UniqueCount,
		"Contributors":           contributors,
		"MetricDatapoints":       datapoints,
	}
}

func cwInsightReportCBOR(rep cwInsightReport) map[string]any {
	contributors := make([]map[string]any, 0, len(rep.Contributors))
	for _, c := range rep.Contributors {
		points := make([]map[string]any, 0, len(c.Datapoints))
		for _, p := range c.Datapoints {
			points = append(points, map[string]any{"Timestamp": time.Unix(p.Timestamp, 0).UTC(), "ApproximateValue": p.Value})
		}
		contributors = append(contributors, map[string]any{"Keys": c.Keys, "ApproximateAggregateValue": c.Aggregate, "Datapoints": points})
	}
	datapoints := make([]map[string]any, 0, len(rep.MetricDatapoints))
	for _, p := range rep.MetricDatapoints {
		dp := map[string]any{"Timestamp": time.Unix(p.Timestamp, 0).UTC()}
		for k, v := range p.Values {
			dp[k] = v
		}
		datapoints = append(datapoints, dp)
	}
	return map[string]any{
		"KeyLabels":              rep.KeyLabels,
		"AggregationStatistic":   rep.AggregationStat,
		"AggregateValue":         rep.AggregateValue,
		"ApproximateUniqueCount": rep.UniqueCount,
		"Contributors":           contributors,
		"MetricDatapoints":       datapoints,
	}
}

// cwRuleProblemError is a refusal whose text is the response message.
type cwRuleProblemError struct{ msg string }

func (e *cwRuleProblemError) Error() string { return e.msg }

func cwRuleProblem(format string, args ...any) error {
	return &cwRuleProblemError{msg: fmt.Sprintf(format, args...)}
}
