package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// xmlEscape escapes &, <, >, ", ' for inclusion in awsQuery XML
// response bodies and attribute values.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Amazon Simple Notification Service (SNS) implements topic publication,
// subscription filtering, and delivery to Amazon SQS, Lambda, and signed
// HTTP/HTTPS endpoints.
//
// Wire protocol: awsQuery (POST / + Action form param + XML envelope),
// same as SQS.

type SNSTopic struct {
	Name string
	ARN  string
	Tags map[string]string
	// Attributes are mutable settings — Policy, DisplayName,
	// DeliveryPolicy, KmsMasterKeyId, etc. — set via
	// SetTopicAttributes and surfaced by GetTopicAttributes alongside
	// the fixed read-only Owner / SubscriptionsConfirmed fields.
	Attributes map[string]string
}

type SNSSubscription struct {
	ARN       string
	TopicARN  string
	Protocol  string // "sqs", "http", "https", "email", "sms", "lambda", "firehose"
	Endpoint  string // queue ARN for sqs, URL for http(s), email addr, etc.
	Confirmed bool
	// ControlPlaneOrigin is the SNS endpoint coordinate used to form the
	// SubscribeURL, UnsubscribeURL, and SigningCertURL delivered to HTTP
	// subscribers. It is internal metadata, not an SNS response member.
	ControlPlaneOrigin string
	// Attributes holds the mutable subscription settings set via
	// SetSubscriptionAttributes — RawMessageDelivery, FilterPolicy,
	// FilterPolicyScope, DeliveryPolicy, RedrivePolicy — surfaced by
	// GetSubscriptionAttributes alongside the fixed read-only fields.
	Attributes map[string]string
}

var (
	snsTopics        sim.Store[SNSTopic]
	snsSubscriptions sim.Store[SNSSubscription]
)

func snsTopicARN(name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:%s",
		awsRegion(), awsAccountID(), name)
}

func snsSubscriptionARN(topicName string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:%s:%s",
		awsRegion(), awsAccountID(), topicName, generateUUID())
}

// snsAPIVersion is the canonical AWS SNS API version (Query
// Protocol). Used to disambiguate Action names from other awsQuery
// services in the AWSQueryRouter dispatch.
const snsAPIVersion = "2010-03-31"

func registerSNS(r *sim.AWSQueryRouter, srv *sim.Server) {
	// Publish is a CloudTrail DATA event (excluded from LookupEvents).
	cloudTrailDeclareDataEvents("sns.amazonaws.com", "Publish", "PublishBatch")
	snsTopics = sim.MakeStore[SNSTopic](srv.DB(), "sns_topics")
	snsSubscriptions = sim.MakeStore[SNSSubscription](srv.DB(), "sns_subscriptions")
	registerSNSHTTPDelivery(srv)

	r.RegisterVersioned(snsAPIVersion, "CreateTopic", handleSNSCreateTopic)
	r.RegisterVersioned(snsAPIVersion, "DeleteTopic", handleSNSDeleteTopic)
	r.RegisterVersioned(snsAPIVersion, "ListTopics", handleSNSListTopics)
	r.RegisterVersioned(snsAPIVersion, "GetTopicAttributes", handleSNSGetTopicAttributes)
	r.RegisterVersioned(snsAPIVersion, "SetTopicAttributes", handleSNSSetTopicAttributes)
	r.RegisterVersioned(snsAPIVersion, "Subscribe", handleSNSSubscribe)
	r.RegisterVersioned(snsAPIVersion, "Unsubscribe", handleSNSUnsubscribe)
	r.RegisterVersioned(snsAPIVersion, "ConfirmSubscription", handleSNSConfirmSubscription)
	r.RegisterVersioned(snsAPIVersion, "GetSubscriptionAttributes", handleSNSGetSubscriptionAttributes)
	r.RegisterVersioned(snsAPIVersion, "SetSubscriptionAttributes", handleSNSSetSubscriptionAttributes)
	r.RegisterVersioned(snsAPIVersion, "ListSubscriptions", handleSNSListSubscriptions)
	r.RegisterVersioned(snsAPIVersion, "ListSubscriptionsByTopic", handleSNSListSubscriptionsByTopic)
	r.RegisterVersioned(snsAPIVersion, "AddPermission", handleSNSAddPermission)
	r.RegisterVersioned(snsAPIVersion, "RemovePermission", handleSNSRemovePermission)
	r.RegisterVersioned(snsAPIVersion, "Publish", handleSNSPublish)
	r.RegisterVersioned(snsAPIVersion, "PublishBatch", handleSNSPublishBatch)
	r.RegisterVersioned(snsAPIVersion, "TagResource", handleSNSTagResource)
	r.RegisterVersioned(snsAPIVersion, "UntagResource", handleSNSUntagResource)
	r.RegisterVersioned(snsAPIVersion, "ListTagsForResource", handleSNSListTagsForResource)

	// Mobile-push, SMS, and data-protection control-plane slices.
	registerSNSMobileSMS(r, srv)
}

func snsXMLResponse(w http.ResponseWriter, op string, body string, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w,
		`<%sResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/">%s<ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata></%sResponse>`,
		op, body, requestID, op)
}

func snsErrorXML(w http.ResponseWriter, code, message string, status int, requestID string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w,
		`<ErrorResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		code, message, requestID)
}

func handleSNSCreateTopic(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		snsErrorXML(w, "InvalidParameter", "Name is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	// CreateTopic carries initial topic attributes as Attributes.entry.N.{key,value}
	// (the SNS query flattening of the Attributes map). FifoTopic, in
	// particular, must be set here so the .fifo-suffix coupling and the
	// per-message FIFO rules apply.
	attrs := snsCreateTopicAttributes(r)
	if msg, ok := snsFifoNameAttrMismatch(name, attrs); !ok {
		snsErrorXML(w, "InvalidParameter", msg, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	arn := snsTopicARN(name)
	if _, ok := snsTopics.Get(name); !ok {
		snsTopics.Put(name, SNSTopic{Name: name, ARN: arn, Tags: make(map[string]string), Attributes: attrs})
	}
	body := fmt.Sprintf("<CreateTopicResult><TopicArn>%s</TopicArn></CreateTopicResult>", xmlEscape(arn))
	snsXMLResponse(w, "CreateTopic", body, sim.RequestID(r.Context()))
}

// snsCreateTopicAttributes pulls the initial Attributes map out of a
// CreateTopic query request (Attributes.entry.N.key / .value).
func snsCreateTopicAttributes(r *http.Request) map[string]string {
	out := map[string]string{}
	for i := 1; i <= 50; i++ {
		k := r.FormValue(fmt.Sprintf("Attributes.entry.%d.key", i))
		if k == "" {
			break
		}
		out[k] = r.FormValue(fmt.Sprintf("Attributes.entry.%d.value", i))
	}
	return out
}

// snsTopicIsFifo reports whether a topic is FIFO (FifoTopic=true).
func snsTopicIsFifo(t SNSTopic) bool {
	return strings.EqualFold(t.Attributes["FifoTopic"], "true")
}

// snsTopicContentBasedDedup reports whether the topic has
// ContentBasedDeduplication enabled.
func snsTopicContentBasedDedup(t SNSTopic) bool {
	return strings.EqualFold(t.Attributes["ContentBasedDeduplication"], "true")
}

// snsFifoNameAttrMismatch enforces the real-SNS coupling between the
// .fifo name suffix and FifoTopic=true: a topic named "<x>.fifo"
// requires FifoTopic=true and vice-versa. Returns (errMessage, ok=false)
// on mismatch.
func snsFifoNameAttrMismatch(name string, attrs map[string]string) (string, bool) {
	hasSuffix := strings.HasSuffix(name, ".fifo")
	fifoAttr := strings.EqualFold(attrs["FifoTopic"], "true")
	if fifoAttr && !hasSuffix {
		return "Fifo topic names must end with .fifo and be 1 to 256 characters long.", false
	}
	if hasSuffix && !fifoAttr {
		return "Topic names must be made up of only uppercase and lowercase ASCII letters, numbers, underscores, and hyphens, and must be between 1 and 256 characters long.", false
	}
	return "", true
}

func handleSNSDeleteTopic(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TopicArn")
	name := snsTopicNameFromARN(arn)
	// snsTopics.Delete returning false is fine here — real SNS
	// DeleteTopic is idempotent and returns success even when the
	// topic doesn't exist. The cascade-clear below runs either way
	// so any dangling subscriptions also get cleared.
	snsTopics.Delete(name)
	// Cascade: drop subscriptions pointing at this topic.
	for _, sub := range snsSubscriptions.List() {
		if sub.TopicARN == arn {
			snsSubscriptions.Delete(sub.ARN)
		}
	}
	snsXMLResponse(w, "DeleteTopic", "", sim.RequestID(r.Context()))
}

func snsTopicNameFromARN(arn string) string {
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func handleSNSListTopics(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	token := r.FormValue("NextToken")
	all := snsTopics.List()
	sortBy(all, func(t SNSTopic) string { return t.ARN })
	page, next := awsPage(all, token, 0, 100)

	var b strings.Builder
	b.WriteString("<ListTopicsResult><Topics>")
	for _, t := range page {
		fmt.Fprintf(&b, "<member><TopicArn>%s</TopicArn></member>", xmlEscape(t.ARN))
	}
	b.WriteString("</Topics>")
	if next != "" {
		fmt.Fprintf(&b, "<NextToken>%s</NextToken>", xmlEscape(next))
	}
	b.WriteString("</ListTopicsResult>")
	snsXMLResponse(w, "ListTopics", b.String(), sim.RequestID(r.Context()))
}

func handleSNSGetTopicAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TopicArn")
	name := snsTopicNameFromARN(arn)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	// Subscription counts are the load-bearing attribute for
	// real-world consumers (CloudWatch alarms on confirmed-vs-pending).
	confirmed := 0
	pending := 0
	for _, sub := range snsSubscriptions.List() {
		if sub.TopicARN == t.ARN {
			if sub.Confirmed {
				confirmed++
			} else {
				pending++
			}
		}
	}
	attrs := map[string]string{
		"TopicArn":               t.ARN,
		"DisplayName":            t.Name,
		"Owner":                  awsAccountID(),
		"SubscriptionsConfirmed": fmt.Sprintf("%d", confirmed),
		"SubscriptionsPending":   fmt.Sprintf("%d", pending),
		"SubscriptionsDeleted":   "0",
	}
	// Mutable attributes set via SetTopicAttributes override the
	// fixed defaults — real SNS does the same (e.g. a `DisplayName`
	// set on the topic replaces the auto-derived value).
	for k, v := range t.Attributes {
		attrs[k] = v
	}
	var b strings.Builder
	b.WriteString("<GetTopicAttributesResult><Attributes>")
	for k, v := range attrs {
		fmt.Fprintf(&b, "<entry><key>%s</key><value>%s</value></entry>",
			xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</Attributes></GetTopicAttributesResult>")
	snsXMLResponse(w, "GetTopicAttributes", b.String(), sim.RequestID(r.Context()))
}

// handleSNSSetTopicAttributes updates a single (AttributeName,
// AttributeValue) pair on a topic. terraform-provider-aws emits this
// repeatedly on aws_sns_topic for DeliveryPolicy / Policy / KmsMasterKeyId /
// etc.; pre-fix the sim returned InvalidAction and the apply failed.
func handleSNSSetTopicAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("TopicArn")
	name := snsTopicNameFromARN(arn)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	attrName := r.FormValue("AttributeName")
	attrValue := r.FormValue("AttributeValue")
	if attrName == "FilterPolicy" {
		if err := snsValidateFilterPolicy(attrValue); err != nil {
			snsErrorXML(w, "InvalidParameter", err.Error(), http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	}
	if attrName == "FilterPolicyScope" &&
		attrValue != "" && attrValue != "MessageAttributes" && attrValue != "MessageBody" {
		snsErrorXML(w, "InvalidParameter", "FilterPolicyScope must be MessageAttributes or MessageBody",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if attrName == "" {
		snsErrorXML(w, "InvalidParameter",
			"AttributeName is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if t.Attributes == nil {
		t.Attributes = map[string]string{}
	}
	if attrValue == "" {
		delete(t.Attributes, attrName)
	} else {
		t.Attributes[attrName] = attrValue
	}
	snsTopics.Put(name, t)
	// Mirror the topic policy into the central resource-policy store so the IAM
	// enforcement gate can resolve it by the topic ARN.
	if attrName == "Policy" {
		if attrValue == "" {
			iamDeleteResourcePolicy(t.ARN)
		} else {
			iamPutResourcePolicy(t.ARN, attrValue)
		}
	}
	snsXMLResponse(w, "SetTopicAttributes", "", sim.RequestID(r.Context()))
}

func handleSNSSubscribe(w http.ResponseWriter, r *http.Request) {
	topicARN := r.FormValue("TopicArn")
	protocol := r.FormValue("Protocol")
	endpoint := r.FormValue("Endpoint")
	if topicARN == "" || protocol == "" || endpoint == "" {
		snsErrorXML(w, "InvalidParameter",
			"TopicArn, Protocol, and Endpoint are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	name := snsTopicNameFromARN(topicARN)
	if _, ok := snsTopics.Get(name); !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	attributes := snsAttributesMap(r, "Attributes")
	if strings.EqualFold(protocol, "firehose") {
		roleARN := attributes["SubscriptionRoleArn"]
		if roleARN == "" {
			snsErrorXML(w, "InvalidParameter", "SubscriptionRoleArn is required for a Firehose subscription",
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		if _, ok := firehoseStreamByARN(endpoint); !ok {
			snsErrorXML(w, "InvalidParameter", "Endpoint must identify an existing Firehose stream",
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		if err := iamValidateServiceRole(roleARN, "sns.amazonaws.com", map[string]string{
			"firehose:PutRecord": endpoint,
		}); err != nil {
			snsErrorXML(w, "InvalidParameter", err.Error(),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		attributes["SubscriptionRoleArn"] = roleARN
	}
	sub := SNSSubscription{
		ARN:                snsSubscriptionARN(name),
		TopicARN:           topicARN,
		Protocol:           protocol,
		Endpoint:           endpoint,
		Confirmed:          !snsProtocolRequiresConfirmation(protocol),
		ControlPlaneOrigin: snsRequestOrigin(r),
		Attributes:         attributes,
	}
	snsSubscriptions.Put(sub.ARN, sub)
	if !sub.Confirmed && (strings.EqualFold(protocol, "http") || strings.EqualFold(protocol, "https")) {
		go snsDeliverHTTPConfirmation(sub)
	}
	if !sub.Confirmed && (strings.EqualFold(protocol, "email") || strings.EqualFold(protocol, "email-json")) {
		if _, err := snsEmailDomain(endpoint); err != nil {
			snsSubscriptions.Delete(sub.ARN)
			snsErrorXML(w, "InvalidParameter", "Invalid parameter: Endpoint", http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		go snsDeliverEmailConfirmation(sub)
	}
	returnedARN := sub.ARN
	if !sub.Confirmed && !snsReturnSubscriptionARN(r) {
		returnedARN = "pending confirmation"
	}
	body := fmt.Sprintf("<SubscribeResult><SubscriptionArn>%s</SubscriptionArn></SubscribeResult>", xmlEscape(returnedARN))
	snsXMLResponse(w, "Subscribe", body, sim.RequestID(r.Context()))
}

func snsProtocolRequiresConfirmation(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "sqs", "lambda", "application", "firehose":
		return false
	default:
		return true
	}
}

func snsReturnSubscriptionARN(r *http.Request) bool {
	return strings.EqualFold(r.FormValue("ReturnSubscriptionArn"), "true")
}

func handleSNSUnsubscribe(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SubscriptionArn")
	snsSubscriptions.Delete(arn)
	snsXMLResponse(w, "Unsubscribe", "", sim.RequestID(r.Context()))
}

// handleSNSConfirmSubscription confirms a pending subscription. Real SNS
// auto-confirms sqs/lambda/application/firehose subscriptions at Subscribe
// time; HTTP(S)/email subscribers receive a token they echo back here to
// flip the subscription to confirmed. The sim issues a deterministic token
// per (topic, subscription) at Subscribe time (snsConfirmationToken); a
// ConfirmSubscription call resolves that token to its subscription, marks it
// confirmed, and returns the ARN — and is idempotent on already-confirmed
// subscriptions, matching real SNS.
func handleSNSConfirmSubscription(w http.ResponseWriter, r *http.Request) {
	topicARN := r.FormValue("TopicArn")
	token := r.FormValue("Token")
	if topicARN == "" || token == "" {
		snsErrorXML(w, "InvalidParameter",
			"TopicArn and Token are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	name := snsTopicNameFromARN(topicARN)
	if _, ok := snsTopics.Get(name); !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var found *SNSSubscription
	for _, sub := range snsSubscriptions.List() {
		if sub.TopicARN == topicARN && snsConfirmationToken(sub) == token {
			s := sub
			found = &s
			break
		}
	}
	if found == nil {
		snsErrorXML(w, "InvalidParameter",
			"Invalid token", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if !found.Confirmed {
		found.Confirmed = true
		snsSubscriptions.Put(found.ARN, *found)
	}
	body := fmt.Sprintf("<ConfirmSubscriptionResult><SubscriptionArn>%s</SubscriptionArn></ConfirmSubscriptionResult>",
		xmlEscape(found.ARN))
	snsXMLResponse(w, "ConfirmSubscription", body, sim.RequestID(r.Context()))
}

// snsConfirmationToken derives the deterministic confirmation token a
// subscription's confirmation message carries. Keying it off the
// subscription ARN means the Subscribe path issues it implicitly (the ARN is
// returned to the subscriber) and ConfirmSubscription can resolve it without
// extra stored state.
func snsConfirmationToken(sub SNSSubscription) string {
	return strings.ReplaceAll(sub.ARN, ":", "")
}

// handleSNSGetSubscriptionAttributes returns a subscription's attributes —
// the fixed read-only fields (SubscriptionArn, TopicArn, Protocol, Endpoint,
// Owner, ConfirmationWasAuthenticated, PendingConfirmation, RawMessageDelivery)
// plus any FilterPolicy / DeliveryPolicy / RedrivePolicy set via
// SetSubscriptionAttributes.
func handleSNSGetSubscriptionAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SubscriptionArn")
	sub, ok := snsSubscriptions.Get(arn)
	if !ok {
		snsErrorXML(w, "NotFound", "Subscription does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	pending := "false"
	if !sub.Confirmed {
		pending = "true"
	}
	attrs := map[string]string{
		"SubscriptionArn":              sub.ARN,
		"TopicArn":                     sub.TopicARN,
		"Protocol":                     sub.Protocol,
		"Endpoint":                     sub.Endpoint,
		"Owner":                        awsAccountID(),
		"ConfirmationWasAuthenticated": "false",
		"PendingConfirmation":          pending,
		"RawMessageDelivery":           "false",
	}
	// Attributes set via SetSubscriptionAttributes override the defaults
	// (e.g. RawMessageDelivery=true) and add the optional policy documents.
	for k, v := range sub.Attributes {
		attrs[k] = v
	}
	var b strings.Builder
	b.WriteString("<GetSubscriptionAttributesResult><Attributes>")
	for k, v := range attrs {
		fmt.Fprintf(&b, "<entry><key>%s</key><value>%s</value></entry>",
			xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</Attributes></GetSubscriptionAttributesResult>")
	snsXMLResponse(w, "GetSubscriptionAttributes", b.String(), sim.RequestID(r.Context()))
}

// handleSNSSetSubscriptionAttributes stores a single (AttributeName,
// AttributeValue) pair on a subscription — RawMessageDelivery, FilterPolicy,
// FilterPolicyScope, DeliveryPolicy, RedrivePolicy. terraform-provider-aws
// emits this on aws_sns_topic_subscription.
func handleSNSSetSubscriptionAttributes(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("SubscriptionArn")
	sub, ok := snsSubscriptions.Get(arn)
	if !ok {
		snsErrorXML(w, "NotFound", "Subscription does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	attrName := r.FormValue("AttributeName")
	if attrName == "" {
		snsErrorXML(w, "InvalidParameter",
			"AttributeName is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	attrValue := r.FormValue("AttributeValue")
	if sub.Attributes == nil {
		sub.Attributes = map[string]string{}
	}
	if attrValue == "" {
		delete(sub.Attributes, attrName)
	} else {
		sub.Attributes[attrName] = attrValue
	}
	snsSubscriptions.Put(arn, sub)
	snsXMLResponse(w, "SetSubscriptionAttributes", "", sim.RequestID(r.Context()))
}

func handleSNSListSubscriptions(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<ListSubscriptionsResult><Subscriptions>")
	for _, sub := range snsSubscriptions.List() {
		fmt.Fprintf(&b,
			"<member><SubscriptionArn>%s</SubscriptionArn><TopicArn>%s</TopicArn><Protocol>%s</Protocol><Endpoint>%s</Endpoint><Owner>%s</Owner></member>",
			xmlEscape(sub.ARN), xmlEscape(sub.TopicARN), xmlEscape(sub.Protocol),
			xmlEscape(sub.Endpoint), awsAccountID())
	}
	b.WriteString("</Subscriptions></ListSubscriptionsResult>")
	snsXMLResponse(w, "ListSubscriptions", b.String(), sim.RequestID(r.Context()))
}

func handleSNSListSubscriptionsByTopic(w http.ResponseWriter, r *http.Request) {
	topicARN := r.FormValue("TopicArn")
	var b strings.Builder
	b.WriteString("<ListSubscriptionsByTopicResult><Subscriptions>")
	for _, sub := range snsSubscriptions.List() {
		if sub.TopicARN != topicARN {
			continue
		}
		fmt.Fprintf(&b,
			"<member><SubscriptionArn>%s</SubscriptionArn><TopicArn>%s</TopicArn><Protocol>%s</Protocol><Endpoint>%s</Endpoint><Owner>%s</Owner></member>",
			xmlEscape(sub.ARN), xmlEscape(sub.TopicARN), xmlEscape(sub.Protocol),
			xmlEscape(sub.Endpoint), awsAccountID())
	}
	b.WriteString("</Subscriptions></ListSubscriptionsByTopicResult>")
	snsXMLResponse(w, "ListSubscriptionsByTopic", b.String(), sim.RequestID(r.Context()))
}

// handleSNSPublish fans the message out through the real delivery path for
// each supported protocol.
// snsPublishEntry is the common publish payload shared by Publish and
// each PublishBatch entry.
type snsPublishEntry struct {
	Id                     string
	Message                string
	Subject                string
	MessageGroupId         string
	MessageDeduplicationId string
	MessageAttributes      map[string]SQSMessageAttribute
}

// snsValidatePublish applies the per-message validation real SNS
// performs: non-empty message, the 256 KiB limit, and — for a FIFO
// topic — the MessageGroupId and dedup requirements. Returns an
// (errCode, message) pair; empty code means valid.
func snsValidatePublish(t SNSTopic, e snsPublishEntry) (string, string) {
	if e.Message == "" {
		return "InvalidParameter", "Invalid parameter: Empty value for parameter Message"
	}
	if len(e.Message) > 262144 {
		return "InvalidParameter", "Invalid parameter: Message too long"
	}
	if snsTopicIsFifo(t) {
		if e.MessageGroupId == "" {
			return "InvalidParameter", "Invalid parameter: The MessageGroupId parameter is required for FIFO topics"
		}
		if !snsTopicContentBasedDedup(t) && e.MessageDeduplicationId == "" {
			return "InvalidParameter",
				"Invalid parameter: The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly"
		}
	}
	return "", ""
}

// snsARNAccount extracts the account-id field (the 5th colon-delimited
// segment) from an ARN — arn:aws:<service>:<region>:<account>:<resource>.
// Returns the empty string when the ARN is malformed.
func snsARNAccount(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

// snsNotificationEnvelope renders SNS's canonical Notification JSON, the
// body an SQS subscriber receives and the inner record of a Lambda SNS
// event. Built with json.Marshal so embedded alarm JSON is always valid JSON
// (fmt.Sprintf %q can emit \x escapes that JSON parsers reject).
func snsNotificationEnvelope(topicARN, msgID, subject, message string, attributes map[string]SQSMessageAttribute) string {
	env := map[string]any{
		"Type":      "Notification",
		"MessageId": msgID,
		"TopicArn":  topicARN,
		"Subject":   subject,
		"Message":   message,
		"Timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if messageAttributes := snsMessageAttributesEnvelope(attributes); messageAttributes != nil {
		env["MessageAttributes"] = messageAttributes
	}
	b, _ := json.Marshal(env)
	return string(b)
}

// snsFanout delivers one published message to the topic's subscribers
// in-process. Each delivery is gated by the TARGET resource's
// resource-based policy under the AWS-service-initiated IAM context
// (sns.amazonaws.com originating from the topic ARN): a target that
// doesn't admit SNS is skipped, exactly as real AWS drops an
// unauthorized delivery. SQS subscribers get the Notification envelope
// enqueued; Lambda subscribers get a real in-process invoke with the
// SNS event payload.
func snsFanout(topicARN, msgID, subject, message string, attributes map[string]SQSMessageAttribute) {
	srcAccount := snsARNAccount(topicARN)
	subs := snsSubscriptions.List()
	matching := 0
	for _, sub := range subs {
		if sub.TopicARN == topicARN {
			matching++
		}
	}
	cwEvalLogger.Info().Str("topicARN", topicARN).Str("msgID", msgID).Int("totalSubscriptions", len(subs)).Int("matchingSubscriptions", matching).Msg("SNS fanout starting")
	if matching == 0 {
		cwEvalLogger.Info().Str("topicARN", topicARN).Str("msgID", msgID).Msg("SNS fanout found no subscriptions for topic")
		return
	}
	for _, sub := range subs {
		if sub.TopicARN != topicARN || !sub.Confirmed {
			continue
		}
		if !snsSubscriptionMatches(sub, message, attributes) {
			continue
		}
		src := iamServiceSource{
			Service:       "sns.amazonaws.com",
			SourceArn:     topicARN,
			SourceAccount: srcAccount,
		}
		cwEvalLogger.Info().Str("topicARN", topicARN).Str("msgID", msgID).Str("protocol", sub.Protocol).Str("endpoint", sub.Endpoint).Msg("SNS fanout delivering to subscription")
		switch strings.ToLower(sub.Protocol) {
		case "sqs":
			snsDeliverToSQS(sub, topicARN, msgID, subject, message, attributes, src)
		case "lambda":
			snsDeliverToLambda(sub.Endpoint, topicARN, msgID, subject, message, attributes, src)
		case "http", "https":
			go snsDeliverHTTPNotification(sub, msgID, subject, message, attributes)
		case "email", "email-json":
			go snsDeliverEmailNotification(sub, msgID, subject, message, attributes)
		case "firehose":
			snsDeliverToFirehose(sub, topicARN, msgID, subject, message, attributes)
		default:
			cwEvalLogger.Info().Str("topicARN", topicARN).Str("msgID", msgID).Str("protocol", sub.Protocol).Str("endpoint", sub.Endpoint).Msg("SNS fanout skipping unsupported subscription protocol")
		}
	}
}

func snsDeliverToFirehose(sub SNSSubscription, topicARN, msgID, subject, message string, attributes map[string]SQSMessageAttribute) {
	if err := iamValidateServiceRole(sub.Attributes["SubscriptionRoleArn"], "sns.amazonaws.com", map[string]string{
		"firehose:PutRecord": sub.Endpoint,
	}); err != nil {
		cwEvalLogger.Error().Err(err).Str("firehoseARN", sub.Endpoint).Str("topicARN", topicARN).
			Msg("Amazon SNS cannot assume its Amazon Data Firehose delivery role")
		return
	}
	body := snsNotificationEnvelope(topicARN, msgID, subject, message, attributes)
	if strings.EqualFold(sub.Attributes["RawMessageDelivery"], "true") {
		body = message
	}
	if err := firehosePutServiceRecord(sub.Endpoint, []byte(body)); err != nil {
		cwEvalLogger.Error().Err(err).Str("firehoseARN", sub.Endpoint).Str("topicARN", topicARN).Msg("Amazon SNS to Amazon Data Firehose delivery failed")
	}
}

// snsDeliverToSQS enqueues the SNS Notification envelope into the
// subscriber queue — but only when the queue's resource policy admits
// sns:SendMessage from this topic. Endpoint is the queue ARN
// (arn:aws:sqs:<region>:<account>:<queue-name>).
func snsDeliverToSQS(sub SNSSubscription, topicARN, msgID, subject, message string, attributes map[string]SQSMessageAttribute, src iamServiceSource) {
	queueARN := sub.Endpoint
	if !iamAuthorizeServiceDelivery(queueARN, "sqs:SendMessage", src) {
		cwEvalLogger.Info().Str("queueARN", queueARN).Str("topicARN", topicARN).Str("sourceService", src.Service).Msg("SNS to SQS delivery denied by resource policy")
		return
	}
	queueName := snsTopicNameFromARN(queueARN)
	if _, ok := sqsQueues.Get(queueName); !ok {
		cwEvalLogger.Info().Str("queueARN", queueARN).Str("queueName", queueName).Msg("SNS to SQS delivery target queue not found")
		return
	}
	body := snsNotificationEnvelope(topicARN, msgID, subject, message, attributes)
	var sqsAttributes map[string]SQSMessageAttribute
	if strings.EqualFold(sub.Attributes["RawMessageDelivery"], "true") {
		body = message
		sqsAttributes = attributes
	}
	sqsEnqueueBodyWithAttributes(queueName, body, sqsAttributes)
	cwEvalLogger.Info().Str("queueARN", queueARN).Str("queueName", queueName).Str("topicARN", topicARN).Str("msgID", msgID).Msg("SNS to SQS delivery succeeded")
}

// snsDeliverToLambda performs a real in-process Lambda invoke with the
// SNS event payload — but only when the function's resource policy
// admits lambda:InvokeFunction from this topic. SNS→Lambda is an async
// (Event) delivery, so the invoke runs in the background. Endpoint is
// the function ARN (arn:aws:lambda:<region>:<account>:function:<name>).
func snsDeliverToLambda(functionARN, topicARN, msgID, subject, message string, attributes map[string]SQSMessageAttribute, src iamServiceSource) {
	if !iamAuthorizeServiceDelivery(functionARN, "lambda:InvokeFunction", src) {
		cwEvalLogger.Info().Str("functionARN", functionARN).Str("topicARN", topicARN).Str("sourceService", src.Service).Msg("SNS to Lambda delivery denied by resource policy")
		return
	}
	name := snsTopicNameFromARN(functionARN)
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		cwEvalLogger.Info().Str("functionARN", functionARN).Str("functionName", name).Msg("SNS to Lambda delivery target function not found")
		return
	}
	payload := snsLambdaEventPayload(functionARN, topicARN, msgID, subject, message, attributes)
	go func() { _, _, _ = invokeLambdaViaRuntimeAPI(fn, payload) }()
	cwEvalLogger.Info().Str("functionARN", functionARN).Str("functionName", name).Str("topicARN", topicARN).Str("msgID", msgID).Msg("SNS to Lambda delivery initiated")
}

// snsLambdaEventPayload builds the SNS event a Lambda subscriber
// receives — a Records array with one Sns record carrying the
// Notification message — matching the real SNS→Lambda event shape.
func snsLambdaEventPayload(functionARN, topicARN, msgID, subject, message string, attributes map[string]SQSMessageAttribute) []byte {
	subscriptionARN := functionARN
	for _, sub := range snsSubscriptions.List() {
		if sub.TopicARN == topicARN && sub.Protocol == "lambda" && sub.Endpoint == functionARN {
			subscriptionARN = sub.ARN
			break
		}
	}
	rec := map[string]any{
		"EventSource":          "aws:sns",
		"EventVersion":         "1.0",
		"EventSubscriptionArn": subscriptionARN,
		"Sns": map[string]any{
			"Type":              "Notification",
			"MessageId":         msgID,
			"TopicArn":          topicARN,
			"Subject":           subject,
			"Message":           message,
			"Timestamp":         time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			"MessageAttributes": snsMessageAttributesEnvelope(attributes),
		},
	}
	out, _ := json.Marshal(map[string]any{"Records": []any{rec}})
	return out
}

func handleSNSPublish(w http.ResponseWriter, r *http.Request) {
	// Publish takes one of three targets. Two of them deliver outside AWS, and
	// this simulator cannot reach either — but it says so in those words
	// rather than reporting a missing TopicArn, which is what a caller
	// publishing an SMS used to be told.
	if phone := r.FormValue("PhoneNumber"); phone != "" {
		snsExternalDeliveryUnavailable(w, r, snsSMSDeliveryReason)
		return
	}
	if target := r.FormValue("TargetArn"); target != "" && strings.Contains(target, ":endpoint/") {
		snsExternalDeliveryUnavailable(w, r, snsMobilePushDeliveryReason)
		return
	}
	topicARN := r.FormValue("TopicArn")
	if topicARN == "" {
		topicARN = r.FormValue("TargetArn")
	}
	if topicARN == "" {
		snsErrorXML(w, "InvalidParameter",
			"TopicArn, TargetArn or PhoneNumber is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	name := snsTopicNameFromARN(topicARN)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	entry := snsPublishEntry{
		Message:                r.FormValue("Message"),
		Subject:                r.FormValue("Subject"),
		MessageGroupId:         r.FormValue("MessageGroupId"),
		MessageDeduplicationId: r.FormValue("MessageDeduplicationId"),
		MessageAttributes:      snsParseMessageAttributes(r, "MessageAttributes"),
	}
	if code, msg := snsValidatePublish(t, entry); code != "" {
		snsErrorXML(w, code, msg, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	msgID := generateUUID()
	snsFanout(topicARN, msgID, entry.Subject, entry.Message, entry.MessageAttributes)

	body := fmt.Sprintf("<PublishResult><MessageId>%s</MessageId></PublishResult>", xmlEscape(msgID))
	snsXMLResponse(w, "Publish", body, sim.RequestID(r.Context()))
}

// handleSNSPublishBatch publishes up to 10 messages to a topic in a
// single call, reporting per-entry success/failure. Batch-level errors
// (empty list, >10 entries, duplicate Ids, total payload over 256 KiB)
// are top-level error responses; per-entry validation failures (e.g. a
// FIFO entry missing MessageGroupId) land in the <Failed> list with HTTP
// 200, matching the real SNS wire contract.
func handleSNSPublishBatch(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	topicARN := r.FormValue("TopicArn")
	if topicARN == "" {
		snsErrorXML(w, "InvalidParameter", "TopicArn is required", http.StatusBadRequest, requestID)
		return
	}
	name := snsTopicNameFromARN(topicARN)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, requestID)
		return
	}
	entries := snsPublishBatchEntries(r)
	if len(entries) == 0 {
		snsErrorXML(w, "EmptyBatchRequest",
			"The batch request doesn't contain any entries.", http.StatusBadRequest, requestID)
		return
	}
	if len(entries) > 10 {
		snsErrorXML(w, "TooManyEntriesInBatchRequest",
			"The batch request contains more entries than permissible.", http.StatusBadRequest, requestID)
		return
	}
	seen := map[string]bool{}
	total := 0
	for _, e := range entries {
		if seen[e.Id] {
			snsErrorXML(w, "BatchEntryIdsNotDistinct",
				"Two or more batch entries in the request have the same Id.", http.StatusBadRequest, requestID)
			return
		}
		seen[e.Id] = true
		total += len(e.Message)
	}
	if total > 262144 {
		snsErrorXML(w, "BatchRequestTooLong",
			"The length of all the batch messages put together is more than the limit.", http.StatusBadRequest, requestID)
		return
	}

	var b strings.Builder
	b.WriteString("<PublishBatchResult><Successful>")
	var failed strings.Builder
	for _, e := range entries {
		if code, msg := snsValidatePublish(t, e); code != "" {
			fmt.Fprintf(&failed,
				"<member><Id>%s</Id><Code>%s</Code><Message>%s</Message><SenderFault>true</SenderFault></member>",
				xmlEscape(e.Id), xmlEscape(code), xmlEscape(msg))
			continue
		}
		msgID := generateUUID()
		snsFanout(topicARN, msgID, e.Subject, e.Message, e.MessageAttributes)
		fmt.Fprintf(&b, "<member><Id>%s</Id><MessageId>%s</MessageId></member>",
			xmlEscape(e.Id), xmlEscape(msgID))
	}
	b.WriteString("</Successful><Failed>")
	b.WriteString(failed.String())
	b.WriteString("</Failed></PublishBatchResult>")
	snsXMLResponse(w, "PublishBatch", b.String(), requestID)
}

// snsPublishBatchEntries pulls the PublishBatchRequestEntries.member.N.*
// flattened query parameters into typed entries.
func snsPublishBatchEntries(r *http.Request) []snsPublishEntry {
	var out []snsPublishEntry
	// Parse past the 10-entry cap so an over-limit batch is detectable
	// (the handler rejects len > 10 with TooManyEntriesInBatchRequest).
	for i := 1; ; i++ {
		prefix := fmt.Sprintf("PublishBatchRequestEntries.member.%d.", i)
		id := r.FormValue(prefix + "Id")
		msg := r.FormValue(prefix + "Message")
		// An absent member breaks the contiguous member sequence.
		if id == "" && msg == "" &&
			r.FormValue(prefix+"MessageGroupId") == "" &&
			r.FormValue(prefix+"Subject") == "" {
			break
		}
		out = append(out, snsPublishEntry{
			Id:                     id,
			Message:                msg,
			Subject:                r.FormValue(prefix + "Subject"),
			MessageGroupId:         r.FormValue(prefix + "MessageGroupId"),
			MessageDeduplicationId: r.FormValue(prefix + "MessageDeduplicationId"),
			MessageAttributes:      snsParseMessageAttributes(r, prefix+"MessageAttributes"),
		})
	}
	return out
}

// snsPolicyStatement is the wire shape of one statement in an SNS topic
// resource policy, matching what AWS's AddPermission produces: an allow with
// an AWS-principal list, an SNS:<action> list, and the topic ARN as Resource.
type snsPolicyStatement struct {
	Sid       string         `json:"Sid"`
	Effect    string         `json:"Effect"`
	Principal map[string]any `json:"Principal"`
	Action    []string       `json:"Action"`
	Resource  string         `json:"Resource"`
}

type snsPolicyDoc struct {
	Version   string               `json:"Version"`
	Id        string               `json:"Id,omitempty"`
	Statement []snsPolicyStatement `json:"Statement"`
}

// snsLoadTopicPolicy parses the topic's current Policy attribute into a
// statement list. A topic with no Policy yet starts from an empty default
// document.
func snsLoadTopicPolicy(t SNSTopic) snsPolicyDoc {
	doc := snsPolicyDoc{Version: "2008-10-17", Id: snsTopicNameFromARN(t.ARN) + "/SNSDefaultPolicy"}
	raw := t.Attributes["Policy"]
	if raw == "" {
		return doc
	}
	var parsed snsPolicyDoc
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		// A topic policy the sim itself can't parse is corrupt own-data;
		// surface it loudly rather than silently dropping statements.
		panic(fmt.Sprintf("sns: unparseable topic Policy attribute: %v", err))
	}
	if parsed.Version == "" {
		parsed.Version = doc.Version
	}
	return parsed
}

// snsStoreTopicPolicy serializes the policy doc back onto the topic's Policy
// attribute and mirrors it into the central resource-policy store (the same
// path SetTopicAttributes(Policy) uses) so iamResourcePolicyDocsForARN
// resolves it.
func snsStoreTopicPolicy(name string, t SNSTopic, doc snsPolicyDoc) {
	out, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("sns: marshal topic Policy: %v", err))
	}
	if t.Attributes == nil {
		t.Attributes = map[string]string{}
	}
	t.Attributes["Policy"] = string(out)
	snsTopics.Put(name, t)
	iamPutResourcePolicy(t.ARN, string(out))
}

// snsListMembers pulls a query-flattened <prefix>.member.N list (the wire
// shape of AddPermission's AWSAccountId and ActionName lists) into a slice.
func snsListMembers(r *http.Request, prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", prefix, i))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

// handleSNSAddPermission appends a statement (Sid=Label, Effect=Allow,
// Principal={AWS:[<account-root>...]}, Action=[SNS:<action>...],
// Resource=<topicArn>) to the topic's resource policy. A second AddPermission
// with the same Label is rejected, matching real SNS.
func handleSNSAddPermission(w http.ResponseWriter, r *http.Request) {
	topicARN := r.FormValue("TopicArn")
	label := r.FormValue("Label")
	if topicARN == "" || label == "" {
		snsErrorXML(w, "InvalidParameter",
			"TopicArn and Label are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	name := snsTopicNameFromARN(topicARN)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	accounts := snsListMembers(r, "AWSAccountId")
	actions := snsListMembers(r, "ActionName")
	if len(accounts) == 0 || len(actions) == 0 {
		snsErrorXML(w, "InvalidParameter",
			"AWSAccountId and ActionName are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	doc := snsLoadTopicPolicy(t)
	for _, st := range doc.Statement {
		if st.Sid == label {
			snsErrorXML(w, "InvalidParameter",
				fmt.Sprintf("Statement already exists with Sid %s", label),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	}
	principals := make([]string, 0, len(accounts))
	for _, acct := range accounts {
		principals = append(principals, fmt.Sprintf("arn:aws:iam::%s:root", acct))
	}
	snsActions := make([]string, 0, len(actions))
	for _, a := range actions {
		snsActions = append(snsActions, "SNS:"+a)
	}
	doc.Statement = append(doc.Statement, snsPolicyStatement{
		Sid:       label,
		Effect:    "Allow",
		Principal: map[string]any{"AWS": principals},
		Action:    snsActions,
		Resource:  t.ARN,
	})
	snsStoreTopicPolicy(name, t, doc)
	snsXMLResponse(w, "AddPermission", "", sim.RequestID(r.Context()))
}

// handleSNSRemovePermission removes the statement whose Sid matches Label.
// Real SNS raises NotFound when no statement carries the label.
func handleSNSRemovePermission(w http.ResponseWriter, r *http.Request) {
	topicARN := r.FormValue("TopicArn")
	label := r.FormValue("Label")
	if topicARN == "" || label == "" {
		snsErrorXML(w, "InvalidParameter",
			"TopicArn and Label are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	name := snsTopicNameFromARN(topicARN)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	doc := snsLoadTopicPolicy(t)
	kept := doc.Statement[:0]
	removed := false
	for _, st := range doc.Statement {
		if st.Sid == label {
			removed = true
			continue
		}
		kept = append(kept, st)
	}
	if !removed {
		snsErrorXML(w, "NotFound",
			fmt.Sprintf("No statement was found with Sid %s", label),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	doc.Statement = kept
	snsStoreTopicPolicy(name, t, doc)
	snsXMLResponse(w, "RemovePermission", "", sim.RequestID(r.Context()))
}

func handleSNSTagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceArn")
	name := snsTopicNameFromARN(arn)
	if _, ok := snsTopics.Get(name); !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	snsTopics.Update(name, func(t *SNSTopic) {
		if t.Tags == nil {
			t.Tags = make(map[string]string)
		}
		for i := 1; i <= 50; i++ {
			k := r.FormValue(fmt.Sprintf("Tags.member.%d.Key", i))
			v := r.FormValue(fmt.Sprintf("Tags.member.%d.Value", i))
			if k == "" {
				break
			}
			t.Tags[k] = v
		}
	})
	// Real SNS wraps the (empty) result in a <TagResourceResult/> node; the SDK
	// deserializer requires it to be present.
	snsXMLResponse(w, "TagResource", "<TagResourceResult/>", sim.RequestID(r.Context()))
}

func handleSNSUntagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceArn")
	name := snsTopicNameFromARN(arn)
	if _, ok := snsTopics.Get(name); !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	snsTopics.Update(name, func(t *SNSTopic) {
		for i := 1; i <= 50; i++ {
			k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", i))
			if k == "" {
				break
			}
			delete(t.Tags, k)
		}
	})
	snsXMLResponse(w, "UntagResource", "<UntagResourceResult/>", sim.RequestID(r.Context()))
}

func handleSNSListTagsForResource(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceArn")
	name := snsTopicNameFromARN(arn)
	t, ok := snsTopics.Get(name)
	if !ok {
		snsErrorXML(w, "NotFound", "Topic does not exist", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	var b strings.Builder
	b.WriteString("<ListTagsForResourceResult><Tags>")
	for k, v := range t.Tags {
		fmt.Fprintf(&b, "<member><Key>%s</Key><Value>%s</Value></member>",
			xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</Tags></ListTagsForResourceResult>")
	snsXMLResponse(w, "ListTagsForResource", b.String(), sim.RequestID(r.Context()))
}
