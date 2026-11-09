package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// CloudWatch Logs forwards the events a subscription filter matches to its
// destination as logs.amazonaws.com, in the gzipped DATA_MESSAGE envelope the
// subscription guide documents: Amazon Kinesis Data Streams and Amazon Data
// Firehose as the filter's role, AWS Lambda through the function's resource
// policy, and a cross-account destination as the destination's role.

type CWSubscriptionFilter struct {
	FilterName             string `json:"filterName"`
	FilterPattern          string `json:"filterPattern"`
	LogGroupName           string `json:"logGroupName"`
	DestinationArn         string `json:"destinationArn"`
	RoleArn                string `json:"roleArn,omitempty"`
	Distribution           string `json:"distribution,omitempty"`
	ApplyOnTransformedLogs bool   `json:"applyOnTransformedLogs"`
	CreationTime           int64  `json:"creationTime"`
	// DisabledUntil is when a filter whose destination refused a delivery
	// resumes forwarding.
	DisabledUntil int64 `json:"disabledUntil,omitempty"`
}

type cwSubscriptionFilterWire struct {
	FilterName             string `json:"filterName"`
	LogGroupName           string `json:"logGroupName"`
	FilterPattern          string `json:"filterPattern"`
	DestinationArn         string `json:"destinationArn"`
	RoleArn                string `json:"roleArn,omitempty"`
	Distribution           string `json:"distribution,omitempty"`
	ApplyOnTransformedLogs bool   `json:"applyOnTransformedLogs"`
	CreationTime           int64  `json:"creationTime"`
}

func (f CWSubscriptionFilter) wire() cwSubscriptionFilterWire {
	return cwSubscriptionFilterWire{
		FilterName:             f.FilterName,
		LogGroupName:           f.LogGroupName,
		FilterPattern:          f.FilterPattern,
		DestinationArn:         f.DestinationArn,
		RoleArn:                f.RoleArn,
		Distribution:           f.Distribution,
		ApplyOnTransformedLogs: f.ApplyOnTransformedLogs,
		CreationTime:           f.CreationTime,
	}
}

const (
	cwSubscriptionFiltersPerGroup = 5
	cwSubscriptionMaxEventAge     = 24 * time.Hour
	cwSubscriptionDisabledFor     = 10 * time.Minute
)

type cwSubscriptionMessage struct {
	Owner               string                   `json:"owner"`
	LogGroup            string                   `json:"logGroup"`
	LogStream           string                   `json:"logStream"`
	SubscriptionFilters []string                 `json:"subscriptionFilters"`
	MessageType         string                   `json:"messageType"`
	LogEvents           []cwSubscriptionLogEvent `json:"logEvents"`
}

type cwSubscriptionLogEvent struct {
	ID        string `json:"id"`
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}

// One forwarded batch, with the destination as the filter named it when the
// events arrived.
type cwSubscriptionDelivery struct {
	LogGroupName   string `json:"logGroupName"`
	FilterName     string `json:"filterName"`
	DestinationArn string `json:"destinationArn"`
	RoleArn        string `json:"roleArn,omitempty"`
	PartitionKey   string `json:"partitionKey"`
	Data           []byte `json:"data"`
}

type cwSubscriptionTarget struct {
	Kind string
	Arn  string
	Role string
}

const (
	cwTargetKinesis  = "Kinesis"
	cwTargetFirehose = "Firehose"
	cwTargetLambda   = "Lambda"
)

var (
	cwSubscriptionFilters    sim.Store[CWSubscriptionFilter]
	cwSubscriptionsByGroup   sim.GenerationIndex[CWSubscriptionFilter]
	cwSubscriptionDeliveries *delivery.Dispatcher[cwSubscriptionDelivery]
)

func registerCloudWatchLogsSubscriptions(r *AWSRouter, srv *sim.Server) {
	cwSubscriptionFilters = sim.MakeStore[CWSubscriptionFilter](srv.DB(), "cw_subscription_filters")
	store := sim.MakeStore[delivery.Item[cwSubscriptionDelivery]](srv.DB(), "cw_subscription_deliveries")
	cwSubscriptionDeliveries = delivery.New(srv, "CloudWatch Logs subscription deliveries", store, delivery.Handler[cwSubscriptionDelivery]{
		Policy: func(cwSubscriptionDelivery) delivery.Policy {
			return delivery.Policy{MaxAge: cwSubscriptionMaxEventAge, Backoff: delivery.Exponential(time.Second, 5*time.Minute)}
		},
		Attempt: func(ctx context.Context, item *delivery.Item[cwSubscriptionDelivery]) delivery.Outcome {
			d := item.Payload
			target, refused := cwResolveSubscriptionTarget(d.DestinationArn, d.RoleArn)
			if refused != nil {
				return delivery.Permanent(refused)
			}
			return cwSendToSubscriptionTarget(ctx, cwSubscriptionSource(d.LogGroupName), target, d.PartitionKey, d.Data)
		},
		Finish: cwFinishSubscriptionDelivery,
	})
	cwSubscriptionDeliveries.Resume()

	r.Register("Logs_20140328.PutSubscriptionFilter", handleCWPutSubscriptionFilter)
	r.Register("Logs_20140328.DescribeSubscriptionFilters", handleCWDescribeSubscriptionFilters)
	r.Register("Logs_20140328.DeleteSubscriptionFilter", handleCWDeleteSubscriptionFilter)
}

func cwGroupSubscriptionFilters(group string) []CWSubscriptionFilter {
	return cwSubscriptionsByGroup.LookupAll(cwSubscriptionFilters, group, func(f CWSubscriptionFilter) []string {
		return []string{f.LogGroupName}
	})
}

// A filter whose destination refused a delivery with a non-retryable error
// skips events for ten minutes, as the subscription guide documents.
func cwFinishSubscriptionDelivery(item delivery.Item[cwSubscriptionDelivery], reason delivery.Reason) {
	if reason != delivery.Rejected {
		return
	}
	d := item.Payload
	until := time.Now().Add(cwSubscriptionDisabledFor).UnixMilli()
	cwSubscriptionFilters.Update(cwFilterKey(d.LogGroupName, d.FilterName), func(f *CWSubscriptionFilter) {
		if f.DestinationArn == d.DestinationArn {
			f.DisabledUntil = until
		}
	})
}

func cwInvalidParameter(format string, args ...any) *awsCallError {
	return &awsCallError{Status: http.StatusBadRequest, Code: "InvalidParameterException", Message: fmt.Sprintf(format, args...)}
}

func cwWriteRefusal(w http.ResponseWriter, refused *awsCallError) {
	AWSError(w, refused.Code, refused.Message, refused.Status)
}

func cwResolveSubscriptionTarget(destinationArn, roleArn string) (cwSubscriptionTarget, *awsCallError) {
	parts := strings.SplitN(destinationArn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return cwSubscriptionTarget{}, cwInvalidParameter("Invalid destinationArn: %s", destinationArn)
	}
	service, resource := parts[2], parts[5]
	switch {
	case service == "kinesis" && strings.HasPrefix(resource, "stream/"):
		return cwSubscriptionTarget{Kind: cwTargetKinesis, Arn: destinationArn, Role: roleArn}, nil
	case service == "firehose" && strings.HasPrefix(resource, "deliverystream/"):
		return cwSubscriptionTarget{Kind: cwTargetFirehose, Arn: destinationArn, Role: roleArn}, nil
	case service == "lambda" && strings.HasPrefix(resource, "function:"):
		return cwSubscriptionTarget{Kind: cwTargetLambda, Arn: destinationArn}, nil
	case service == "logs" && strings.HasPrefix(resource, "destination:"):
		dest, ok := cwDestinations.Get(strings.TrimPrefix(resource, "destination:"))
		if !ok || dest.Arn != destinationArn {
			return cwSubscriptionTarget{}, &awsCallError{Status: http.StatusBadRequest, Code: "ResourceNotFoundException",
				Message: "The specified destination does not exist: " + destinationArn}
		}
		target, refused := cwResolveSubscriptionTarget(dest.TargetArn, dest.RoleArn)
		if refused == nil && target.Kind == cwTargetLambda {
			refused = cwInvalidParameter("Invalid destination target: %s", dest.TargetArn)
		}
		return target, refused
	}
	return cwSubscriptionTarget{}, cwInvalidParameter("Invalid destinationArn: %s", destinationArn)
}

// The subscription guide grants a function to the log group's ARN with its
// trailing ":*".
func cwSubscriptionSource(logGroupName string) string {
	return cwLogGroupArn(logGroupName) + ":*"
}

func cwSendToSubscriptionTarget(ctx context.Context, source string, target cwSubscriptionTarget, partitionKey string, data []byte) delivery.Outcome {
	actor := awsActor{Service: "logs.amazonaws.com", Source: source, Role: target.Role}
	encoded := base64.StdEncoding.EncodeToString(data)
	switch target.Kind {
	case cwTargetKinesis:
		status, body, err := awsCallJSON(ctx, actor, "kinesis", "Kinesis_20131202.PutRecord", "application/x-amz-json-1.1",
			map[string]any{"StreamARN": target.Arn, "PartitionKey": partitionKey, "Data": encoded})
		return ebCallResult(status, body, err, false)
	case cwTargetFirehose:
		status, body, err := awsCallJSON(ctx, actor, "firehose", "Firehose_20150804.PutRecord", "application/x-amz-json-1.1",
			map[string]any{
				"DeliveryStreamName": target.Arn[strings.LastIndex(target.Arn, "/")+1:],
				"Record":             map[string]any{"Data": encoded},
			})
		return ebCallResult(status, body, err, false)
	default:
		event, err := json.Marshal(map[string]any{"awslogs": map[string]string{"data": encoded}})
		if err != nil {
			return delivery.Permanent(err)
		}
		header := http.Header{"X-Amz-Invocation-Type": {"Event"}, "Content-Type": {"application/json"}}
		status, body, err := awsCall(ctx, actor, "lambda", http.MethodPost,
			"/2015-03-31/functions/"+url.PathEscape(target.Arn)+"/invocations", header, event)
		return ebCallResult(status, body, err, false)
	}
}

// Before it accepts a filter, CloudWatch Logs proves it can reach the
// destination: a CONTROL_MESSAGE to a stream, a DryRun invocation of a
// function.
func cwCheckSubscriptionTarget(ctx context.Context, source string, target cwSubscriptionTarget) *awsCallError {
	if target.Kind == cwTargetLambda {
		header := http.Header{"X-Amz-Invocation-Type": {"DryRun"}, "Content-Type": {"application/json"}}
		status, _, err := awsCall(ctx, awsActor{Service: "logs.amazonaws.com", Source: source}, "lambda", http.MethodPost,
			"/2015-03-31/functions/"+url.PathEscape(target.Arn)+"/invocations", header, []byte("{}"))
		if err != nil || status >= 400 {
			return cwInvalidParameter("Could not execute the lambda function. Make sure you have given CloudWatch Logs permission to execute your function.")
		}
		return nil
	}
	data := cwGzipSubscriptionMessage(cwSubscriptionMessage{
		Owner:               "CloudwatchLogs",
		SubscriptionFilters: []string{},
		MessageType:         "CONTROL_MESSAGE",
		LogEvents: []cwSubscriptionLogEvent{{
			Timestamp: time.Now().UnixMilli(),
			Message:   "CWL CONTROL MESSAGE: Checking health of destination " + target.Kind + " stream.",
		}},
	})
	if outcome := cwSendToSubscriptionTarget(ctx, source, target, sim.NewUUID(), data); !outcome.OK() {
		return cwInvalidParameter("Could not deliver test message to specified %s stream. Check if the given %s stream is in ACTIVE state.", target.Kind, strings.ToLower(target.Kind))
	}
	return nil
}

// The Amazon Data Firehose example documents gzip level 6. Neither the
// envelope's encoding nor a write to memory can fail.
func cwGzipSubscriptionMessage(message cwSubscriptionMessage) []byte {
	raw, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, 6)
	if err != nil {
		panic(err)
	}
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return buf.Bytes()
}

var cwEventIDFloor = new(big.Int).Exp(big.NewInt(10), big.NewInt(55), nil)

// The guide's event IDs are 56 decimal digits.
func cwSubscriptionEventID() string {
	span := new(big.Int).Sub(new(big.Int).Mul(cwEventIDFloor, big.NewInt(10)), cwEventIDFloor)
	n, err := rand.Int(rand.Reader, span)
	if err != nil {
		panic(err)
	}
	return n.Add(n, cwEventIDFloor).String()
}

func cwSubscriptionPartitionKey(f CWSubscriptionFilter, logStream string) string {
	if f.Distribution == "Random" {
		return sim.NewUUID()
	}
	sum := sha256.Sum256([]byte(f.LogGroupName + "\x00" + logStream))
	return hex.EncodeToString(sum[:])
}

// Each filter forwards the events of one ingested batch that it matches, the
// transformed versions where it applies to transformed logs.
func cwDeliverSubscriptions(group, stream string, events []CWLogEvent) {
	filters := cwGroupSubscriptionFilters(group)
	if len(filters) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	ids := make([]string, len(events))
	for _, f := range filters {
		if f.DisabledUntil > now {
			continue
		}
		pattern, err := cwCompileLogPattern(f.FilterPattern)
		if err != nil {
			continue
		}
		var matched []cwSubscriptionLogEvent
		for i, e := range events {
			message := e.Message
			if f.ApplyOnTransformedLogs {
				message = e.processedMessage()
			}
			if !pattern.match(message) {
				continue
			}
			if ids[i] == "" {
				ids[i] = cwSubscriptionEventID()
			}
			matched = append(matched, cwSubscriptionLogEvent{ID: ids[i], Timestamp: e.Timestamp, Message: message})
		}
		if len(matched) == 0 {
			continue
		}
		data := cwGzipSubscriptionMessage(cwSubscriptionMessage{
			Owner:               awsAccountID(),
			LogGroup:            group,
			LogStream:           stream,
			SubscriptionFilters: []string{f.FilterName},
			MessageType:         "DATA_MESSAGE",
			LogEvents:           matched,
		})
		cwSubscriptionDeliveries.Submit(sim.NewUUID(), cwSubscriptionDelivery{
			LogGroupName:   group,
			FilterName:     f.FilterName,
			DestinationArn: f.DestinationArn,
			RoleArn:        f.RoleArn,
			PartitionKey:   cwSubscriptionPartitionKey(f, stream),
			Data:           data,
		})
	}
}

func handleCWPutSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName           string `json:"logGroupName"`
		FilterName             string `json:"filterName"`
		FilterPattern          string `json:"filterPattern"`
		DestinationArn         string `json:"destinationArn"`
		RoleArn                string `json:"roleArn"`
		Distribution           string `json:"distribution"`
		ApplyOnTransformedLogs bool   `json:"applyOnTransformedLogs"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.FilterName == "" || req.DestinationArn == "" {
		AWSError(w, "InvalidParameterException", "logGroupName, filterName and destinationArn are required", http.StatusBadRequest)
		return
	}
	group, ok := cwLogGroups.Get(req.LogGroupName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	if group.class() != "STANDARD" {
		AWSErrorf(w, "InvalidOperationException", http.StatusBadRequest,
			"Subscription filters are supported only for log groups in the Standard log class: %s", req.LogGroupName)
		return
	}
	if _, err := cwCompileLogPattern(req.FilterPattern); err != nil {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Invalid filter pattern: %s", err.Error())
		return
	}
	key := cwFilterKey(req.LogGroupName, req.FilterName)
	existing, exists := cwSubscriptionFilters.Get(key)
	if !exists && len(cwGroupSubscriptionFilters(req.LogGroupName)) >= cwSubscriptionFiltersPerGroup {
		AWSError(w, "LimitExceededException", "Resource limit exceeded.", http.StatusBadRequest)
		return
	}
	target, refused := cwResolveSubscriptionTarget(req.DestinationArn, req.RoleArn)
	if refused == nil {
		refused = cwCheckSubscriptionTarget(r.Context(), cwSubscriptionSource(req.LogGroupName), target)
	}
	if refused != nil {
		cwWriteRefusal(w, refused)
		return
	}
	creation := time.Now().UnixMilli()
	if exists {
		creation = existing.CreationTime
	}
	distribution := req.Distribution
	if distribution == "" {
		distribution = "ByLogStream"
	}
	cwSubscriptionFilters.Put(key, CWSubscriptionFilter{
		FilterName:             req.FilterName,
		FilterPattern:          req.FilterPattern,
		LogGroupName:           req.LogGroupName,
		DestinationArn:         req.DestinationArn,
		RoleArn:                req.RoleArn,
		Distribution:           distribution,
		ApplyOnTransformedLogs: req.ApplyOnTransformedLogs,
		CreationTime:           creation,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWDescribeSubscriptionFilters(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
		Limit            int    `json:"limit"`
		NextToken        string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName is required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	filters := []CWSubscriptionFilter{}
	for _, f := range cwGroupSubscriptionFilters(req.LogGroupName) {
		if strings.HasPrefix(f.FilterName, req.FilterNamePrefix) {
			filters = append(filters, f)
		}
	}
	page, next, pageOK := awsKeyPage(w, logsBadToken, filters, func(it CWSubscriptionFilter) string { return it.FilterName }, req.NextToken, req.Limit, 50)
	if !pageOK {
		return
	}
	wire := make([]cwSubscriptionFilterWire, 0, len(page))
	for _, f := range page {
		wire = append(wire, f.wire())
	}
	out := map[string]any{"subscriptionFilters": wire}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleCWDeleteSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName string `json:"logGroupName"`
		FilterName   string `json:"filterName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LogGroupName == "" || req.FilterName == "" {
		AWSError(w, "InvalidParameterException", "logGroupName and filterName are required", http.StatusBadRequest)
		return
	}
	if _, ok := cwLogGroups.Get(req.LogGroupName); !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified log group does not exist: %s", req.LogGroupName)
		return
	}
	if !cwSubscriptionFilters.Delete(cwFilterKey(req.LogGroupName, req.FilterName)) {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"The specified subscription filter does not exist: %s", req.FilterName)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
