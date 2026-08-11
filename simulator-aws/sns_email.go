package main

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

func snsEmailDomain(endpoint string) (string, error) {
	address, err := mail.ParseAddress(endpoint)
	if err != nil {
		return "", err
	}
	at := strings.LastIndexByte(address.Address, '@')
	if at < 1 || at == len(address.Address)-1 {
		return "", fmt.Errorf("email endpoint must contain a destination domain")
	}
	return address.Address[at+1:], nil
}

func snsConfirmationEnvelope(sub SNSSubscription) map[string]any {
	token := snsConfirmationToken(sub)
	messageID := generateUUID()
	subscribeURL := snsControlURL(sub, "ConfirmSubscription", url.Values{
		"TopicArn": {sub.TopicARN},
		"Token":    {token},
	})
	envelope := map[string]any{
		"Type":             "SubscriptionConfirmation",
		"MessageId":        messageID,
		"Token":            token,
		"TopicArn":         sub.TopicARN,
		"Message":          "You have chosen to subscribe to the topic " + sub.TopicARN + ".",
		"SubscribeURL":     subscribeURL,
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"SignatureVersion": "1",
		"SigningCertURL":   snsSigningCertificateURL(sub),
	}
	envelope["Signature"] = snsSignEnvelope(envelope)
	return envelope
}

func snsNotificationEmailEnvelope(sub SNSSubscription, messageID, subject, message string, attributes map[string]SQSMessageAttribute) map[string]any {
	envelope := map[string]any{
		"Type":             "Notification",
		"MessageId":        messageID,
		"TopicArn":         sub.TopicARN,
		"Message":          message,
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"SignatureVersion": "1",
		"SigningCertURL":   snsSigningCertificateURL(sub),
		"UnsubscribeURL": snsControlURL(sub, "Unsubscribe", url.Values{
			"SubscriptionArn": {sub.ARN},
		}),
	}
	if subject != "" {
		envelope["Subject"] = subject
	}
	if messageAttributes := snsMessageAttributesEnvelope(attributes); messageAttributes != nil {
		envelope["MessageAttributes"] = messageAttributes
	}
	envelope["Signature"] = snsSignEnvelope(envelope)
	return envelope
}

func snsDeliverEmailConfirmation(sub SNSSubscription) {
	envelope := snsConfirmationEnvelope(sub)
	var body string
	if strings.EqualFold(sub.Protocol, "email-json") {
		encoded, _ := json.Marshal(envelope)
		body = string(encoded)
	} else {
		body = "You have chosen to subscribe to the Amazon SNS topic " + sub.TopicARN + ".\r\n\r\n" +
			"To confirm this subscription, use the following URL:\r\n" +
			snsEnvelopeString(envelope, "SubscribeURL") + "\r\n\r\n" +
			"Token: " + snsEnvelopeString(envelope, "Token") + "\r\n"
	}
	snsSendEmail(sub, "AWS Notification - Subscription Confirmation", body)
}

func snsDeliverEmailNotification(sub SNSSubscription, messageID, subject, message string, attributes map[string]SQSMessageAttribute) {
	body := message
	if strings.EqualFold(sub.Protocol, "email-json") {
		encoded, _ := json.Marshal(snsNotificationEmailEnvelope(sub, messageID, subject, message, attributes))
		body = string(encoded)
	}
	if subject == "" {
		subject = "AWS Notification Message"
	}
	snsSendEmail(sub, subject, body)
}

func snsSendEmail(sub SNSSubscription, subject, body string) {
	domain, err := snsEmailDomain(sub.Endpoint)
	if err != nil {
		cwEvalLogger.Error().Err(err).Str("endpoint", sub.Endpoint).Msg("Amazon SNS email endpoint is invalid")
		return
	}
	subject = strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	message := []byte("From: AWS Notifications <no-reply@sns.amazonaws.com>\r\n" +
		"To: " + sub.Endpoint + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body + "\r\n")
	if err := awsDeliverSMTP(domain, "no-reply@sns.amazonaws.com", []string{sub.Endpoint}, message); err != nil {
		cwEvalLogger.Error().Err(err).Str("endpoint", sub.Endpoint).Str("subscriptionARN", sub.ARN).
			Msg("Amazon SNS email delivery failed")
	}
}
