package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LAMBDA("FunctionName" [, arg]*) queries a custom data source, as "Create a
// custom connector to a data source" defines the exchange: CloudWatch invokes
// the function, which grants lambda.datasource.cloudwatch.amazonaws.com
// lambda:InvokeFunction, with a GetMetricData event, and reads back
// MetricDataResults or an Error. The function has 10 seconds (4.5 for an
// alarm) and 6 MB of response; datapoints off the requested start and period
// grid, or outside the range, are discarded.

const (
	cwDataSourcePrincipal     = "lambda.datasource.cloudwatch.amazonaws.com"
	cwDataSourceMaxArguments  = 20
	cwDataSourceMaxResponse   = 6 << 20
	cwDataSourceGraphDeadline = 10 * time.Second
	cwDataSourceAlarmDeadline = 4500 * time.Millisecond
)

type cwDataSourceResponse struct {
	MetricDataResults []struct {
		StatusCode string    `json:"StatusCode"`
		Label      string    `json:"Label"`
		Timestamps []float64 `json:"Timestamps"`
		Values     []float64 `json:"Values"`
		Messages   []struct {
			Code  string `json:"Code"`
			Value string `json:"Value"`
		} `json:"Messages"`
	} `json:"MetricDataResults"`
	Error *struct {
		Code  string `json:"Code"`
		Value string `json:"Value"`
	} `json:"Error"`
}

func cwDataSourceFailure(label, status, message string) cwMathValue {
	return cwArrayValue([]cwMetricSeries{{Label: label, Points: map[int64]float64{}, Status: status, Messages: []cwMessage{{Code: status, Value: message}}}})
}

func (ev *cwMetricEvaluator) lambdaDataSource(n *cwMathNode, q CWMetricDataQuery) (cwMathValue, error) {
	if len(n.args) < 1 || n.args[0].kind != "str" {
		return cwMathValue{}, fmt.Errorf("LAMBDA takes the function name in quotes and optional arguments")
	}
	if len(n.args)-1 > cwDataSourceMaxArguments {
		return cwMathValue{}, fmt.Errorf("LAMBDA takes at most %d arguments after the function name", cwDataSourceMaxArguments)
	}
	name := n.args[0].name
	if strings.Contains(name, "*") {
		return cwMathValue{}, fmt.Errorf("the LAMBDA function name cannot use *")
	}
	arguments := make([]any, 0, len(n.args)-1)
	for _, a := range n.args[1:] {
		switch {
		case a.kind == "str":
			arguments = append(arguments, strings.ReplaceAll(a.name, "\n", " "))
		case a.kind == "num":
			arguments = append(arguments, a.num)
		case a.kind == "neg" && a.left.kind == "num":
			arguments = append(arguments, -a.left.num)
		case a.kind == "id" && (a.name == "true" || a.name == "false"):
			arguments = append(arguments, a.name == "true")
		default:
			return cwMathValue{}, fmt.Errorf("LAMBDA arguments are strings, numbers or Booleans")
		}
	}
	fn, _, ok := lambdaResolveInvocationTarget(name, "")
	if !ok {
		return cwMathValue{}, fmt.Errorf("the Lambda function %s does not exist", name)
	}
	if !iamAuthorizeServiceDelivery(fn.FunctionArn, "lambda:InvokeFunction", iamServiceSource{Service: cwDataSourcePrincipal, SourceAccount: awsAccountID()}) {
		return cwDataSourceFailure(name, "Forbidden", fmt.Sprintf("The function %s does not grant %s lambda:InvokeFunction.", name, cwDataSourcePrincipal)), nil
	}
	period := ev.periodOf(q)
	payload, err := json.Marshal(map[string]any{
		"EventType": "GetMetricData",
		"GetMetricDataRequest": map[string]any{
			"StartTime": ev.start,
			"EndTime":   ev.end,
			"Period":    period,
			"Arguments": arguments,
		},
	})
	if err != nil {
		return cwMathValue{}, err
	}
	deadline := cwDataSourceGraphDeadline
	if ev.opts.alarm {
		deadline = cwDataSourceAlarmDeadline
	}
	ctx, cancel := context.WithTimeout(ev.opts.ctx, deadline)
	defer cancel()
	out, unhandled, serviceErr := invokeLambdaViaRuntimeAPI(ctx, fn, payload)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return cwDataSourceFailure(name, "InternalError", fmt.Sprintf("The function %s did not complete within %s.", name, deadline)), nil
	case serviceErr != nil:
		return cwDataSourceFailure(name, "InternalError", serviceErr.Code+": "+serviceErr.Message), nil
	case unhandled:
		return cwDataSourceFailure(name, "InternalError", strings.TrimSpace(string(out))), nil
	case len(out) > cwDataSourceMaxResponse:
		return cwDataSourceFailure(name, "InternalError", "The function's response is larger than 6 MB."), nil
	}
	var resp cwDataSourceResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return cwDataSourceFailure(name, "InternalError", "The function's response is not a GetMetricData response: "+err.Error()), nil
	}
	if e := resp.Error; e != nil {
		switch e.Code {
		case "Validation":
			return cwMathValue{}, fmt.Errorf("%s", e.Value)
		case "Forbidden":
			return cwDataSourceFailure(name, "Forbidden", e.Value), nil
		}
		return cwDataSourceFailure(name, "InternalError", e.Value), nil
	}
	p := int64(period)
	series := make([]cwMetricSeries, 0, len(resp.MetricDataResults))
	for _, r := range resp.MetricDataResults {
		s := cwMetricSeries{Label: r.Label, Period: period, Points: map[int64]float64{}}
		switch r.StatusCode {
		case "", "Complete":
		case "InternalError", "PartialData", "Forbidden":
			s.Status = r.StatusCode
		default:
			return cwDataSourceFailure(name, "InternalError", fmt.Sprintf("The function returned the unknown StatusCode %s.", r.StatusCode)), nil
		}
		for _, m := range r.Messages {
			s.Messages = append(s.Messages, cwMessage{Code: m.Code, Value: m.Value})
		}
		if len(r.Timestamps) != len(r.Values) {
			return cwDataSourceFailure(name, "InternalError", "The function returned a different number of Timestamps and Values."), nil
		}
		for i, ts := range r.Timestamps {
			t := int64(ts)
			if float64(t) != ts || t < ev.start || t >= ev.end || (t-ev.start)%p != 0 {
				continue
			}
			s.Points[t] = r.Values[i]
		}
		series = append(series, s)
	}
	return cwArrayValue(series), nil
}
