package aws_sdk_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type snsHTTPDelivery struct {
	headers http.Header
	body    []byte
}

func receiveSNSHTTP(t *testing.T, deliveries <-chan snsHTTPDelivery) snsHTTPDelivery {
	t.Helper()
	select {
	case delivery := <-deliveries:
		return delivery
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Amazon SNS HTTP delivery")
		return snsHTTPDelivery{}
	}
}

func snsHTTPString(envelope map[string]any, field string) string {
	value, _ := envelope[field].(string)
	return value
}

func verifySNSHTTPEnvelope(t *testing.T, envelope map[string]any) {
	t.Helper()
	certResponse, err := http.Get(snsHTTPString(envelope, "SigningCertURL"))
	require.NoError(t, err)
	defer certResponse.Body.Close()
	require.Equal(t, http.StatusOK, certResponse.StatusCode)
	certPEM, err := io.ReadAll(certResponse.Body)
	require.NoError(t, err)
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	publicKey, ok := cert.PublicKey.(*rsa.PublicKey)
	require.True(t, ok)

	fields := []string{"Message", "MessageId"}
	if snsHTTPString(envelope, "Type") == "Notification" && snsHTTPString(envelope, "Subject") != "" {
		fields = append(fields, "Subject")
	}
	if snsHTTPString(envelope, "Type") == "Notification" {
		fields = append(fields, "Timestamp", "TopicArn", "Type")
	} else {
		fields = append(fields, "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type")
	}
	var canonical strings.Builder
	for _, field := range fields {
		canonical.WriteString(field)
		canonical.WriteByte('\n')
		canonical.WriteString(snsHTTPString(envelope, field))
		canonical.WriteByte('\n')
	}
	digest := sha1.Sum([]byte(canonical.String()))
	signature, err := base64.StdEncoding.DecodeString(snsHTTPString(envelope, "Signature"))
	require.NoError(t, err)
	require.NoError(t, rsa.VerifyPKCS1v15(publicKey, crypto.SHA1, digest[:], signature))
}

// TestSNS_HTTPSubscriptionDelivery_SDK exercises the complete public Amazon
// SNS HTTP flow through an official SDK client and a real HTTP listener:
// signed confirmation, ConfirmSubscription, signed notification, and raw
// message delivery.
func TestSNS_HTTPSubscriptionDelivery_SDK(t *testing.T) {
	deliveries := make(chan snsHTTPDelivery, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		deliveries <- snsHTTPDelivery{headers: r.Header.Clone(), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()

	client := snsClient()
	topic, err := client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("http-delivery-topic")})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() {
		_, _ = client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	subscribe, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn:              aws.String(topicARN),
		Protocol:              aws.String("http"),
		Endpoint:              aws.String(endpoint.URL),
		ReturnSubscriptionArn: true,
	})
	require.NoError(t, err)
	subscriptionARN := aws.ToString(subscribe.SubscriptionArn)
	require.NotEmpty(t, subscriptionARN)

	confirmation := receiveSNSHTTP(t, deliveries)
	assert.Equal(t, "SubscriptionConfirmation", confirmation.headers.Get("x-amz-sns-message-type"))
	var confirmationEnvelope map[string]any
	require.NoError(t, json.Unmarshal(confirmation.body, &confirmationEnvelope))
	assert.Equal(t, topicARN, snsHTTPString(confirmationEnvelope, "TopicArn"))
	assert.Equal(t, "1", snsHTTPString(confirmationEnvelope, "SignatureVersion"))
	assert.Contains(t, snsHTTPString(confirmationEnvelope, "SubscribeURL"), "Action=ConfirmSubscription")
	verifySNSHTTPEnvelope(t, confirmationEnvelope)

	_, err = client.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN),
		Token:    aws.String(snsHTTPString(confirmationEnvelope, "Token")),
	})
	require.NoError(t, err)

	_, err = client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(topicARN),
		Subject:  aws.String("delivery subject"),
		Message:  aws.String("delivery body"),
		MessageAttributes: map[string]snstypes.MessageAttributeValue{
			"event": {DataType: aws.String("String"), StringValue: aws.String("delivery")},
		},
	})
	require.NoError(t, err)
	notification := receiveSNSHTTP(t, deliveries)
	assert.Equal(t, "Notification", notification.headers.Get("x-amz-sns-message-type"))
	var notificationEnvelope map[string]any
	require.NoError(t, json.Unmarshal(notification.body, &notificationEnvelope))
	assert.Equal(t, "delivery body", snsHTTPString(notificationEnvelope, "Message"))
	assert.Equal(t, "delivery subject", snsHTTPString(notificationEnvelope, "Subject"))
	assert.Contains(t, snsHTTPString(notificationEnvelope, "UnsubscribeURL"), "Action=Unsubscribe")
	attributes, ok := notificationEnvelope["MessageAttributes"].(map[string]any)
	require.True(t, ok)
	eventAttribute, ok := attributes["event"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "String", snsHTTPString(eventAttribute, "Type"))
	assert.Equal(t, "delivery", snsHTTPString(eventAttribute, "Value"))
	verifySNSHTTPEnvelope(t, notificationEnvelope)

	_, err = client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subscriptionARN),
		AttributeName:   aws.String("RawMessageDelivery"),
		AttributeValue:  aws.String("true"),
	})
	require.NoError(t, err)
	_, err = client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(topicARN),
		Message:  aws.String("raw delivery body"),
	})
	require.NoError(t, err)
	raw := receiveSNSHTTP(t, deliveries)
	assert.Equal(t, "true", raw.headers.Get("x-amz-sns-rawdelivery"))
	assert.Equal(t, "raw delivery body", string(raw.body))
}
