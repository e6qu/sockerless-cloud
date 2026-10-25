package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every AWS protocol's error envelope, as the SDKs and the AWS CLI parse it.
// The bodies are the bytes the per-service writers produced before they
// shared awsWriteError.
func TestAWSWriteErrorEnvelopes(t *testing.T) {
	const message = "the thing was not found"
	cases := []struct {
		name        string
		env         awsErrorEnvelope
		status      int
		code        string
		contentType string
		errorType   string
		body        string
	}{
		{
			name: "awsJson1.0", env: awsErrorEnvelope{Protocol: awsJSON10, Model: "sfn"}, status: 400, code: "StateMachineDoesNotExist",
			contentType: "application/x-amz-json-1.0", errorType: "StateMachineDoesNotExist",
			body: `{"__type":"StateMachineDoesNotExist","message":"the thing was not found"}` + "\n",
		},
		{
			name: "awsJson1.1", env: awsErrorEnvelope{Protocol: awsJSON11, Model: "glue"}, status: 400, code: "EntityNotFoundException",
			contentType: "application/x-amz-json-1.1", errorType: "EntityNotFoundException",
			body: `{"Message":"the thing was not found","__type":"EntityNotFoundException"}` + "\n",
		},
		{
			name: "awsJson1.1 without a model", env: awsErrorEnvelope{Protocol: awsJSON11}, status: 404, code: "ResourceNotFoundException",
			contentType: "application/x-amz-json-1.1", errorType: "ResourceNotFoundException",
			body: `{"Message":"the thing was not found","__type":"ResourceNotFoundException","message":"the thing was not found"}` + "\n",
		},
		{
			name: "restJson1", env: awsErrorEnvelope{Protocol: awsRestJSON1, Model: "efs"}, status: 404, code: "FileSystemNotFound",
			contentType: "application/json", errorType: "FileSystemNotFound",
			body: `{"ErrorCode":"FileSystemNotFound","Message":"the thing was not found","__type":"FileSystemNotFound"}` + "\n",
		},
		{
			name: "awsQuery", env: awsErrorEnvelope{Protocol: awsQuery, Namespace: "http://rds.amazonaws.com/doc/2014-10-31/"}, status: 404, code: "DBInstanceNotFound",
			contentType: "text/xml",
			body:        `<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>DBInstanceNotFound</Code><Message>the thing was not found</Message></Error><RequestId>req-1</RequestId></ErrorResponse>`,
		},
		{
			name: "ec2Query", env: awsErrorEnvelope{Protocol: awsEC2Query}, status: 400, code: "InvalidVpcID.NotFound",
			contentType: "text/xml",
			body:        `<Response><Errors><Error><Code>InvalidVpcID.NotFound</Code><Message>the thing was not found</Message></Error></Errors><RequestID>req-1</RequestID></Response>`,
		},
		{
			name: "restXml", env: awsErrorEnvelope{Protocol: awsRestXML, Namespace: "https://route53.amazonaws.com/doc/2013-04-01/"}, status: 404, code: "NoSuchHostedZone",
			contentType: "application/xml",
			body: `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
				`<ErrorResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Error><Type>Sender</Type><Code>NoSuchHostedZone</Code><Message>the thing was not found</Message></Error><RequestId>req-1</RequestId></ErrorResponse>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			awsWriteError(w, c.env, c.status, c.code, message, "req-1")
			if w.Code != c.status {
				t.Errorf("status = %d, want %d", w.Code, c.status)
			}
			if got := w.Header().Get("Content-Type"); got != c.contentType {
				t.Errorf("Content-Type = %q, want %q", got, c.contentType)
			}
			if got := w.Header().Get("X-Amzn-Errortype"); got != c.errorType {
				t.Errorf("X-Amzn-Errortype = %q, want %q", got, c.errorType)
			}
			if got := w.Body.String(); got != c.body {
				t.Errorf("body =\n%s\nwant\n%s", got, c.body)
			}
		})
	}
}

// A message is text in the XML envelopes, so one carrying markup stays a
// well-formed document that decodes back to the message.
func TestAWSWriteErrorEscapesXMLText(t *testing.T) {
	const message = `bad <input> & 'quoted' "value"`
	for _, protocol := range []awsProtocol{awsQuery, awsRestXML, awsEC2Query} {
		w := httptest.NewRecorder()
		awsWriteError(w, awsErrorEnvelope{Protocol: protocol, Namespace: "urn:test"}, http.StatusBadRequest, "Bad&Code", message, "")
		var decoded struct {
			Message   string `xml:"Error>Message"`
			Code      string `xml:"Error>Code"`
			EC2       string `xml:"Errors>Error>Message"`
			RequestID string `xml:"RequestId"`
			EC2ID     string `xml:"RequestID"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("protocol %d: decode %s: %v", protocol, w.Body.String(), err)
		}
		got, id := decoded.Message, decoded.RequestID
		if protocol == awsEC2Query {
			got, id = decoded.EC2, decoded.EC2ID
		}
		if got != message || id == "" {
			t.Errorf("protocol %d: message %q request ID %q, want %q and a fresh ID", protocol, got, id, message)
		}
	}
}
