package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cwQueryMetricDataQueries reads a query-protocol MetricDataQueries list.
func cwQueryMetricDataQueries(r *http.Request, prefix string) ([]CWMetricDataQuery, string) {
	var out []CWMetricDataQuery
	for i := 1; ; i++ {
		base := fmt.Sprintf("%s.member.%d", prefix, i)
		id := r.FormValue(base + ".Id")
		if id == "" {
			return out, ""
		}
		q := CWMetricDataQuery{
			Id:         id,
			Expression: r.FormValue(base + ".Expression"),
			Label:      r.FormValue(base + ".Label"),
			AccountId:  r.FormValue(base + ".AccountId"),
		}
		if v := r.FormValue(base + ".ReturnData"); v != "" {
			b := v == "true"
			q.ReturnData = &b
		}
		if v := r.FormValue(base + ".Period"); v != "" {
			p, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil, fmt.Sprintf("The parameter %s.Period must be an integer.", base)
			}
			q.Period = int32(p)
		}
		if ns := r.FormValue(base + ".MetricStat.Metric.Namespace"); ns != "" || r.FormValue(base+".MetricStat.Metric.MetricName") != "" {
			ms := &CWMetricStat{
				Metric: CWMetricRef{
					Namespace:  ns,
					MetricName: r.FormValue(base + ".MetricStat.Metric.MetricName"),
					Dimensions: cwQueryDimensions(r, base+".MetricStat.Metric.Dimensions"),
				},
				Stat: r.FormValue(base + ".MetricStat.Stat"),
				Unit: r.FormValue(base + ".MetricStat.Unit"),
			}
			if v := r.FormValue(base + ".MetricStat.Period"); v != "" {
				p, err := strconv.ParseInt(v, 10, 32)
				if err != nil {
					return nil, fmt.Sprintf("The parameter %s.MetricStat.Period must be an integer.", base)
				}
				ms.Period = int32(p)
			}
			q.MetricStat = ms
		}
		out = append(out, q)
	}
}

func cwMetricDataQueriesXML(element string, queries []CWMetricDataQuery) string {
	if len(queries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<" + element + ">")
	for _, q := range queries {
		b.WriteString("<member>")
		fmt.Fprintf(&b, "<Id>%s</Id>", xmlEscape(q.Id))
		if ms := q.MetricStat; ms != nil {
			fmt.Fprintf(&b, "<MetricStat><Metric><Namespace>%s</Namespace><MetricName>%s</MetricName><Dimensions>",
				xmlEscape(ms.Metric.Namespace), xmlEscape(ms.Metric.MetricName))
			for _, d := range ms.Metric.Dimensions {
				fmt.Fprintf(&b, "<member><Name>%s</Name><Value>%s</Value></member>", xmlEscape(d.Name), xmlEscape(d.Value))
			}
			fmt.Fprintf(&b, "</Dimensions></Metric><Period>%d</Period><Stat>%s</Stat>", ms.Period, xmlEscape(ms.Stat))
			if ms.Unit != "" {
				fmt.Fprintf(&b, "<Unit>%s</Unit>", xmlEscape(ms.Unit))
			}
			b.WriteString("</MetricStat>")
		}
		if q.Expression != "" {
			fmt.Fprintf(&b, "<Expression>%s</Expression>", xmlEscape(q.Expression))
		}
		if q.Label != "" {
			fmt.Fprintf(&b, "<Label>%s</Label>", xmlEscape(q.Label))
		}
		if q.ReturnData != nil {
			fmt.Fprintf(&b, "<ReturnData>%t</ReturnData>", *q.ReturnData)
		}
		if q.Period > 0 {
			fmt.Fprintf(&b, "<Period>%d</Period>", q.Period)
		}
		if q.AccountId != "" {
			fmt.Fprintf(&b, "<AccountId>%s</AccountId>", xmlEscape(q.AccountId))
		}
		b.WriteString("</member>")
	}
	b.WriteString("</" + element + ">")
	return b.String()
}

// cwMetricDataOutput is one MetricDataResult in protocol-neutral form, its
// points ordered as ScanBy asks (newest first by default).
type cwMetricDataOutput struct {
	Id         string
	Label      string
	StatusCode string
	Messages   []cwMessage
	Timestamps []time.Time
	Values     []float64
}

func cwGetMetricData(ctx context.Context, queries []CWMetricDataQuery, start, end time.Time, scanBy string) ([]cwMetricDataOutput, error) {
	results, err := cwEvaluateMetricQueries(cwMathOptions{ctx: ctx}, queries, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}
	out := make([]cwMetricDataOutput, 0, len(results))
	for _, res := range results {
		o := cwMetricDataOutput{Id: res.Id, Label: res.Label, StatusCode: res.Series.Status, Messages: res.Series.Messages, Timestamps: []time.Time{}, Values: []float64{}}
		if o.StatusCode == "" {
			o.StatusCode = "Complete"
		}
		for _, t := range res.Series.times(scanBy != "TimestampAscending") {
			o.Timestamps = append(o.Timestamps, time.Unix(t, 0).UTC())
			o.Values = append(o.Values, res.Series.Points[t])
		}
		out = append(out, o)
	}
	return out, nil
}
