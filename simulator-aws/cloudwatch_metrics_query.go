package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// CloudWatch metrics — query-protocol surface (the aws CLI / botocore path).
//
// The Go SDK speaks rpc-v2-cbor to CloudWatch (handled in cloudwatch_metrics.go
// under /service/GraniteServiceVersion...), but the aws CLI / botocore use the
// legacy query protocol (Action=..., form params, XML responses) — so
// `aws cloudwatch put-metric-data` / `get-metric-statistics` / `list-metrics`
// previously returned InvalidAction. These handlers serve that surface from the
// SAME `cwMetrics` store and the SAME period-bucketing + statistic helpers, so
// data pushed via either protocol reads back through either.

const cwQueryXmlns = `xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/"`

func registerCloudWatchMetricsQuery(r *sim.AWSQueryRouter) {
	r.Register("PutMetricData", handleCWQueryPutMetricData)
	r.Register("GetMetricStatistics", handleCWQueryGetMetricStatistics)
	r.Register("ListMetrics", handleCWQueryListMetrics)
}

func cwQueryDimensions(r *http.Request, prefix string) []CWDimension {
	var dims []CWDimension
	for i := 1; ; i++ {
		name := r.FormValue(fmt.Sprintf("%s.member.%d.Name", prefix, i))
		if name == "" {
			break
		}
		dims = append(dims, CWDimension{Name: name, Value: r.FormValue(fmt.Sprintf("%s.member.%d.Value", prefix, i))})
	}
	return dims
}

func handleCWQueryPutMetricData(w http.ResponseWriter, r *http.Request) {
	namespace := r.FormValue("Namespace")
	if namespace == "" {
		cwQueryError(w, "MissingParameter", "The parameter Namespace is required.")
		return
	}
	for i := 1; ; i++ {
		base := fmt.Sprintf("MetricData.member.%d", i)
		name := r.FormValue(base + ".MetricName")
		if name == "" {
			break
		}
		dims := cwQueryDimensions(r, base+".Dimensions")
		ts := time.Now().UTC()
		if t := r.FormValue(base + ".Timestamp"); t != "" {
			if parsed, err := time.Parse(time.RFC3339, t); err == nil {
				ts = parsed
			}
		}
		var value float64
		if v := r.FormValue(base + ".Value"); v != "" {
			pv, err := strconv.ParseFloat(v, 64)
			if err != nil {
				cwQueryError(w, "InvalidParameterValue", fmt.Sprintf("The parameter %s.Value is not a valid number.", base))
				return
			}
			value = pv
		}
		datum := CWMetricDatum{
			Namespace:  namespace,
			MetricName: name,
			Dimensions: dims,
			Value:      value,
			Timestamp:  float64(ts.Unix()),
			Unit:       r.FormValue(base + ".Unit"),
		}
		cwStoreDatum(datum)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<PutMetricDataResponse %s><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></PutMetricDataResponse>`,
		cwQueryXmlns, generateUUID())
}

func handleCWQueryGetMetricStatistics(w http.ResponseWriter, r *http.Request) {
	namespace := r.FormValue("Namespace")
	metricName := r.FormValue("MetricName")
	dims := cwQueryDimensions(r, "Dimensions")

	period := int64(60)
	if p := r.FormValue("Period"); p != "" {
		if n, err := strconv.ParseInt(p, 10, 64); err == nil && n > 0 {
			period = n
		}
	}
	startSec := cwParseTimeUnix(r.FormValue("StartTime"))
	endSec := cwParseTimeUnix(r.FormValue("EndTime"))

	var stats []string
	for i := 1; ; i++ {
		s := r.FormValue(fmt.Sprintf("Statistics.member.%d", i))
		if s == "" {
			break
		}
		stats = append(stats, s)
	}
	if len(stats) == 0 {
		stats = []string{"Average"}
	}

	data, _ := cwMetrics.Get(metricsKey(namespace, metricName, dims))
	unit := "None"
	buckets := map[int64][]float64{}
	for _, d := range data {
		if startSec != 0 && d.Timestamp < startSec {
			continue
		}
		if endSec != 0 && d.Timestamp >= endSec {
			continue
		}
		if d.Unit != "" {
			unit = d.Unit
		}
		b := (int64(d.Timestamp) / period) * period
		buckets[b] = append(buckets[b], d.Value)
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	var points strings.Builder
	for _, k := range keys {
		points.WriteString("<member>")
		fmt.Fprintf(&points, "<Timestamp>%s</Timestamp>", time.Unix(k, 0).UTC().Format(time.RFC3339))
		for _, s := range stats {
			fmt.Fprintf(&points, "<%s>%s</%s>", s, cwFormatFloat(cwApplyStat(s, buckets[k])), s)
		}
		fmt.Fprintf(&points, "<Unit>%s</Unit>", xmlEscape(unit))
		points.WriteString("</member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetMetricStatisticsResponse %s><GetMetricStatisticsResult><Label>%s</Label><Datapoints>%s</Datapoints></GetMetricStatisticsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></GetMetricStatisticsResponse>`,
		cwQueryXmlns, xmlEscape(metricName), points.String(), generateUUID())
}

func handleCWQueryListMetrics(w http.ResponseWriter, r *http.Request) {
	nsFilter := r.FormValue("Namespace")
	nameFilter := r.FormValue("MetricName")

	type metricKey struct {
		namespace, name, dims string
	}
	seen := map[metricKey]CWMetricDatum{}
	for _, series := range cwMetrics.List() {
		for _, d := range series {
			if nsFilter != "" && d.Namespace != nsFilter {
				continue
			}
			if nameFilter != "" && d.MetricName != nameFilter {
				continue
			}
			seen[metricKey{d.Namespace, d.MetricName, fmt.Sprintf("%v", d.Dimensions)}] = d
		}
	}
	metrics := make([]CWMetricDatum, 0, len(seen))
	for _, d := range seen {
		metrics = append(metrics, d)
	}
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].Namespace != metrics[j].Namespace {
			return metrics[i].Namespace < metrics[j].Namespace
		}
		return metrics[i].MetricName < metrics[j].MetricName
	})

	var members strings.Builder
	for _, d := range metrics {
		members.WriteString("<member>")
		fmt.Fprintf(&members, "<Namespace>%s</Namespace><MetricName>%s</MetricName>", xmlEscape(d.Namespace), xmlEscape(d.MetricName))
		members.WriteString("<Dimensions>")
		for _, dim := range d.Dimensions {
			fmt.Fprintf(&members, "<member><Name>%s</Name><Value>%s</Value></member>", xmlEscape(dim.Name), xmlEscape(dim.Value))
		}
		members.WriteString("</Dimensions></member>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListMetricsResponse %s><ListMetricsResult><Metrics>%s</Metrics></ListMetricsResult><ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></ListMetricsResponse>`,
		cwQueryXmlns, members.String(), generateUUID())
}

func cwParseTimeUnix(s string) float64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return float64(t.Unix())
	}
	return 0
}

func cwFormatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func cwQueryError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `<ErrorResponse %s><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		cwQueryXmlns, code, xmlEscape(message), generateUUID())
}
