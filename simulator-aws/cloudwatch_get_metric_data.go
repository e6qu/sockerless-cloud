package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GetMetricData returns at most MaxDatapoints data points, 100,800 by
// default, newest first unless ScanBy says otherwise, and pages the rest by
// NextToken. A request that performs a math expression gets no NextToken.
// Labels expand the dynamic values the User Guide's "Use dynamic labels"
// lists, with times in LabelOptions.Timezone.

const cwDefaultMaxDatapoints = 100800

type cwGetMetricDataInput struct {
	Queries       []CWMetricDataQuery
	Start, End    time.Time
	ScanBy        string
	MaxDatapoints int
	NextToken     string
	Timezone      string
}

// cwMetricDataOutput is one MetricDataResult in protocol-neutral form.
type cwMetricDataOutput struct {
	Id         string
	Label      string
	StatusCode string
	Messages   []cwMessage
	Timestamps []time.Time
	Values     []float64
}

type cwMetricDataPage struct {
	Results   []cwMetricDataOutput
	NextToken string
	Messages  []cwMessage
}

// cwMetricDataFailure carries the error code a GetMetricData failure answers.
type cwMetricDataFailure struct {
	code    string
	message string
}

var cwLabelTimezonePattern = regexp.MustCompile(`^[+-]([01][0-9]|2[0-3])([0-5][0-9])$`)

func cwGetMetricData(ctx context.Context, in cwGetMetricDataInput) (cwMetricDataPage, *cwMetricDataFailure) {
	zone := time.UTC
	if in.Timezone != "" {
		m := cwLabelTimezonePattern.FindStringSubmatch(in.Timezone)
		if m == nil {
			return cwMetricDataPage{}, &cwMetricDataFailure{"InvalidParameterValue",
				fmt.Sprintf("The value %s for parameter LabelOptions.Timezone is not valid: use + or - followed by four digits, such as +0130.", in.Timezone)}
		}
		hours, _ := strconv.Atoi(m[1])
		minutes, _ := strconv.Atoi(m[2])
		offset := hours*3600 + minutes*60
		if in.Timezone[0] == '-' {
			offset = -offset
		}
		zone = time.FixedZone(in.Timezone, offset)
	}
	if in.MaxDatapoints < 0 {
		return cwMetricDataPage{}, &cwMetricDataFailure{"InvalidParameterValue", "The parameter MaxDatapoints must be greater than 0."}
	}
	limit := in.MaxDatapoints
	if limit == 0 {
		limit = cwDefaultMaxDatapoints
	}
	now := time.Now()
	results, err := cwEvaluateMetricQueries(cwMathOptions{ctx: ctx, now: now}, in.Queries, in.Start.Unix(), in.End.Unix())
	if err != nil {
		return cwMetricDataPage{}, &cwMetricDataFailure{"ValidationError", err.Error()}
	}
	expression := false
	for _, q := range in.Queries {
		if q.Expression != "" {
			expression = true
		}
	}
	signature := cwMetricDataSignature(in)
	offset := 0
	if in.NextToken != "" {
		if expression {
			return cwMetricDataPage{}, &cwMetricDataFailure{"InvalidNextToken", "The service returned an invalid NextToken."}
		}
		parsed, ok := cwParseMetricDataToken(in.NextToken, signature)
		if !ok {
			return cwMetricDataPage{}, &cwMetricDataFailure{"InvalidNextToken", "The service returned an invalid NextToken."}
		}
		offset = parsed
	}

	descending := in.ScanBy != "TimestampAscending"
	page := cwMetricDataPage{Results: make([]cwMetricDataOutput, 0, len(results))}
	position, taken := 0, 0
	more := false
	for _, res := range results {
		label, err := cwExpandLabel(res, zone, now)
		if err != nil {
			return cwMetricDataPage{}, &cwMetricDataFailure{"ValidationError", fmt.Sprintf("Error in label of %s: %v", res.Id, err)}
		}
		o := cwMetricDataOutput{Id: res.Id, Label: label, StatusCode: res.Series.Status, Messages: res.Series.Messages, Timestamps: []time.Time{}, Values: []float64{}}
		partial := false
		for _, t := range res.Series.times(descending) {
			if position < offset {
				position++
				continue
			}
			if taken >= limit {
				partial = true
				continue
			}
			o.Timestamps = append(o.Timestamps, time.Unix(t, 0).UTC())
			o.Values = append(o.Values, res.Series.Points[t])
			position++
			taken++
		}
		if partial {
			more = true
			if o.StatusCode == "" {
				o.StatusCode = "PartialData"
			}
		}
		if o.StatusCode == "" {
			o.StatusCode = "Complete"
		}
		page.Results = append(page.Results, o)
	}
	if more && !expression {
		page.NextToken = cwMetricDataToken(position, signature)
	}
	return page, nil
}

// cwMetricDataSignature binds a NextToken to the request it pages.
func cwMetricDataSignature(in cwGetMetricDataInput) string {
	body, _ := json.Marshal(struct {
		Queries       []CWMetricDataQuery
		Start, End    int64
		ScanBy        string
		MaxDatapoints int
		Timezone      string
	}{in.Queries, in.Start.Unix(), in.End.Unix(), in.ScanBy, in.MaxDatapoints, in.Timezone})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:8])
}

func cwMetricDataToken(position int, signature string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(position) + ":" + signature))
}

func cwParseMetricDataToken(token, signature string) (int, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false
	}
	position, sig, ok := strings.Cut(string(raw), ":")
	if !ok || sig != signature {
		return 0, false
	}
	n, err := strconv.Atoi(position)
	return n, err == nil && n > 0
}

var cwDynamicLabelPattern = regexp.MustCompile(`\$\{([A-Z_]+|PROP\('([^']*)'\))\}`)

const cwMaxDynamicLabelValues = 6

// cwExpandLabel expands the dynamic values in a query's label over the
// series' whole time range; a label without one keeps the default label.
func cwExpandLabel(res cwMetricResult, zone *time.Location, now time.Time) (string, error) {
	if !strings.Contains(res.Template, "${") {
		return res.Label, nil
	}
	matches := cwDynamicLabelPattern.FindAllStringSubmatch(res.Template, -1)
	if len(matches) > cwMaxDynamicLabelValues {
		return "", fmt.Errorf("a label holds at most %d dynamic values", cwMaxDynamicLabelValues)
	}
	labels := 0
	for _, m := range matches {
		if m[0] == "${LABEL}" {
			labels++
		}
	}
	if labels > 1 {
		return "", fmt.Errorf("a label uses ${LABEL} at most once")
	}
	s := res.Series
	times := s.times(false)
	var unknown error
	out := cwDynamicLabelPattern.ReplaceAllStringFunc(res.Template, func(token string) string {
		m := cwDynamicLabelPattern.FindStringSubmatch(token)
		if strings.HasPrefix(m[1], "PROP(") {
			v, err := cwLabelProperty(s, m[2])
			if err != nil {
				unknown = err
			}
			return v
		}
		if m[1] == "LABEL" {
			return res.DefaultLabel
		}
		if m[1] == "DATAPOINT_COUNT" {
			return strconv.Itoa(len(times))
		}
		v, ok := cwLabelStatistic(m[1], s, times, zone, now)
		if !ok {
			unknown = fmt.Errorf("%s is not a dynamic label value", token)
		}
		return v
	})
	return out, unknown
}

func cwLabelStatistic(name string, s cwMetricSeries, times []int64, zone *time.Location, now time.Time) (string, bool) {
	known := map[string]bool{"AVG": true, "FIRST": true, "FIRST_LAST_RANGE": true, "FIRST_LAST_TIME_RANGE": true, "FIRST_TIME": true,
		"FIRST_TIME_RELATIVE": true, "LAST": true, "LAST_TIME": true, "LAST_TIME_RELATIVE": true, "MAX": true, "MAX_TIME": true,
		"MAX_TIME_RELATIVE": true, "MIN": true, "MIN_MAX_RANGE": true, "MIN_MAX_TIME_RANGE": true, "MIN_TIME": true,
		"MIN_TIME_RELATIVE": true, "SUM": true}
	if !known[name] {
		return "", false
	}
	if len(times) == 0 {
		return "", true
	}
	first, last := times[0], times[len(times)-1]
	minT, maxT := first, first
	sum := 0.0
	for _, t := range times {
		v := s.Points[t]
		sum += v
		if v < s.Points[minT] {
			minT = t
		}
		if v > s.Points[maxT] {
			maxT = t
		}
	}
	number := func(v float64) string { return cwLabelNumber(v) }
	stamp := func(t int64) string { return time.Unix(t, 0).In(zone).Format("2006-01-02 15:04:05 -0700") }
	relative := func(t int64) string { return cwLabelDuration(now.Sub(time.Unix(t, 0))) }
	switch name {
	case "AVG":
		return number(sum / float64(len(times))), true
	case "SUM":
		return number(sum), true
	case "FIRST":
		return number(s.Points[first]), true
	case "LAST":
		return number(s.Points[last]), true
	case "MIN":
		return number(s.Points[minT]), true
	case "MAX":
		return number(s.Points[maxT]), true
	case "FIRST_LAST_RANGE":
		return number(math.Abs(s.Points[last] - s.Points[first])), true
	case "MIN_MAX_RANGE":
		return number(s.Points[maxT] - s.Points[minT]), true
	case "FIRST_TIME":
		return stamp(first), true
	case "LAST_TIME":
		return stamp(last), true
	case "MIN_TIME":
		return stamp(minT), true
	case "MAX_TIME":
		return stamp(maxT), true
	case "FIRST_TIME_RELATIVE":
		return relative(first), true
	case "LAST_TIME_RELATIVE":
		return relative(last), true
	case "MIN_TIME_RELATIVE":
		return relative(minT), true
	case "MAX_TIME_RELATIVE":
		return relative(maxT), true
	case "FIRST_LAST_TIME_RANGE":
		return cwLabelDuration(time.Duration(last-first) * time.Second), true
	case "MIN_MAX_TIME_RANGE":
		d := maxT - minT
		if d < 0 {
			d = -d
		}
		return cwLabelDuration(time.Duration(d) * time.Second), true
	}
	return "", false
}

func cwLabelNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// cwLabelDuration writes an absolute time difference in days, hours,
// minutes and seconds, leaving out the leading units that are zero.
func cwLabelDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	total := int64(d / time.Second)
	parts := []string{}
	for _, unit := range []struct {
		seconds int64
		suffix  string
	}{{86400, "d"}, {3600, "h"}, {60, "m"}, {1, "s"}} {
		n := total / unit.seconds
		total -= n * unit.seconds
		if n > 0 || len(parts) > 0 || unit.suffix == "s" {
			parts = append(parts, strconv.FormatInt(n, 10)+unit.suffix)
		}
	}
	return strings.Join(parts, " ")
}

func cwLabelProperty(s cwMetricSeries, property string) (string, error) {
	switch {
	case property == "AccountId":
		return awsAccountID(), nil
	case property == "AccountLabel":
		return "", nil
	case property == "Region":
		return awsRegion(), nil
	case property == "Period":
		return strconv.Itoa(int(s.Period)), nil
	case property == "MetricName":
		if s.Metric != nil {
			return s.Metric.MetricName, nil
		}
		return "", nil
	case property == "Namespace":
		if s.Metric != nil {
			return s.Metric.Namespace, nil
		}
		return "", nil
	case property == "Stat":
		return s.Stat, nil
	case strings.HasPrefix(property, "Dim."):
		if s.Metric != nil {
			for _, d := range s.Metric.Dimensions {
				if d.Name == strings.TrimPrefix(property, "Dim.") {
					return d.Value, nil
				}
			}
		}
		return "", nil
	}
	return "", fmt.Errorf("PROP('%s') is not a metric property", property)
}
