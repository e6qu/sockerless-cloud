package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"github.com/e6qu/sockerless-cloud/sim/signals"
)

// Amazon SQS standard and FIFO queues, fed by producers and by Amazon SNS,
// EventBridge, EventBridge Scheduler, AWS Lambda and AWS Step Functions. SQS
// speaks awsJson1_0 (`X-Amz-Target: AmazonSQS.<Action>`), as aws-sdk-go-v2
// service/sqs has since v1.28.

// SQSQueue is the durable provider-side record for a standard or FIFO queue.
// Messages remain stored until DeleteMessage, retention expiry, purge, or
// dead-letter redrive; ReceiveMessage leases them for the visibility timeout,
// so an unacknowledged message becomes available again.
type SQSQueue struct {
	Name              string
	URL               string
	ARN               string
	CreatedTimestamp  int64
	VisibilityTimeout int // seconds; mirrored into Attributes["VisibilityTimeout"]
	Tags              map[string]string
	// Messages also carries the FIFO deduplication records, which outlive
	// message deletion for the five-minute deduplication interval.
	Messages msgq.Queue[sqsPayload]
	// Attributes stores every queue attribute CreateQueue or SetQueueAttributes set;
	// GetQueueAttributes adds the system values (QueueArn, CreatedTimestamp, message
	// counts).
	Attributes map[string]string
}

type sqsPayload struct {
	Body                   string                         `json:"body"`
	MD5OfBody              string                         `json:"md5OfBody"`
	MessageAttributes      map[string]SQSMessageAttribute `json:"messageAttributes,omitempty"`
	MD5OfMessageAttributes string                         `json:"md5OfMessageAttributes,omitempty"`
}

type SQSMessage struct {
	MessageId               string
	Body                    string
	MD5OfBody               string
	ReceiptHandle           string
	SentTimestamp           int64
	ApproximateReceiveCount int
	FirstReceivedAt         int64
	MessageGroupID          string
	MessageDeduplicationID  string
	SequenceNumber          string
	MessageAttributes       map[string]SQSMessageAttribute
	MD5OfMessageAttributes  string
}

func sqsMessageView(m msgq.Message[sqsPayload], fifo bool) SQSMessage {
	v := SQSMessage{
		MessageId:               m.ID,
		Body:                    m.Payload.Body,
		MD5OfBody:               m.Payload.MD5OfBody,
		ReceiptHandle:           m.Receipt,
		SentTimestamp:           m.EnqueuedAt,
		ApproximateReceiveCount: m.Deliveries,
		FirstReceivedAt:         m.FirstDeliveredAt,
		MessageGroupID:          m.Group,
		MessageDeduplicationID:  m.DedupID,
		MessageAttributes:       m.Payload.MessageAttributes,
		MD5OfMessageAttributes:  m.Payload.MD5OfMessageAttributes,
	}
	if fifo {
		v.SequenceNumber = strconv.FormatUint(m.Seq, 10)
	}
	return v
}

// sqsPolicy is the delivery policy a queue's attributes configure. The receipt
// handle of a message stays valid after its visibility timeout until the
// message is received again, as it does on Amazon SQS.
func sqsPolicy(q SQSQueue) msgq.Policy {
	pol := msgq.Policy{
		Retention:            time.Duration(sqsQueueIntAttribute(q, "MessageRetentionPeriod", sqsDefaultRetentionSeconds)) * time.Second,
		ReceiptOutlivesLease: true,
	}
	if _, maxReceiveCount, ok := sqsParseRedrivePolicy(q.Attributes); ok {
		pol.MaxDeliveries = maxReceiveCount
	}
	if sqsQueueIsFifo(q) {
		pol.Ordered = true
		pol.DedupWindow = sqsDeduplicationWindow
	}
	return pol
}

type SQSMessageAttribute struct {
	DataType    string `json:"DataType"`
	StringValue string `json:"StringValue,omitempty"`
	BinaryValue []byte `json:"BinaryValue,omitempty"`
}

// sqsMessageAttributeMD5 digests a message-attribute set as SQS does, which
// aws-sdk-go-v2 checks on receive: attributes sorted by name, each a
// length-prefixed name and data type, a transport byte (1 string or number, 2
// binary), then the length-prefixed value.
func sqsMessageAttributeMD5(attrs map[string]SQSMessageAttribute) string {
	if len(attrs) == 0 {
		return ""
	}
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	encStr := func(s string) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s)))
		buf.Write(l[:])
		buf.WriteString(s)
	}
	encBytes := func(b []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(b)))
		buf.Write(l[:])
		buf.Write(b)
	}
	for _, n := range names {
		a := attrs[n]
		encStr(n)
		encStr(a.DataType)
		if strings.HasPrefix(a.DataType, "Binary") {
			buf.WriteByte(2)
			encBytes(a.BinaryValue)
		} else {
			buf.WriteByte(1)
			encStr(a.StringValue)
		}
	}
	sum := md5.Sum(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// sqsSelectMessageAttributeSubset returns the stored attributes ReceiveMessage's
// names select ("All" or ".*" select all), or nil; the caller renders the subset
// and recomputes MD5OfMessageAttributes over it.
func sqsSelectMessageAttributeSubset(attrs map[string]SQSMessageAttribute, requested []string) map[string]SQSMessageAttribute {
	if len(attrs) == 0 || len(requested) == 0 {
		return nil
	}
	all := false
	want := map[string]bool{}
	for _, n := range requested {
		if n == "All" || n == ".*" {
			all = true
		}
		want[n] = true
	}
	out := map[string]SQSMessageAttribute{}
	for name, a := range attrs {
		if all || want[name] {
			out[name] = a
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sqsRenderMessageAttributes(attrs map[string]SQSMessageAttribute) map[string]any {
	out := map[string]any{}
	for name, a := range attrs {
		entry := map[string]any{"DataType": a.DataType}
		if strings.HasPrefix(a.DataType, "Binary") {
			entry["BinaryValue"] = a.BinaryValue
		} else {
			entry["StringValue"] = a.StringValue
		}
		out[name] = entry
	}
	return out
}

var (
	sqsQueues sim.Store[SQSQueue]
	// sqsQueueChanges announces a queue's name on every write of it: its
	// creation, deletion, attributes and messages.
	sqsQueueChanges = signals.New[string]()
)

const (
	sqsDefaultVisibilityTimeout = 30
	sqsDefaultDelaySeconds      = 0
	sqsDefaultRetentionSeconds  = 4 * 24 * 60 * 60
	sqsDefaultMaximumBytes      = 1024 * 1024
	sqsDeduplicationWindow      = 5 * time.Minute
)

// sqsQueueURL builds the queue URL SQS emits, on AWS's
// `sqs.<region>.amazonaws.com` host; the SDK uses it only as the QueueUrl key of
// later calls and never dereferences it.
func sqsQueueURL(name string) string {
	return fmt.Sprintf("https://sqs.%s.amazonaws.com/%s/%s",
		awsRegion(), awsAccountID(), name)
}

func sqsQueueARN(name string) string {
	return fmt.Sprintf("arn:aws:sqs:%s:%s:%s",
		awsRegion(), awsAccountID(), name)
}

// sqsEnqueueByARN delivers a plain-body message to the queue an ARN names and
// reports whether the queue exists. Internal delivery (SNS or EventBridge to SQS)
// calls it after authorizing against the queue policy.
func sqsEnqueueByARN(queueARN, body string) bool {
	name := queueARN
	if i := strings.LastIndex(queueARN, ":"); i >= 0 {
		name = queueARN[i+1:]
	}
	if _, ok := sqsQueues.Get(name); !ok {
		return false
	}
	_ = sqsEnqueue(name, sqsSendEntry{MessageBody: body})
	return true
}

// sqsParseRedrivePolicy returns a RedrivePolicy's dead-letter target ARN and
// maxReceiveCount, which SQS writes as a JSON string number; a number is
// accepted too. ok is false without a usable policy.
func sqsParseRedrivePolicy(attrs map[string]string) (dlqARN string, maxReceiveCount int, ok bool) {
	raw, present := attrs["RedrivePolicy"]
	if !present || strings.TrimSpace(raw) == "" {
		return "", 0, false
	}
	var rp struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		MaxReceiveCount     any    `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(raw), &rp); err != nil {
		return "", 0, false
	}
	switch v := rp.MaxReceiveCount.(type) {
	case string:
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return "", 0, false
		}
		maxReceiveCount = n
	case float64:
		if v <= 0 {
			return "", 0, false
		}
		maxReceiveCount = int(v)
	default:
		return "", 0, false
	}
	if rp.DeadLetterTargetArn == "" {
		return "", 0, false
	}
	return rp.DeadLetterTargetArn, maxReceiveCount, true
}

func sqsNameFromARN(arn string) string {
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// sqsEnqueueRedrives moves each message to the dead-letter queue dlqARN names,
// as SQS redrives on receive: the message leaves the source, takes a new
// MessageId and restarts its receive count. With no DLQ, or a deleted one, the
// messages are dropped, as in SQS.
func sqsEnqueueRedrives(dlqARN string, msgs []msgq.Message[sqsPayload]) {
	if dlqARN == "" || len(msgs) == 0 {
		return
	}
	now := time.Now()
	sqsQueues.Update(sqsNameFromARN(dlqARN), func(d *SQSQueue) {
		delay := time.Duration(sqsQueueIntAttribute(*d, "DelaySeconds", sqsDefaultDelaySeconds)) * time.Second
		pol := sqsPolicy(*d)
		for _, m := range msgs {
			d.Messages.Enqueue(m.Payload, msgq.EnqueueOpts{Delay: delay, Group: m.Group, DedupID: m.DedupID}, pol, now)
		}
	})
	sqsSignalQueue(sqsNameFromARN(dlqARN))
}

func registerSQS(r *AWSRouter, srv *sim.Server) {
	// Message-level ops are CloudTrail DATA events (excluded from LookupEvents);
	// queue-management ops are management events.
	cloudTrailDeclareDataEvents("sqs.amazonaws.com",
		"SendMessage", "SendMessageBatch", "ReceiveMessage", "DeleteMessage",
		"DeleteMessageBatch", "ChangeMessageVisibility", "ChangeMessageVisibilityBatch")
	sqsQueues = sim.Announcing(sim.MakeStore[SQSQueue](srv.DB(), "sqs_queues"), sqsQueueChanges)
	if err := sqsMigrateQueues(srv.DB()); err != nil {
		log.Fatalf("sqs: %v", err)
	}

	r.Register("AmazonSQS.CreateQueue", handleSQSCreateQueue)
	r.Register("AmazonSQS.DeleteQueue", handleSQSDeleteQueue)
	r.Register("AmazonSQS.GetQueueUrl", handleSQSGetQueueURL)
	r.Register("AmazonSQS.ListQueues", handleSQSListQueues)
	r.Register("AmazonSQS.GetQueueAttributes", handleSQSGetQueueAttributes)
	r.Register("AmazonSQS.SetQueueAttributes", handleSQSSetQueueAttributes)
	r.Register("AmazonSQS.SendMessage", handleSQSSendMessage)
	r.Register("AmazonSQS.SendMessageBatch", handleSQSSendMessageBatch)
	r.Register("AmazonSQS.ReceiveMessage", handleSQSReceiveMessage)
	r.Register("AmazonSQS.DeleteMessage", handleSQSDeleteMessage)
	r.Register("AmazonSQS.DeleteMessageBatch", handleSQSDeleteMessageBatch)
	r.Register("AmazonSQS.ChangeMessageVisibility", handleSQSChangeMessageVisibility)
	r.Register("AmazonSQS.ChangeMessageVisibilityBatch", handleSQSChangeMessageVisibilityBatch)
	r.Register("AmazonSQS.AddPermission", handleSQSAddPermission)
	r.Register("AmazonSQS.RemovePermission", handleSQSRemovePermission)
	r.Register("AmazonSQS.TagQueue", handleSQSTagQueue)
	r.Register("AmazonSQS.UntagQueue", handleSQSUntagQueue)
	r.Register("AmazonSQS.ListQueueTags", handleSQSListQueueTags)
	r.Register("AmazonSQS.PurgeQueue", handleSQSPurgeQueue)
}

// queueNameFromURL returns the queue name from a queue URL, which SQS callers
// pass after CreateQueue, or from a bare name.
func queueNameFromURL(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func sqsErrorJSON(w http.ResponseWriter, code, message string, status int) {
	AWSError(w, code, message, status)
}

func sqsQueueDoesNotExist(w http.ResponseWriter) {
	sqsErrorJSON(w, "AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist.", http.StatusBadRequest)
}

// sqsNumericAttributes must hold integers: SQS rejects any other value with
// InvalidParameterValue at CreateQueue or SetQueueAttributes.
var sqsNumericAttributes = []string{
	"VisibilityTimeout",
	"DelaySeconds",
	"MessageRetentionPeriod",
	"MaximumMessageSize",
	"ReceiveMessageWaitTimeSeconds",
}

// sqsValidateNumericAttributes returns the first numeric attribute whose value
// is not an integer, with ok false.
func sqsValidateNumericAttributes(attrs map[string]string) (msg string, ok bool) {
	ranges := map[string][2]int{
		"VisibilityTimeout":             {0, 43200},
		"DelaySeconds":                  {0, 900},
		"MessageRetentionPeriod":        {60, 1209600},
		"MaximumMessageSize":            {1024, sqsDefaultMaximumBytes},
		"ReceiveMessageWaitTimeSeconds": {0, 20},
	}
	for _, k := range sqsNumericAttributes {
		v, present := attrs[k]
		if !present {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Sprintf("Invalid value for the parameter %s.", k), false
		}
		if bounds, constrained := ranges[k]; constrained && (n < bounds[0] || n > bounds[1]) {
			return fmt.Sprintf("Invalid value for the parameter %s.", k), false
		}
	}
	return "", true
}

func sqsQueueIntAttribute(q SQSQueue, name string, fallback int) int {
	raw, ok := q.Attributes[name]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func handleSQSCreateQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueName  string            `json:"QueueName"`
		Attributes map[string]string `json:"Attributes"`
		Tags       map[string]string `json:"tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	if req.QueueName == "" {
		sqsErrorJSON(w, "MissingParameter", "QueueName is required", http.StatusBadRequest)
		return
	}
	if msg, ok := sqsFifoNameAttrMismatch(req.QueueName, req.Attributes); !ok {
		sqsErrorJSON(w, "InvalidParameterValue", msg, http.StatusBadRequest)
		return
	}
	if msg, ok := sqsValidateNumericAttributes(req.Attributes); !ok {
		sqsErrorJSON(w, "InvalidParameterValue", msg, http.StatusBadRequest)
		return
	}
	if existing, ok := sqsQueues.Get(req.QueueName); ok {
		// SQS treats CreateQueue as idempotent only with identical attributes;
		// differing ones are QueueNameExists.
		if sqsAttributesEqual(existing.Attributes, req.Attributes) {
			sim.WriteJSON(w, http.StatusOK, map[string]string{"QueueUrl": existing.URL})
			return
		}
		sqsErrorJSON(w, "QueueNameExists",
			"A queue already exists with the same name and a different value for attribute(s).",
			http.StatusBadRequest)
		return
	}
	q := SQSQueue{
		Name:              req.QueueName,
		URL:               sqsQueueURL(req.QueueName),
		ARN:               sqsQueueARN(req.QueueName),
		CreatedTimestamp:  time.Now().Unix(),
		VisibilityTimeout: sqsDefaultVisibilityTimeout,
		Tags:              map[string]string{},
		Attributes:        map[string]string{},
	}
	// Persist every attribute; VisibilityTimeout also goes to the typed field Receive reads.
	for k, v := range req.Attributes {
		q.Attributes[k] = v
		if k == "VisibilityTimeout" {
			if n, err := strconv.Atoi(v); err == nil {
				q.VisibilityTimeout = n
			}
		}
	}
	for k, v := range req.Tags {
		q.Tags[k] = v
	}
	sqsQueues.Put(req.QueueName, q)
	sim.WriteJSON(w, http.StatusOK, map[string]string{"QueueUrl": q.URL})
}

func handleSQSDeleteQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	if !sqsQueues.Delete(queueNameFromURL(req.QueueUrl)) {
		sqsQueueDoesNotExist(w)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleSQSPurgeQueue removes every message, keeping the queue's attributes and
// tags. SQS allows one PurgeQueue per queue per minute; the simulator does not
// enforce that.
func handleSQSPurgeQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	// Clear under the store's write lock so a concurrent receive or send is not
	// overwritten by a snapshot write-back.
	sqsQueues.Update(name, func(q *SQSQueue) { q.Messages.Purge() })
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQSGetQueueURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueName string `json:"QueueName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	q, ok := sqsQueues.Get(req.QueueName)
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]string{"QueueUrl": q.URL})
}

func handleSQSListQueues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueNamePrefix string `json:"QueueNamePrefix"`
		NextToken       string `json:"NextToken"`
		MaxResults      int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	all := sqsQueues.List()
	sortBy(all, func(q SQSQueue) string { return q.URL })
	var filtered []SQSQueue
	for _, q := range all {
		if req.QueueNamePrefix != "" && !strings.HasPrefix(q.Name, req.QueueNamePrefix) {
			continue
		}
		filtered = append(filtered, q)
	}
	page, next, pageOK := awsKeyPage(w, sqsBadToken, filtered, func(it SQSQueue) string { return it.URL }, req.NextToken, req.MaxResults, 1000)
	if !pageOK {
		return
	}
	urls := make([]string, 0, len(page))
	for _, q := range page {
		urls = append(urls, q.URL)
	}
	out := map[string]any{"QueueUrls": urls}
	if next != "" {
		out["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleSQSGetQueueAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl       string   `json:"QueueUrl"`
		AttributeNames []string `json:"AttributeNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	q, ok := sqsQueues.Get(queueNameFromURL(req.QueueUrl))
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	wanted := map[string]bool{}
	for _, n := range req.AttributeNames {
		wanted[n] = true
	}
	all := len(wanted) == 0 || wanted["All"]

	counts := q.Messages.Count(sqsPolicy(q), time.Now())
	visibleCount, invisibleCount, delayedCount := counts.Available, counts.Held, counts.Delayed

	// Layer the queue's own attributes over the system ones, taking VisibilityTimeout from the typed field.
	allAttrs := map[string]string{
		"QueueArn":                              q.ARN,
		"VisibilityTimeout":                     strconv.Itoa(q.VisibilityTimeout),
		"CreatedTimestamp":                      strconv.FormatInt(q.CreatedTimestamp, 10),
		"ApproximateNumberOfMessages":           strconv.Itoa(visibleCount),
		"ApproximateNumberOfMessagesNotVisible": strconv.Itoa(invisibleCount),
		"ApproximateNumberOfMessagesDelayed":    strconv.Itoa(delayedCount),
		"DelaySeconds":                          strconv.Itoa(sqsDefaultDelaySeconds),
		"MessageRetentionPeriod":                strconv.Itoa(sqsDefaultRetentionSeconds),
		"MaximumMessageSize":                    strconv.Itoa(sqsDefaultMaximumBytes),
		"ReceiveMessageWaitTimeSeconds":         "0",
	}
	for k, v := range q.Attributes {
		allAttrs[k] = v
	}
	// The typed VisibilityTimeout field governs receives, so it wins over a diverging map entry.
	allAttrs["VisibilityTimeout"] = strconv.Itoa(q.VisibilityTimeout)
	out := map[string]string{}
	for k, v := range allAttrs {
		if all || wanted[k] {
			out[k] = v
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Attributes": out})
}

func handleSQSSetQueueAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl   string            `json:"QueueUrl"`
		Attributes map[string]string `json:"Attributes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if msg, ok := sqsValidateNumericAttributes(req.Attributes); !ok {
		sqsErrorJSON(w, "InvalidParameterValue", msg, http.StatusBadRequest)
		return
	}
	var queueARN string
	sqsQueues.Update(name, func(q *SQSQueue) {
		if q.Attributes == nil {
			q.Attributes = map[string]string{}
		}
		for k, v := range req.Attributes {
			q.Attributes[k] = v
			if k == "VisibilityTimeout" {
				if n, err := strconv.Atoi(v); err == nil {
					q.VisibilityTimeout = n
				}
			}
		}
		queueARN = q.ARN
	})
	// Mirror the queue policy into the central resource-policy store so the IAM
	// enforcement gate can resolve it by the queue ARN.
	if policy, ok := req.Attributes["Policy"]; ok {
		if policy == "" {
			iamDeleteResourcePolicy(queueARN)
		} else {
			iamPutResourcePolicy(queueARN, policy)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// sqsSendEntry is the payload SendMessage and each SendMessageBatch entry share;
// only a batch entry carries an Id.
type sqsSendEntry struct {
	Id                     string                         `json:"Id"`
	MessageBody            string                         `json:"MessageBody"`
	MessageAttributes      map[string]SQSMessageAttribute `json:"MessageAttributes"`
	DelaySeconds           *int                           `json:"DelaySeconds"`
	MessageGroupId         string                         `json:"MessageGroupId"`
	MessageDeduplicationId string                         `json:"MessageDeduplicationId"`
}

// sqsValidateSend applies SQS's per-message checks: a non-empty body, the
// queue's size limit (1 MiB by default) and, on a FIFO queue, MessageGroupId and
// deduplication. An empty code means valid.
func sqsValidateSend(q SQSQueue, e sqsSendEntry) (string, string) {
	if e.MessageBody == "" {
		return "MissingParameter", "MessageBody is required"
	}
	maximumBytes := sqsQueueIntAttribute(q, "MaximumMessageSize", sqsDefaultMaximumBytes)
	if len(e.MessageBody) > maximumBytes {
		return "InvalidParameterValue",
			fmt.Sprintf("One or more parameters are invalid. Reason: Message must be shorter than %d bytes.", maximumBytes)
	}
	if e.DelaySeconds != nil && (*e.DelaySeconds < 0 || *e.DelaySeconds > 900) {
		return "InvalidParameterValue", "DelaySeconds must be between 0 and 900."
	}
	if sqsQueueIsFifo(q) {
		if e.DelaySeconds != nil {
			return "InvalidParameterValue", "Value for parameter DelaySeconds is invalid. Reason: The request include parameter that is not valid for this queue type."
		}
		if e.MessageGroupId == "" {
			return "MissingParameter", "The request must contain the parameter MessageGroupId."
		}
		if !sqsContentBasedDedup(q) && e.MessageDeduplicationId == "" {
			return "InvalidParameterValue",
				"The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly"
		}
	}
	return "", ""
}

type sqsEnqueueResult struct {
	MessageID              string
	MD5OfBody              string
	MD5OfMessageAttributes string
	SequenceNumber         string
}

func sqsDeduplicationKey(q SQSQueue, e sqsSendEntry) (key, id string) {
	id = e.MessageDeduplicationId
	if id == "" && sqsContentBasedDedup(q) {
		sum := sha256.Sum256([]byte(e.MessageBody))
		id = hex.EncodeToString(sum[:])
	}
	key = id
	if q.Attributes["DeduplicationScope"] == "messageGroup" {
		key = e.MessageGroupId + "\x00" + id
	}
	return key, id
}

func sqsEnqueue(name string, e sqsSendEntry) (result sqsEnqueueResult) {
	hash := md5.Sum([]byte(e.MessageBody))
	result.MD5OfBody = hex.EncodeToString(hash[:])
	result.MD5OfMessageAttributes = sqsMessageAttributeMD5(e.MessageAttributes)
	payload := sqsPayload{
		Body:                   e.MessageBody,
		MD5OfBody:              result.MD5OfBody,
		MessageAttributes:      e.MessageAttributes,
		MD5OfMessageAttributes: result.MD5OfMessageAttributes,
	}
	now := time.Now()
	sqsQueues.Update(name, func(q *SQSQueue) {
		opts := msgq.EnqueueOpts{
			Delay:   time.Duration(sqsQueueIntAttribute(*q, "DelaySeconds", sqsDefaultDelaySeconds)) * time.Second,
			Group:   e.MessageGroupId,
			DedupID: e.MessageDeduplicationId,
		}
		if e.DelaySeconds != nil {
			opts.Delay = time.Duration(*e.DelaySeconds) * time.Second
		}
		fifo := sqsQueueIsFifo(*q)
		if fifo {
			opts.DedupKey, opts.DedupID = sqsDeduplicationKey(*q, e)
		}
		m, _ := q.Messages.Enqueue(payload, opts, sqsPolicy(*q), now)
		result.MessageID = m.ID
		if fifo {
			result.SequenceNumber = strconv.FormatUint(m.Seq, 10)
		}
	})
	sqsSignalQueue(name)
	return result
}

func sqsEnqueueBodyWithAttributes(queueName, body string, attributes map[string]SQSMessageAttribute) {
	_ = sqsEnqueue(queueName, sqsSendEntry{MessageBody: body, MessageAttributes: attributes})
	if q, ok := sqsQueues.Get(queueName); ok {
		cwEvalLogger.Debug().Str("queueName", queueName).Int("messageCount", len(q.Messages.Messages)).Msg("SQS enqueue body completed")
	}
}

func handleSQSSendMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl               string                         `json:"QueueUrl"`
		MessageBody            string                         `json:"MessageBody"`
		MessageAttributes      map[string]SQSMessageAttribute `json:"MessageAttributes"`
		DelaySeconds           *int                           `json:"DelaySeconds"`
		MessageGroupId         string                         `json:"MessageGroupId"`
		MessageDeduplicationId string                         `json:"MessageDeduplicationId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	q, ok := sqsQueues.Get(name)
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	entry := sqsSendEntry{
		MessageBody:            req.MessageBody,
		MessageAttributes:      req.MessageAttributes,
		DelaySeconds:           req.DelaySeconds,
		MessageGroupId:         req.MessageGroupId,
		MessageDeduplicationId: req.MessageDeduplicationId,
	}
	if code, msg := sqsValidateSend(q, entry); code != "" {
		sqsErrorJSON(w, code, msg, http.StatusBadRequest)
		return
	}
	result := sqsEnqueue(name, entry)
	resp := map[string]string{
		"MessageId":        result.MessageID,
		"MD5OfMessageBody": result.MD5OfBody,
	}
	if result.MD5OfMessageAttributes != "" {
		resp["MD5OfMessageAttributes"] = result.MD5OfMessageAttributes
	}
	if result.SequenceNumber != "" {
		resp["SequenceNumber"] = result.SequenceNumber
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleSQSSendMessageBatch sends up to 10 messages. Batch-level failures (no
// entries, more than 10, duplicate Ids, total size over the limit) are
// top-level errors; per-entry failures (a FIFO entry without MessageGroupId) go
// to Failed with HTTP 200, as in SQS.
func handleSQSSendMessageBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string         `json:"QueueUrl"`
		Entries  []sqsSendEntry `json:"Entries"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	q, ok := sqsQueues.Get(name)
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if len(req.Entries) == 0 {
		sqsErrorJSON(w, "AWS.SimpleQueueService.EmptyBatchRequest",
			"There should be at least one SendMessageBatchRequestEntry in the request.",
			http.StatusBadRequest)
		return
	}
	if len(req.Entries) > 10 {
		sqsErrorJSON(w, "AWS.SimpleQueueService.TooManyEntriesInBatchRequest",
			"Maximum number of entries per request are 10. You have sent "+strconv.Itoa(len(req.Entries))+".",
			http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	total := 0
	for _, e := range req.Entries {
		if seen[e.Id] {
			sqsErrorJSON(w, "AWS.SimpleQueueService.BatchEntryIdsNotDistinct",
				"Id "+e.Id+" repeated.", http.StatusBadRequest)
			return
		}
		seen[e.Id] = true
		total += len(e.MessageBody)
	}
	maximumBytes := sqsQueueIntAttribute(q, "MaximumMessageSize", sqsDefaultMaximumBytes)
	if total > maximumBytes {
		sqsErrorJSON(w, "AWS.SimpleQueueService.BatchRequestTooLong",
			"Batch requests cannot be longer than "+strconv.Itoa(maximumBytes)+" bytes. You have sent "+strconv.Itoa(total)+" bytes.",
			http.StatusBadRequest)
		return
	}

	successful := make([]map[string]string, 0, len(req.Entries))
	failed := make([]map[string]any, 0)
	for _, e := range req.Entries {
		if code, msg := sqsValidateSend(q, e); code != "" {
			failed = append(failed, map[string]any{
				"Id":          e.Id,
				"Code":        code,
				"Message":     msg,
				"SenderFault": true,
			})
			continue
		}
		result := sqsEnqueue(name, e)
		entry := map[string]string{
			"Id":               e.Id,
			"MessageId":        result.MessageID,
			"MD5OfMessageBody": result.MD5OfBody,
		}
		if result.MD5OfMessageAttributes != "" {
			entry["MD5OfMessageAttributes"] = result.MD5OfMessageAttributes
		}
		if result.SequenceNumber != "" {
			entry["SequenceNumber"] = result.SequenceNumber
		}
		successful = append(successful, entry)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Successful": successful,
		"Failed":     failed,
	})
}

func handleSQSReceiveMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl                    string   `json:"QueueUrl"`
		MaxNumberOfMessages         int      `json:"MaxNumberOfMessages"`
		VisibilityTimeout           *int     `json:"VisibilityTimeout"`
		WaitTimeSeconds             *int     `json:"WaitTimeSeconds"`
		AttributeNames              []string `json:"AttributeNames"`
		MessageSystemAttributeNames []string `json:"MessageSystemAttributeNames"`
		MessageAttributeNames       []string `json:"MessageAttributeNames"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	q, ok := sqsQueues.Get(name)
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	maxN := req.MaxNumberOfMessages
	if maxN == 0 {
		maxN = 1
	} else if maxN < 1 || maxN > 10 {
		sqsErrorJSON(w, "InvalidParameterValue",
			fmt.Sprintf("Value %d for parameter MaxNumberOfMessages is invalid. Reason: must be between 1 and 10.", maxN),
			http.StatusBadRequest)
		return
	}
	visTimeout := q.VisibilityTimeout
	if req.VisibilityTimeout != nil {
		visTimeout = *req.VisibilityTimeout
	}
	waitSeconds := 0
	if v, ok := q.Attributes["ReceiveMessageWaitTimeSeconds"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			sqsErrorJSON(w, "InvalidParameterValue", "ReceiveMessageWaitTimeSeconds must be an integer.", http.StatusBadRequest)
			return
		}
		waitSeconds = n
	}
	if req.WaitTimeSeconds != nil {
		waitSeconds = *req.WaitTimeSeconds
	}
	if waitSeconds < 0 || waitSeconds > 20 {
		sqsErrorJSON(w, "InvalidParameterValue",
			fmt.Sprintf("Value %d for parameter WaitTimeSeconds is invalid. Reason: must be between 0 and 20.", waitSeconds),
			http.StatusBadRequest)
		return
	}

	sim.DeclareWait(r.Context(), time.Duration(waitSeconds)*time.Second)
	picked, ok := sqsLongPoll(r.Context(), name, maxN, visTimeout, time.Now().Add(time.Duration(waitSeconds)*time.Second))
	if !ok {
		return
	}

	out := make([]map[string]any, 0, len(picked))
	for _, m := range picked {
		msg := map[string]any{
			"MessageId":     m.MessageId,
			"ReceiptHandle": m.ReceiptHandle,
			"MD5OfBody":     m.MD5OfBody,
			"Body":          m.Body,
			"Attributes":    sqsRenderSystemAttributes(m, append(req.AttributeNames, req.MessageSystemAttributeNames...)),
		}
		if len(req.AttributeNames) == 0 && len(req.MessageSystemAttributeNames) == 0 {
			msg["Attributes"] = map[string]string{
				"ApproximateReceiveCount":          strconv.Itoa(m.ApproximateReceiveCount),
				"SentTimestamp":                    strconv.FormatInt(m.SentTimestamp, 10),
				"ApproximateFirstReceiveTimestamp": strconv.FormatInt(m.FirstReceivedAt, 10),
			}
		}
		if subset := sqsSelectMessageAttributeSubset(m.MessageAttributes, req.MessageAttributeNames); subset != nil {
			msg["MessageAttributes"] = sqsRenderMessageAttributes(subset)
			// The MD5 must cover exactly the returned subset: aws-sdk-go-v2's
			// ValidateMessageChecksums recomputes MD5OfMessageAttributes over the
			// received attributes and rejects the message on mismatch, so a
			// partial selection cannot reuse the stored full-set digest.
			msg["MD5OfMessageAttributes"] = sqsMessageAttributeMD5(subset)
		}
		out = append(out, msg)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Messages": out})
}

func sqsReceiveAvailableMessages(name string, maxN int, visTimeout int) []SQSMessage {
	now := time.Now()
	var picked []SQSMessage
	var got msgq.Received[sqsPayload]
	var dlqARN string

	sqsQueues.Update(name, func(qq *SQSQueue) {
		dlqARN, _, _ = sqsParseRedrivePolicy(qq.Attributes)
		total := len(qq.Messages.Messages)
		got = qq.Messages.Receive(msgq.ReceiveOpts[sqsPayload]{
			Max:   maxN,
			Lease: time.Duration(visTimeout) * time.Second,
		}, sqsPolicy(*qq), now)
		fifo := sqsQueueIsFifo(*qq)
		for _, m := range got.Leased {
			picked = append(picked, sqsMessageView(m, fifo))
		}
		cwEvalLogger.Debug().Str("queueName", name).Int("totalMessages", total).Int("picked", len(picked)).Int("redrived", len(got.DeadLettered)).Int("visibilityTimeout", visTimeout).Msg("SQS ReceiveMessage scanned queue")
	})
	sqsEnqueueRedrives(dlqARN, got.DeadLettered)
	return picked
}

func sqsRenderSystemAttributes(message SQSMessage, requested []string) map[string]string {
	if len(requested) == 0 {
		return nil
	}
	all := false
	wanted := map[string]bool{}
	for _, name := range requested {
		all = all || name == "All"
		wanted[name] = true
	}
	values := map[string]string{
		"ApproximateReceiveCount":          strconv.Itoa(message.ApproximateReceiveCount),
		"SentTimestamp":                    strconv.FormatInt(message.SentTimestamp, 10),
		"ApproximateFirstReceiveTimestamp": strconv.FormatInt(message.FirstReceivedAt, 10),
	}
	if message.MessageGroupID != "" {
		values["MessageGroupId"] = message.MessageGroupID
	}
	if message.MessageDeduplicationID != "" {
		values["MessageDeduplicationId"] = message.MessageDeduplicationID
	}
	if message.SequenceNumber != "" {
		values["SequenceNumber"] = message.SequenceNumber
	}
	out := map[string]string{}
	for name, value := range values {
		if all || wanted[name] {
			out[name] = value
		}
	}
	return out
}

func handleSQSDeleteMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl      string `json:"QueueUrl"`
		ReceiptHandle string `json:"ReceiptHandle"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	sqsDeleteReceiptHandles(name, []string{req.ReceiptHandle})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// sqsReceiptHandleInvalid writes ReceiptHandleIsInvalid, which SQS raises for a
// handle that matches no message the queue holds.
func sqsReceiptHandleInvalid(w http.ResponseWriter, handle string) {
	w.Header().Set("x-amzn-query-error", "ReceiptHandleIsInvalid;Sender")
	sqsErrorJSON(w, "ReceiptHandleIsInvalid",
		`The input receipt handle "`+handle+`" is not a valid receipt handle.`,
		http.StatusBadRequest)
}

// handleSQSDeleteMessageBatch deletes up to 10 messages by receipt handle in a
// single call, reporting per-entry success/failure the way real SQS does. The
// batch-level failures (empty list, >10 entries, duplicate Ids) are top-level
// errors; a per-entry receipt handle that matches no in-flight message lands in
// the Failed array with HTTP 200.
func handleSQSDeleteMessageBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
		Entries  []struct {
			Id            string `json:"Id"`
			ReceiptHandle string `json:"ReceiptHandle"`
		} `json:"Entries"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if len(req.Entries) == 0 {
		sqsErrorJSON(w, "AWS.SimpleQueueService.EmptyBatchRequest",
			"There should be at least one DeleteMessageBatchRequestEntry in the request.",
			http.StatusBadRequest)
		return
	}
	if len(req.Entries) > 10 {
		sqsErrorJSON(w, "AWS.SimpleQueueService.TooManyEntriesInBatchRequest",
			"Maximum number of entries per request are 10. You have sent "+strconv.Itoa(len(req.Entries))+".",
			http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, e := range req.Entries {
		if seen[e.Id] {
			sqsErrorJSON(w, "AWS.SimpleQueueService.BatchEntryIdsNotDistinct",
				"Id "+e.Id+" repeated.", http.StatusBadRequest)
			return
		}
		seen[e.Id] = true
	}

	successful := make([]map[string]string, 0, len(req.Entries))
	failed := make([]map[string]any, 0)
	for _, e := range req.Entries {
		var found bool
		sqsQueues.Update(name, func(qq *SQSQueue) {
			_, found = qq.Messages.Settle(e.ReceiptHandle, sqsPolicy(*qq), time.Now())
		})
		sqsSignalQueue(name)
		if found {
			successful = append(successful, map[string]string{"Id": e.Id})
		} else {
			failed = append(failed, map[string]any{
				"Id":          e.Id,
				"Code":        "ReceiptHandleIsInvalid",
				"Message":     `The input receipt handle "` + e.ReceiptHandle + `" is not a valid receipt handle.`,
				"SenderFault": true,
			})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Successful": successful,
		"Failed":     failed,
	})
}

// sqsApplyVisibility moves the visibility deadline of the message a receipt
// handle names to now+timeout, as ChangeMessageVisibility does, and reports
// whether the handle matched and whether the message was in flight (SQS answers
// MessageNotInflight otherwise). The caller rejects a negative timeout.
func sqsApplyVisibility(name, handle string, timeout int) (matched, inflight bool) {
	sqsQueues.Update(name, func(qq *SQSQueue) {
		_, matched, inflight = qq.Messages.Extend(handle, time.Duration(timeout)*time.Second, sqsPolicy(*qq), time.Now())
	})
	sqsSignalQueue(name)
	return matched, inflight
}

func handleSQSChangeMessageVisibility(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl          string `json:"QueueUrl"`
		ReceiptHandle     string `json:"ReceiptHandle"`
		VisibilityTimeout int    `json:"VisibilityTimeout"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if req.VisibilityTimeout < 0 || req.VisibilityTimeout > 43200 {
		sqsErrorJSON(w, "InvalidParameterValue",
			fmt.Sprintf("Value %d for parameter VisibilityTimeout is invalid. Reason: must be between 0 and 43200.", req.VisibilityTimeout),
			http.StatusBadRequest)
		return
	}
	matched, inflight := sqsApplyVisibility(name, req.ReceiptHandle, req.VisibilityTimeout)
	if !matched {
		sqsReceiptHandleInvalid(w, req.ReceiptHandle)
		return
	}
	if !inflight {
		sqsErrorJSON(w, "AWS.SimpleQueueService.MessageNotInflight",
			"The message referred to is not in flight.", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleSQSChangeMessageVisibilityBatch changes visibility for up to 10
// in-flight messages in one call, reporting per-entry success/failure. The
// batch-level failures (empty list, >10 entries, duplicate Ids) are top-level
// errors; per-entry failures (invalid handle, not in flight, out-of-range
// timeout) land in the Failed array with HTTP 200.
func handleSQSChangeMessageVisibilityBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
		Entries  []struct {
			Id                string `json:"Id"`
			ReceiptHandle     string `json:"ReceiptHandle"`
			VisibilityTimeout int    `json:"VisibilityTimeout"`
		} `json:"Entries"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if len(req.Entries) == 0 {
		sqsErrorJSON(w, "AWS.SimpleQueueService.EmptyBatchRequest",
			"There should be at least one ChangeMessageVisibilityBatchRequestEntry in the request.",
			http.StatusBadRequest)
		return
	}
	if len(req.Entries) > 10 {
		sqsErrorJSON(w, "AWS.SimpleQueueService.TooManyEntriesInBatchRequest",
			"Maximum number of entries per request are 10. You have sent "+strconv.Itoa(len(req.Entries))+".",
			http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, e := range req.Entries {
		if seen[e.Id] {
			sqsErrorJSON(w, "AWS.SimpleQueueService.BatchEntryIdsNotDistinct",
				"Id "+e.Id+" repeated.", http.StatusBadRequest)
			return
		}
		seen[e.Id] = true
	}

	successful := make([]map[string]string, 0, len(req.Entries))
	failed := make([]map[string]any, 0)
	for _, e := range req.Entries {
		if e.VisibilityTimeout < 0 || e.VisibilityTimeout > 43200 {
			failed = append(failed, map[string]any{
				"Id":          e.Id,
				"Code":        "InvalidParameterValue",
				"Message":     fmt.Sprintf("Value %d for parameter VisibilityTimeout is invalid. Reason: must be between 0 and 43200.", e.VisibilityTimeout),
				"SenderFault": true,
			})
			continue
		}
		matched, inflight := sqsApplyVisibility(name, e.ReceiptHandle, e.VisibilityTimeout)
		switch {
		case !matched:
			failed = append(failed, map[string]any{
				"Id":          e.Id,
				"Code":        "ReceiptHandleIsInvalid",
				"Message":     `The input receipt handle "` + e.ReceiptHandle + `" is not a valid receipt handle.`,
				"SenderFault": true,
			})
		case !inflight:
			failed = append(failed, map[string]any{
				"Id":          e.Id,
				"Code":        "AWS.SimpleQueueService.MessageNotInflight",
				"Message":     "The message referred to is not in flight.",
				"SenderFault": true,
			})
		default:
			successful = append(successful, map[string]string{"Id": e.Id})
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"Successful": successful,
		"Failed":     failed,
	})
}

// sqsPolicyDocument parses the queue's Policy attribute, or returns an empty 2012-10-17 document.
func sqsPolicyDocument(raw string) (map[string]any, []map[string]any, error) {
	policy := map[string]any{
		"Version":   "2012-10-17",
		"Statement": []map[string]any{},
	}
	if raw == "" {
		return policy, []map[string]any{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil, nil, err
	}
	statements := []map[string]any{}
	if raw, ok := policy["Statement"]; ok {
		if arr, ok := raw.([]any); ok {
			for _, s := range arr {
				if m, ok := s.(map[string]any); ok {
					statements = append(statements, m)
				}
			}
		}
	}
	return policy, statements, nil
}

// sqsStoreQueuePolicy writes the policy to Attributes["Policy"] and to the IAM
// resource-policy mirror, as SetQueueAttributes(Policy) does; an empty policy
// clears both.
func sqsStoreQueuePolicy(name, queueARN, policyJSON string) {
	sqsQueues.Update(name, func(q *SQSQueue) {
		if q.Attributes == nil {
			q.Attributes = map[string]string{}
		}
		if policyJSON == "" {
			delete(q.Attributes, "Policy")
		} else {
			q.Attributes["Policy"] = policyJSON
		}
	})
	if policyJSON == "" {
		iamDeleteResourcePolicy(queueARN)
	} else {
		iamPutResourcePolicy(queueARN, policyJSON)
	}
}

// handleSQSAddPermission appends an Allow statement (Sid=Label) granting the
// named accounts the named actions, stored as "SQS:" actions and account-root
// principals, as SQS canonicalizes them.
func handleSQSAddPermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl      string   `json:"QueueUrl"`
		Label         string   `json:"Label"`
		AWSAccountIds []string `json:"AWSAccountIds"`
		Actions       []string `json:"Actions"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	q, ok := sqsQueues.Get(name)
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if req.Label == "" {
		sqsErrorJSON(w, "MissingParameter", "Label is required", http.StatusBadRequest)
		return
	}
	if len(req.AWSAccountIds) == 0 || len(req.Actions) == 0 {
		sqsErrorJSON(w, "MissingParameter",
			"AWSAccountIds and Actions are required", http.StatusBadRequest)
		return
	}
	policy, statements, err := sqsPolicyDocument(q.Attributes["Policy"])
	if err != nil {
		sqsErrorJSON(w, "InternalError",
			"Stored queue policy is not valid JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, s := range statements {
		if sid, _ := s["Sid"].(string); sid == req.Label {
			// SQS rejects a duplicate label.
			sqsErrorJSON(w, "InvalidParameterValue",
				"Value "+req.Label+" for parameter Label is invalid. Reason: Already exists.",
				http.StatusBadRequest)
			return
		}
	}
	principals := make([]string, 0, len(req.AWSAccountIds))
	for _, acct := range req.AWSAccountIds {
		principals = append(principals, "arn:aws:iam::"+acct+":root")
	}
	actions := make([]string, 0, len(req.Actions))
	for _, a := range req.Actions {
		if strings.Contains(a, ":") {
			actions = append(actions, a)
		} else {
			actions = append(actions, "SQS:"+a)
		}
	}
	statements = append(statements, map[string]any{
		"Sid":       req.Label,
		"Effect":    "Allow",
		"Principal": map[string]any{"AWS": principals},
		"Action":    actions,
		"Resource":  q.ARN,
	})
	policy["Statement"] = statements
	body, _ := json.Marshal(policy)
	sqsStoreQueuePolicy(name, q.ARN, string(body))
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleSQSRemovePermission removes the statement with Sid=Label; removing the
// last statement clears the policy, as in SQS.
func handleSQSRemovePermission(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
		Label    string `json:"Label"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	q, ok := sqsQueues.Get(name)
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	if req.Label == "" {
		sqsErrorJSON(w, "MissingParameter", "Label is required", http.StatusBadRequest)
		return
	}
	policy, statements, err := sqsPolicyDocument(q.Attributes["Policy"])
	if err != nil {
		sqsErrorJSON(w, "InternalError",
			"Stored queue policy is not valid JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filtered := make([]map[string]any, 0, len(statements))
	found := false
	for _, s := range statements {
		if sid, _ := s["Sid"].(string); sid == req.Label {
			found = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !found {
		sqsErrorJSON(w, "InvalidParameterValue",
			"Value "+req.Label+" for parameter Label is invalid. Reason: can't find label on existing policy.",
			http.StatusBadRequest)
		return
	}
	if len(filtered) == 0 {
		sqsStoreQueuePolicy(name, q.ARN, "")
	} else {
		policy["Statement"] = filtered
		body, _ := json.Marshal(policy)
		sqsStoreQueuePolicy(name, q.ARN, string(body))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQSTagQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string            `json:"QueueUrl"`
		Tags     map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	sqsQueues.Update(name, func(q *SQSQueue) {
		if q.Tags == nil {
			q.Tags = map[string]string{}
		}
		for k, v := range req.Tags {
			q.Tags[k] = v
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQSUntagQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string   `json:"QueueUrl"`
		TagKeys  []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	name := queueNameFromURL(req.QueueUrl)
	if _, ok := sqsQueues.Get(name); !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	sqsQueues.Update(name, func(q *SQSQueue) {
		for _, k := range req.TagKeys {
			delete(q.Tags, k)
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSQSListQueueTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sqsErrorJSON(w, "MalformedInputException", err.Error(), http.StatusBadRequest)
		return
	}
	q, ok := sqsQueues.Get(queueNameFromURL(req.QueueUrl))
	if !ok {
		sqsQueueDoesNotExist(w)
		return
	}
	tags := q.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

// sqsQueueIsFifo reports the FifoQueue attribute, which SQS keys FIFO behaviour
// off and CreateQueue holds to the .fifo suffix.
func sqsQueueIsFifo(q SQSQueue) bool {
	return strings.EqualFold(q.Attributes["FifoQueue"], "true")
}

// sqsContentBasedDedup reports whether the queue has
// ContentBasedDeduplication enabled — when set, a FIFO send may omit
// MessageDeduplicationId (SQS derives the dedup id from the body).
func sqsContentBasedDedup(q SQSQueue) bool {
	return strings.EqualFold(q.Attributes["ContentBasedDeduplication"], "true")
}

// sqsFifoNameAttrMismatch enforces SQS's coupling of the .fifo suffix and
// FifoQueue=true, returning the error message and false on a mismatch.
func sqsFifoNameAttrMismatch(name string, attrs map[string]string) (string, bool) {
	hasSuffix := strings.HasSuffix(name, ".fifo")
	fifoAttr := strings.EqualFold(attrs["FifoQueue"], "true")
	if fifoAttr && !hasSuffix {
		return "The name of a FIFO queue can only include alphanumeric characters, hyphens, or underscores, must end with the .fifo suffix and be 1 to 80 in length", false
	}
	if hasSuffix && !fifoAttr {
		// SQS allows a dot in a queue name only in the .fifo suffix of a FIFO queue.
		return "Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length", false
	}
	return "", true
}

// sqsAttributesEqual reports whether two attribute maps are equal (CreateQueue
// idempotency: same name + same attributes → OK; differing → QueueNameExists).
func sqsAttributesEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
