package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

var (
	snsSigningKey      *rsa.PrivateKey
	snsSigningCertPEM  []byte
	snsSigningCertName string
)

// SNSSigningIdentity is the persisted Amazon SNS message-signing key pair.
// Real SNS keeps the same signing certificate available at its published
// SigningCertURL long after a message was delivered, so the identity is
// created once and reused across simulator restarts — otherwise every
// previously delivered message would reference a cert URL that now 404s.
type SNSSigningIdentity struct {
	PrivateKeyPEM  []byte
	CertificatePEM []byte
}

func registerSNSHTTPDelivery(srv *sim.Server) {
	identities := sim.MakeStore[SNSSigningIdentity](srv.DB(), "sns_signing_identity")
	identity, ok := identities.Get("default")
	if !ok {
		identity = generateSNSSigningIdentity()
		identities.Put("default", identity)
	}

	keyBlock, _ := pem.Decode(identity.PrivateKeyPEM)
	if keyBlock == nil {
		panic("persisted Amazon SNS signing key is not PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		panic(fmt.Sprintf("parse persisted Amazon SNS signing key: %v", err))
	}
	certBlock, _ := pem.Decode(identity.CertificatePEM)
	if certBlock == nil {
		panic("persisted Amazon SNS signing certificate is not PEM")
	}
	sum := sha256.Sum256(certBlock.Bytes)
	snsSigningKey = key
	snsSigningCertPEM = identity.CertificatePEM
	snsSigningCertName = hex.EncodeToString(sum[:])

	registerSNSHTTPDeliveries(srv)

	srv.HandleFunc("GET /SimpleNotificationService-"+snsSigningCertName+".pem", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(snsSigningCertPEM)
	})
}

func generateSNSSigningIdentity() SNSSigningIdentity {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("generate Amazon SNS signing key: %v", err))
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "sns." + awsRegion() + ".amazonaws.com"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"sns." + awsRegion() + ".amazonaws.com"},
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(fmt.Sprintf("create Amazon SNS signing certificate: %v", err))
	}
	return SNSSigningIdentity{
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}),
	}
}

func snsRequestOrigin(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + r.Host
}

func snsControlURL(sub SNSSubscription, action string, values url.Values) string {
	values.Set("Action", action)
	values.Set("Version", snsAPIVersion)
	return strings.TrimRight(sub.ControlPlaneOrigin, "/") + "/?" + values.Encode()
}

func snsSigningCertificateURL(sub SNSSubscription) string {
	return strings.TrimRight(sub.ControlPlaneOrigin, "/") +
		"/SimpleNotificationService-" + snsSigningCertName + ".pem"
}

func snsDeliverHTTPConfirmation(sub SNSSubscription) {
	envelope := snsConfirmationEnvelope(sub)
	messageID := snsEnvelopeString(envelope, "MessageId")
	snsPostHTTP(sub, "SubscriptionConfirmation", envelope, messageID, "")
}

func snsDeliverHTTPNotification(sub SNSSubscription, messageID, subject, message string, attributes map[string]SQSMessageAttribute) {
	if strings.EqualFold(sub.Attributes["RawMessageDelivery"], "true") {
		snsPostHTTP(sub, "Notification", nil, messageID, message)
		return
	}
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	unsubscribeURL := snsControlURL(sub, "Unsubscribe", url.Values{
		"SubscriptionArn": {sub.ARN},
	})
	envelope := map[string]any{
		"Type":             "Notification",
		"MessageId":        messageID,
		"TopicArn":         sub.TopicARN,
		"Message":          message,
		"Timestamp":        timestamp,
		"SignatureVersion": "1",
		"SigningCertURL":   snsSigningCertificateURL(sub),
		"UnsubscribeURL":   unsubscribeURL,
	}
	if subject != "" {
		envelope["Subject"] = subject
	}
	if messageAttributes := snsMessageAttributesEnvelope(attributes); messageAttributes != nil {
		envelope["MessageAttributes"] = messageAttributes
	}
	envelope["Signature"] = snsSignEnvelope(envelope)
	snsPostHTTP(sub, "Notification", envelope, messageID, "")
}

func snsEnvelopeString(envelope map[string]any, field string) string {
	value, _ := envelope[field].(string)
	return value
}

func snsSignEnvelope(envelope map[string]any) string {
	var fields []string
	switch snsEnvelopeString(envelope, "Type") {
	case "Notification":
		fields = []string{"Message", "MessageId"}
		if snsEnvelopeString(envelope, "Subject") != "" {
			fields = append(fields, "Subject")
		}
		fields = append(fields, "Timestamp", "TopicArn", "Type")
	default:
		fields = []string{"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"}
	}
	var canonical strings.Builder
	for _, field := range fields {
		canonical.WriteString(field)
		canonical.WriteByte('\n')
		canonical.WriteString(snsEnvelopeString(envelope, field))
		canonical.WriteByte('\n')
	}
	digest := sha1.Sum([]byte(canonical.String()))
	signature, err := rsa.SignPKCS1v15(rand.Reader, snsSigningKey, crypto.SHA1, digest[:])
	if err != nil {
		panic(fmt.Sprintf("sign Amazon SNS message: %v", err))
	}
	return base64.StdEncoding.EncodeToString(signature)
}

// snsHTTPDelivery is one message Amazon SNS owes an HTTP or HTTPS endpoint.
type snsHTTPDelivery struct {
	SubscriptionARN string
	Endpoint        string
	TopicARN        string
	MessageType     string
	MessageID       string
	Raw             bool
	Body            []byte
	Policy          snsRetryPolicy
	RequestID       string
}

var snsHTTPDeliveries *delivery.Dispatcher[snsHTTPDelivery]

func registerSNSHTTPDeliveries(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[snsHTTPDelivery]](srv.DB(), "sns_http_deliveries")
	snsHTTPDeliveries = delivery.New(srv, "Amazon SNS HTTP/S deliveries", store, delivery.Handler[snsHTTPDelivery]{
		Policy: func(d snsHTTPDelivery) delivery.Policy {
			return delivery.Policy{MaxAttempts: d.Policy.NumRetries + 1, Backoff: d.Policy.delay}
		},
		Attempt: snsAttemptHTTP,
		Finish:  snsFinishHTTP,
	})
	snsHTTPDeliveries.Resume()
}

func snsPostHTTP(sub SNSSubscription, messageType string, envelope map[string]any, messageID, raw string) {
	d := snsHTTPDelivery{
		SubscriptionARN: sub.ARN,
		Endpoint:        sub.Endpoint,
		TopicARN:        sub.TopicARN,
		MessageType:     messageType,
		MessageID:       messageID,
		Raw:             envelope == nil,
		Body:            []byte(raw),
		Policy:          snsEffectiveHTTPRetryPolicy(sub),
		RequestID:       sim.NewUUID(),
	}
	if envelope != nil {
		body, err := json.Marshal(envelope)
		if err != nil {
			cwEvalLogger.Error().Err(err).Str("endpoint", sub.Endpoint).Msg("Amazon SNS HTTP envelope does not encode")
			return
		}
		d.Body = body
		d.MessageID = snsEnvelopeString(envelope, "MessageId")
		d.TopicARN = snsEnvelopeString(envelope, "TopicArn")
	}
	snsHTTPDeliveries.Submit(d.RequestID, d)
}

func snsAttemptHTTP(ctx context.Context, item *delivery.Item[snsHTTPDelivery]) delivery.Outcome {
	d := item.Payload
	if _, ok := snsSubscriptions.Get(d.SubscriptionARN); !ok && d.MessageType == "Notification" {
		return delivery.Permanent(fmt.Errorf("subscription %s no longer exists", d.SubscriptionARN))
	}
	header := http.Header{}
	header.Set("Content-Type", "text/plain; charset=UTF-8")
	header.Set("x-amz-sns-message-type", d.MessageType)
	header.Set("x-amz-sns-message-id", d.MessageID)
	header.Set("x-amz-sns-topic-arn", d.TopicARN)
	if d.Raw {
		header.Set("x-amz-sns-subscription-arn", d.SubscriptionARN)
		header.Set("x-amz-sns-rawdelivery", "true")
	}
	return delivery.Post(ctx, delivery.Request{URL: d.Endpoint, Header: header, Body: d.Body, Timeout: 15 * time.Second}, snsHTTPStatusClass)
}

// snsHTTPStatusClass follows the Amazon SNS reading of an endpoint's answer:
// 2xx delivers, 429 and 5xx are server-side errors it retries, and any other
// answer is a client-side error it does not retry.
func snsHTTPStatusClass(status int) delivery.Class {
	switch {
	case status >= 200 && status < 300:
		return delivery.Accept
	case status == http.StatusTooManyRequests || status >= 500:
		return delivery.Retry
	}
	return delivery.Reject
}

// snsFinishHTTP moves a notification Amazon SNS could not deliver to the
// subscription's dead-letter queue, carrying the RequestID, ErrorCode and
// ErrorMessage attributes the service adds.
func snsFinishHTTP(item delivery.Item[snsHTTPDelivery], reason delivery.Reason) {
	d := item.Payload
	if reason == delivery.Succeeded {
		return
	}
	cwEvalLogger.Error().Str("endpoint", d.Endpoint).Str("reason", string(reason)).Str("error", item.LastError).
		Int("attempts", item.Attempts).Msg("Amazon SNS HTTP delivery failed")
	if d.MessageType != "Notification" {
		return
	}
	sub, ok := snsSubscriptions.Get(d.SubscriptionARN)
	if !ok {
		return
	}
	dlq := snsRedriveTarget(sub)
	if dlq == "" {
		return
	}
	errorCode := "EndpointUnavailable"
	if item.LastStatus != 0 {
		errorCode = strconv.Itoa(item.LastStatus)
	}
	snsDeadLetter(sub, dlq, string(d.Body), map[string]SQSMessageAttribute{
		"RequestID":    {DataType: "String", StringValue: d.RequestID},
		"ErrorCode":    {DataType: "String", StringValue: errorCode},
		"ErrorMessage": {DataType: "String", StringValue: item.LastError},
	})
}

// snsRedriveTarget is the dead-letter queue ARN of a subscription's
// RedrivePolicy, empty without one.
func snsRedriveTarget(sub SNSSubscription) string {
	raw := sub.Attributes["RedrivePolicy"]
	if raw == "" {
		return ""
	}
	var policy struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	}
	if json.Unmarshal([]byte(raw), &policy) != nil {
		return ""
	}
	return policy.DeadLetterTargetArn
}

// snsDeadLetter enqueues a message Amazon SNS gave up on to the subscription's
// dead-letter queue, which admits it only when its policy lets the topic send.
func snsDeadLetter(sub SNSSubscription, queueARN, body string, attributes map[string]SQSMessageAttribute) {
	src := iamServiceSource{Service: "sns.amazonaws.com", SourceArn: sub.TopicARN, SourceAccount: snsARNAccount(sub.TopicARN)}
	if !iamAuthorizeServiceDelivery(queueARN, "sqs:SendMessage", src) {
		cwEvalLogger.Info().Str("queueARN", queueARN).Str("subscription", sub.ARN).Msg("Amazon SNS dead-letter queue policy denies the topic")
		return
	}
	queueName := snsTopicNameFromARN(queueARN)
	if _, ok := sqsQueues.Get(queueName); !ok {
		cwEvalLogger.Info().Str("queueARN", queueARN).Str("subscription", sub.ARN).Msg("Amazon SNS dead-letter queue does not exist")
		return
	}
	sqsEnqueueBodyWithAttributes(queueName, body, attributes)
}
