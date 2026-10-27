package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSNSNotificationEnvelopeValidJSON verifies snsNotificationEnvelope emits
// parseable JSON even when the embedded alarm message holds control characters,
// which fmt.Sprintf("%q") would render as \x escapes JSON rejects.
func TestSNSNotificationEnvelopeValidJSON(t *testing.T) {
	// Simulate a CloudWatch alarm JSON payload whose AlarmName contains a
	// control character. json.Marshal escapes it as \u0001, so the message
	// string itself is valid JSON.
	message := `{"AlarmName":"bad\u0001char","Description":"line1\nline2"}`
	envelopeStr := snsNotificationEnvelope("arn:aws:sns:us-east-1:123456789012:t", "msg-id", "subj", message, nil)

	var envelope map[string]any
	if err := json.Unmarshal([]byte(envelopeStr), &envelope); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\nenvelope: %s", err, envelopeStr)
	}
	if envelope["Type"] != "Notification" {
		t.Errorf("expected Type=Notification, got %v", envelope["Type"])
	}
	inner, ok := envelope["Message"].(string)
	if !ok {
		t.Fatalf("Message should be a string, got %T", envelope["Message"])
	}
	var alarm map[string]any
	if err := json.Unmarshal([]byte(inner), &alarm); err != nil {
		t.Fatalf("inner Message is not valid JSON: %v\ninner: %s", err, inner)
	}
	if alarm["AlarmName"] != "bad\x01char" {
		t.Errorf("alarm name did not round-trip: %v", alarm["AlarmName"])
	}
	if !strings.Contains(envelopeStr, `"Timestamp"`) {
		t.Error("envelope should include a Timestamp field")
	}
}

func TestSNSNotificationEnvelopeQuotesAndBackslashes(t *testing.T) {
	// A CloudWatch alarm description with quotes, newlines and backslashes
	// round-trips through the SNS-to-SQS envelope as valid JSON, in the envelope and
	// in its Message.
	message := `{"AlarmName":"cpu\"alarm","AlarmDescription":"cpu above 50 \"adversarial\" line1\nline2 \\path","Region":"us-east-1"}`
	subject := `ALARM: "cpu\"alarm" in us-east-1`
	envelopeStr := snsNotificationEnvelope("arn:aws:sns:us-east-1:123456789012:t", "msg-id", subject, message, nil)

	var envelope map[string]any
	if err := json.Unmarshal([]byte(envelopeStr), &envelope); err != nil {
		t.Fatalf("envelope is not valid JSON: %v\nenvelope: %s", err, envelopeStr)
	}
	inner, ok := envelope["Message"].(string)
	if !ok {
		t.Fatalf("Message should be a string, got %T", envelope["Message"])
	}
	var alarm map[string]any
	if err := json.Unmarshal([]byte(inner), &alarm); err != nil {
		t.Fatalf("inner Message is not valid JSON: %v\ninner: %s", err, inner)
	}
	if alarm["AlarmName"] != `cpu"alarm` {
		t.Errorf("alarm name did not round-trip: %v", alarm["AlarmName"])
	}
	wantDesc := "cpu above 50 \"adversarial\" line1\nline2 \\path"
	if alarm["AlarmDescription"] != wantDesc {
		t.Errorf("alarm description did not round-trip: got %q want %q", alarm["AlarmDescription"], wantDesc)
	}
}
