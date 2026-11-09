package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	mathrand "math/rand/v2"
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
	FilterName             string   `json:"filterName"`
	FilterPattern          string   `json:"filterPattern"`
	LogGroupName           string   `json:"logGroupName"`
	DestinationArn         string   `json:"destinationArn"`
	RoleArn                string   `json:"roleArn,omitempty"`
	Distribution           string   `json:"distribution,omitempty"`
	ApplyOnTransformedLogs bool     `json:"applyOnTransformedLogs"`
	FieldSelectionCriteria string   `json:"fieldSelectionCriteria,omitempty"`
	EmitSystemFields       []string `json:"emitSystemFields,omitempty"`
	CreationTime           int64    `json:"creationTime"`
	// DisabledUntil is when a filter whose destination refused a delivery
	// resumes forwarding.
	DisabledUntil int64 `json:"disabledUntil,omitempty"`
}

type cwSubscriptionFilterWire struct {
	FilterName             string   `json:"filterName"`
	LogGroupName           string   `json:"logGroupName"`
	FilterPattern          string   `json:"filterPattern"`
	DestinationArn         string   `json:"destinationArn"`
	RoleArn                string   `json:"roleArn,omitempty"`
	Distribution           string   `json:"distribution,omitempty"`
	ApplyOnTransformedLogs bool     `json:"applyOnTransformedLogs"`
	FieldSelectionCriteria string   `json:"fieldSelectionCriteria,omitempty"`
	EmitSystemFields       []string `json:"emitSystemFields,omitempty"`
	CreationTime           int64    `json:"creationTime"`
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
		FieldSelectionCriteria: f.FieldSelectionCriteria,
		EmitSystemFields:       f.EmitSystemFields,
		CreationTime:           f.CreationTime,
	}
}

const (
	cwSubscriptionFiltersPerGroup = 5
	cwSubscriptionMaxEventAge     = 24 * time.Hour
	cwSubscriptionDisabledFor     = 10 * time.Minute

	// The account-level guide's envelope and the metrics page's PolicyLevel
	// dimension name an account-level policy; a log group-level filter's
	// envelope, in the log group-level guide, carries no policyLevel.
	cwPolicyLevelAccount          = "ACCOUNT_LEVEL_POLICY"
	cwPolicyLevelAccountDimension = "AccountPolicy"

	// Destination record limits: an Amazon Kinesis Data Streams record (data
	// and partition key) up to the stream's maximum record size, 1 MiB unless
	// raised; an Amazon Data Firehose record up to 1,000 KiB; an asynchronous
	// AWS Lambda invocation payload up to 1 MB.
	cwKinesisDefaultMaxRecordKiB = 1024
	cwFirehoseMaxRecordBytes     = 1000 * 1024
	cwLambdaMaxAsyncPayload      = 1024 * 1024
	cwLambdaEnvelopeBytes        = len(`{"awslogs":{"data":""}}`)
)

type cwSubscriptionMessage struct {
	Owner               string                   `json:"owner"`
	LogGroup            string                   `json:"logGroup"`
	LogStream           string                   `json:"logStream"`
	SubscriptionFilters []string                 `json:"subscriptionFilters"`
	MessageType         string                   `json:"messageType"`
	LogEvents           []cwSubscriptionLogEvent `json:"logEvents"`
	PolicyLevel         string                   `json:"policyLevel,omitempty"`
}

// emitSystemFields adds the named system fields to each forwarded event.
type cwSubscriptionLogEvent struct {
	ID        string `json:"id"`
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
	Account   string `json:"@aws.account,omitempty"`
	Region    string `json:"@aws.region,omitempty"`
	SourceLog string `json:"@source.log,omitempty"`
}

// One forwarded batch, with the destination as the filter or account policy
// named it when the events arrived.
type cwSubscriptionDelivery struct {
	LogGroupName    string `json:"logGroupName"`
	FilterName      string `json:"filterName"`
	PolicyLevel     string `json:"policyLevel,omitempty"`
	DestinationArn  string `json:"destinationArn"`
	DestinationType string `json:"destinationType"`
	RoleArn         string `json:"roleArn,omitempty"`
	PartitionKey    string `json:"partitionKey"`
	Events          int    `json:"events"`
	Data            []byte `json:"data"`
	Attempts        int    `json:"attempts"`
}

type cwSubscriptionTarget struct {
	Kind string
	Arn  string
	Role string
}

// The metrics page describes DestinationType's values as AWS Lambda, Amazon
// Kinesis Data Streams and Amazon Data Firehose without spelling them; the
// dimension carries the short names the destinations' ARNs and the
// subscription guide's control messages use.
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
		Policy: func(d cwSubscriptionDelivery) delivery.Policy {
			return delivery.Policy{MaxAge: cwSubscriptionMaxEventAge, MinWait: func(last delivery.Outcome) time.Duration {
				return cwSubscriptionRetryDelay(d.Attempts, last)
			}}
		},
		Attempt: func(ctx context.Context, item *delivery.Item[cwSubscriptionDelivery]) delivery.Outcome {
			item.Payload.Attempts = item.Attempts
			d := item.Payload
			target, refused := cwResolveSubscriptionTarget(d.DestinationArn, d.RoleArn)
			if refused != nil {
				cwPublishForwardingMetric(d, "DeliveryErrors", float64(d.Events), "None")
				return delivery.Permanent(refused)
			}
			outcome, throttled := cwSendToSubscriptionTarget(ctx, cwSubscriptionSource(d.LogGroupName), target, d.PartitionKey, d.Data)
			switch {
			case outcome.OK():
				cwPublishForwardingMetric(d, "ForwardedLogEvents", float64(d.Events), "None")
				cwPublishForwardingMetric(d, "ForwardedBytes", float64(len(d.Data)), "Bytes")
			case throttled:
				cwPublishForwardingMetric(d, "DeliveryThrottling", float64(d.Events), "None")
			default:
				cwPublishForwardingMetric(d, "DeliveryErrors", float64(d.Events), "None")
			}
			return outcome
		},
		Finish: cwFinishSubscriptionDelivery,
	})
	cwSubscriptionDeliveries.Resume()

	r.Register("Logs_20140328.PutSubscriptionFilter", handleCWPutSubscriptionFilter)
	r.Register("Logs_20140328.DescribeSubscriptionFilters", handleCWDescribeSubscriptionFilters)
	r.Register("Logs_20140328.DeleteSubscriptionFilter", handleCWDeleteSubscriptionFilter)
}

// The guides bound a retried delivery at 24 hours and give no cadence; the
// AWS SDKs and Tools Reference Guide's standard retry mode waits
// random(0, 1) x min(20 s, base x 2^retry), from a 1-second base after a
// throttling error and a 50-millisecond base after any other.
const (
	cwSubscriptionTransientBase = 50 * time.Millisecond
	cwSubscriptionThrottleBase  = time.Second
	cwSubscriptionMaxBackoff    = 20 * time.Second
)

func cwSubscriptionRetryDelay(retry int, last delivery.Outcome) time.Duration {
	base := cwSubscriptionTransientBase
	if failure, ok := last.Err.(ebTargetError); ok && cwThrottlingErrors[failure.Code] {
		base = cwSubscriptionThrottleBase
	}
	limit := cwSubscriptionMaxBackoff
	if retry < 20 {
		limit = min(limit, base<<retry)
	}
	return time.Duration(mathrand.Int64N(int64(limit) + 1))
}

// The error codes the AWS SDKs and Tools Reference Guide classifies as
// throttling.
var cwThrottlingErrors = map[string]bool{
	"Throttling": true, "ThrottlingException": true, "ThrottledException": true, "RequestThrottledException": true,
	"TooManyRequestsException": true, "ProvisionedThroughputExceededException": true, "TransactionInProgressException": true,
	"LimitExceededException": true, "PriorRequestNotComplete": true, "RequestThrottled": true, "EC2ThrottledException": true,
	"RequestLimitExceeded": true, "SlowDown": true, "BandwidthLimitExceeded": true,
}

// The metrics page converts a filter name to ASCII, replacing each character
// it cannot carry with a question mark.
func cwMetricFilterNameDimension(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// The metrics page lists LogGroupName, DestinationType and FilterName for a
// log group-level filter, and PolicyLevel, DestinationType and FilterName for
// an account-level policy; ForwardedBytes takes no PolicyLevel.
func cwPublishForwardingMetric(d cwSubscriptionDelivery, metric string, value float64, unit string) {
	dims := []CWDimension{
		{Name: "LogGroupName", Value: d.LogGroupName},
		{Name: "DestinationType", Value: d.DestinationType},
		{Name: "FilterName", Value: cwMetricFilterNameDimension(d.FilterName)},
	}
	if d.PolicyLevel != "" && metric != "ForwardedBytes" {
		dims[0] = CWDimension{Name: "PolicyLevel", Value: cwPolicyLevelAccountDimension}
	}
	cwStoreDatum(CWMetricDatum{
		Namespace:  "AWS/Logs",
		MetricName: metric,
		Dimensions: dims,
		Value:      value,
		Timestamp:  float64(time.Now().Unix()),
		Unit:       unit,
	})
}

func cwGroupSubscriptionFilters(group string) []CWSubscriptionFilter {
	return cwSubscriptionsByGroup.LookupAll(cwSubscriptionFilters, group, func(f CWSubscriptionFilter) []string {
		return []string{f.LogGroupName}
	})
}

// A filter or account policy whose destination refused a delivery with a
// non-retryable error skips events for ten minutes, as the subscription guide
// documents.
func cwFinishSubscriptionDelivery(item delivery.Item[cwSubscriptionDelivery], reason delivery.Reason) {
	if reason != delivery.Rejected {
		return
	}
	d := item.Payload
	until := time.Now().Add(cwSubscriptionDisabledFor).UnixMilli()
	if d.PolicyLevel != "" {
		cwAccountPolicies.Update(cwAccountPolicyKey(cwPolicySubscription, d.FilterName), func(p *CWAccountPolicy) {
			p.DisabledUntil = until
		})
		return
	}
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

// The subscription guide retries a throttling error or a 5xx for up to 24
// hours and stops on any other refusal. Kinesis throttles with
// ProvisionedThroughputExceededException, Lambda with 429
// TooManyRequestsException, and Firehose with 503 ServiceUnavailableException.
func cwSubscriptionCallResult(status int, body []byte, err error) (delivery.Outcome, bool) {
	if err != nil {
		if callErr, ok := err.(*awsCallError); ok {
			return delivery.Permanent(ebTargetError{callErr.Code, callErr.Message}).WithStatus(callErr.Status), false
		}
		return delivery.Retryable(err), false
	}
	if status < 400 {
		return delivery.Delivered(), false
	}
	code, message := awsJSONError(body)
	failure := ebTargetError{code, message}
	switch {
	case status == http.StatusTooManyRequests || cwThrottlingErrors[code] || code == "ServiceUnavailableException":
		return delivery.Retryable(failure).WithStatus(status), true
	case status >= 500:
		return delivery.Retryable(failure).WithStatus(status), false
	}
	return delivery.Permanent(failure).WithStatus(status), false
}

func cwSendToSubscriptionTarget(ctx context.Context, source string, target cwSubscriptionTarget, partitionKey string, data []byte) (delivery.Outcome, bool) {
	actor := awsActor{Service: "logs.amazonaws.com", Source: source, Role: target.Role}
	encoded := base64.StdEncoding.EncodeToString(data)
	switch target.Kind {
	case cwTargetKinesis:
		status, body, err := awsCallJSON(ctx, actor, "kinesis", "Kinesis_20131202.PutRecord", "application/x-amz-json-1.1",
			map[string]any{"StreamARN": target.Arn, "PartitionKey": partitionKey, "Data": encoded})
		return cwSubscriptionCallResult(status, body, err)
	case cwTargetFirehose:
		status, body, err := awsCallJSON(ctx, actor, "firehose", "Firehose_20150804.PutRecord", "application/x-amz-json-1.1",
			map[string]any{
				"DeliveryStreamName": target.Arn[strings.LastIndex(target.Arn, "/")+1:],
				"Record":             map[string]any{"Data": encoded},
			})
		return cwSubscriptionCallResult(status, body, err)
	default:
		event, err := json.Marshal(map[string]any{"awslogs": map[string]string{"data": encoded}})
		if err != nil {
			return delivery.Permanent(err), false
		}
		header := http.Header{"X-Amz-Invocation-Type": {"Event"}, "Content-Type": {"application/json"}}
		status, body, err := awsCall(ctx, actor, "lambda", http.MethodPost,
			"/2015-03-31/functions/"+url.PathEscape(target.Arn)+"/invocations", header, event)
		return cwSubscriptionCallResult(status, body, err)
	}
}

// Before it accepts a filter, CloudWatch Logs proves it can reach the
// destination: a CONTROL_MESSAGE to a stream, a DryRun invocation of a
// function. The guide gives the control message's type and purpose; its owner,
// empty log group, stream, filters and event ID, and its text, follow a
// published capture of one.
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
	destination := "Kinesis stream"
	if target.Kind == cwTargetFirehose {
		destination = "Firehose"
	}
	data := cwGzipSubscriptionMessage(cwSubscriptionMessage{
		Owner:               "CloudwatchLogs",
		SubscriptionFilters: []string{},
		MessageType:         "CONTROL_MESSAGE",
		LogEvents: []cwSubscriptionLogEvent{{
			Timestamp: time.Now().UnixMilli(),
			Message:   "CWL CONTROL MESSAGE: Checking health of destination " + destination + ".",
		}},
	})
	if outcome, _ := cwSendToSubscriptionTarget(ctx, source, target, sim.NewUUID(), data); !outcome.OK() {
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

// cwSubscriptionRecordLimit is the most gzipped bytes one record to target
// carries.
func cwSubscriptionRecordLimit(target cwSubscriptionTarget, partitionKey string) int {
	switch target.Kind {
	case cwTargetKinesis:
		kib := int64(cwKinesisDefaultMaxRecordKiB)
		if stream, ok := kinesisStreamByARN(target.Arn); ok && stream.MaxRecordSizeInKiB > 0 {
			kib = stream.MaxRecordSizeInKiB
		}
		return int(kib)*1024 - len(partitionKey)
	case cwTargetFirehose:
		return cwFirehoseMaxRecordBytes
	}
	return (cwLambdaMaxAsyncPayload - cwLambdaEnvelopeBytes) / 4 * 3
}

// cwSplitSubscriptionMessage gzips message, halving its events until each
// part fits limit; a single event too large for any record goes alone, for
// the destination to refuse.
func cwSplitSubscriptionMessage(message cwSubscriptionMessage, limit int) []cwSubscriptionMessage {
	data := cwGzipSubscriptionMessage(message)
	if len(data) <= limit || len(message.LogEvents) < 2 {
		return []cwSubscriptionMessage{message}
	}
	half := len(message.LogEvents) / 2
	first, second := message, message
	first.LogEvents = message.LogEvents[:half]
	second.LogEvents = message.LogEvents[half:]
	return append(cwSplitSubscriptionMessage(first, limit), cwSplitSubscriptionMessage(second, limit)...)
}

func cwSubscriptionPartitionKey(distribution, group, logStream string) string {
	if distribution == "Random" {
		return sim.NewUUID()
	}
	sum := sha256.Sum256([]byte(group + "\x00" + logStream))
	return hex.EncodeToString(sum[:])
}

// cwSubscriptionSpec is what one log group-level filter or the account-level
// policy forwards and where.
type cwSubscriptionSpec struct {
	name                   string
	policyLevel            string
	pattern                string
	destinationArn         string
	roleArn                string
	distribution           string
	applyOnTransformedLogs bool
	fieldSelection         string
	emitSystemFields       []string
}

// cwAccountSubscription is the account-level subscription filter policy that
// applies to group, if any. The account-level guide never applies it to the
// log group of its destination function.
func cwAccountSubscription(group string, now int64) (cwSubscriptionSpec, bool) {
	for _, p := range cwAccountPoliciesOf(cwPolicySubscription) {
		if p.DisabledUntil > now || !cwPolicySelects(p.PolicyType, p.SelectionCriteria, group) {
			continue
		}
		var doc cwSubscriptionPolicyDocument
		if json.Unmarshal([]byte(p.PolicyDocument), &doc) != nil {
			continue
		}
		if target, refused := cwResolveSubscriptionTarget(doc.DestinationArn, doc.RoleArn); refused == nil && target.Kind == cwTargetLambda {
			if fn, _, ok := lambdaResolveInvocationTarget(target.Arn, ""); ok && cwLambdaLogGroup(fn) == group {
				continue
			}
		}
		return cwSubscriptionSpec{
			name:           p.PolicyName,
			policyLevel:    cwPolicyLevelAccount,
			pattern:        doc.FilterPattern,
			destinationArn: doc.DestinationArn,
			roleArn:        doc.RoleArn,
			distribution:   doc.Distribution,
		}, true
	}
	return cwSubscriptionSpec{}, false
}

func cwLambdaLogGroup(fn LambdaFunction) string {
	if group, ok := fn.LoggingConfig["LogGroup"].(string); ok && group != "" {
		return group
	}
	return "/aws/lambda/" + fn.FunctionName
}

// Each filter, and the account-level policy, forwards the events of one
// ingested batch that it matches, the transformed versions where it applies
// to transformed logs.
func cwDeliverSubscriptions(group, stream string, events []CWLogEvent) {
	now := time.Now().UnixMilli()
	var specs []cwSubscriptionSpec
	for _, f := range cwGroupSubscriptionFilters(group) {
		if f.DisabledUntil > now {
			continue
		}
		specs = append(specs, cwSubscriptionSpec{
			name:                   f.FilterName,
			pattern:                f.FilterPattern,
			destinationArn:         f.DestinationArn,
			roleArn:                f.RoleArn,
			distribution:           f.Distribution,
			applyOnTransformedLogs: f.ApplyOnTransformedLogs,
			fieldSelection:         f.FieldSelectionCriteria,
			emitSystemFields:       f.EmitSystemFields,
		})
	}
	if spec, ok := cwAccountSubscription(group, now); ok {
		specs = append(specs, spec)
	}
	system := cwSystemFieldValues(group)
	for _, spec := range specs {
		cwForwardSubscription(spec, group, stream, events, system)
	}
}

func cwForwardSubscription(spec cwSubscriptionSpec, group, stream string, events []CWLogEvent, system map[string]string) {
	pattern, err := cwCompileLogPattern(spec.pattern)
	if err != nil {
		return
	}
	if sel, err := cwParseFieldSelection(spec.fieldSelection); err != nil || (sel != nil && !sel.match(system)) {
		return
	}
	var matched []cwSubscriptionLogEvent
	for _, e := range events {
		message := e.Message
		if spec.applyOnTransformedLogs {
			message = e.processedMessage()
		}
		if !pattern.match(message) {
			continue
		}
		forwarded := cwSubscriptionLogEvent{ID: e.ID, Timestamp: e.Timestamp, Message: message}
		for _, field := range spec.emitSystemFields {
			switch field {
			case "@aws.account":
				forwarded.Account = system[field]
			case "@aws.region":
				forwarded.Region = system[field]
			case "@source.log":
				forwarded.SourceLog = system[field]
			}
		}
		matched = append(matched, forwarded)
	}
	if len(matched) == 0 {
		return
	}
	message := cwSubscriptionMessage{
		Owner:               awsAccountID(),
		LogGroup:            group,
		LogStream:           stream,
		SubscriptionFilters: []string{spec.name},
		MessageType:         "DATA_MESSAGE",
		LogEvents:           matched,
		PolicyLevel:         spec.policyLevel,
	}
	partitionKey := cwSubscriptionPartitionKey(spec.distribution, group, stream)
	target, refused := cwResolveSubscriptionTarget(spec.destinationArn, spec.roleArn)
	limit := math.MaxInt
	if refused == nil {
		limit = cwSubscriptionRecordLimit(target, partitionKey)
	}
	for _, part := range cwSplitSubscriptionMessage(message, limit) {
		cwSubscriptionDeliveries.Submit(sim.NewUUID(), cwSubscriptionDelivery{
			LogGroupName:    group,
			FilterName:      spec.name,
			PolicyLevel:     spec.policyLevel,
			DestinationArn:  spec.destinationArn,
			DestinationType: target.Kind,
			RoleArn:         spec.roleArn,
			PartitionKey:    partitionKey,
			Events:          len(part.LogEvents),
			Data:            cwGzipSubscriptionMessage(part),
		})
	}
}

func handleCWPutSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupName           string   `json:"logGroupName"`
		FilterName             string   `json:"filterName"`
		FilterPattern          string   `json:"filterPattern"`
		DestinationArn         string   `json:"destinationArn"`
		RoleArn                string   `json:"roleArn"`
		Distribution           string   `json:"distribution"`
		ApplyOnTransformedLogs bool     `json:"applyOnTransformedLogs"`
		FieldSelectionCriteria string   `json:"fieldSelectionCriteria"`
		EmitSystemFields       []string `json:"emitSystemFields"`
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
	if !group.standard() {
		AWSError(w, "InvalidOperationException", "Subscriptions are supported only for log groups in the Standard log class.", http.StatusBadRequest)
		return
	}
	if _, err := cwCompileLogPattern(req.FilterPattern); err != nil {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
			"Invalid filter pattern: %s", err.Error())
		return
	}
	if _, err := cwParseFieldSelection(req.FieldSelectionCriteria); err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
		return
	}
	if err := cwCheckSystemFields(req.EmitSystemFields, cwSystemFields...); err != nil {
		AWSError(w, "InvalidParameterException", err.Error(), http.StatusBadRequest)
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
		FieldSelectionCriteria: req.FieldSelectionCriteria,
		EmitSystemFields:       req.EmitSystemFields,
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
