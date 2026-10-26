package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

type awsProtocol uint8

const (
	awsJSON10 awsProtocol = iota + 1
	awsJSON11
	awsRestJSON1
	awsRestXML
	awsQuery
	awsEC2Query
)

type awsErrorEnvelope struct {
	Protocol awsProtocol
	// Model names the vendored Smithy model whose error shapes fix the member a
	// JSON error carries its message under. Empty takes the model of the request
	// in flight (awsErrorModelScope).
	Model string
	// Namespace is the xmlns of an awsQuery or restXml ErrorResponse.
	Namespace string
}

// An XML envelope carries requestID, or a fresh one when it is empty.
func awsWriteError(w http.ResponseWriter, env awsErrorEnvelope, status int, code, message, requestID string) {
	switch env.Protocol {
	case awsJSON10, awsJSON11, awsRestJSON1:
		awsWriteJSONErrorBody(w, env.Protocol, status, code, awsJSONErrorBody(w, env.Model, code, message))
	case awsQuery, awsRestXML, awsEC2Query:
		awsWriteXMLError(w, env, status, code, message, requestID)
	default:
		panic(fmt.Sprintf("awsWriteError: unknown protocol %d", env.Protocol))
	}
}

// An empty model takes the one recorded for w. A response no modelled service
// produced, such as an in-process recorder or a non-Smithy data plane, carries
// both spellings of the message member.
func awsJSONErrorBody(w http.ResponseWriter, model, code, message string) map[string]string {
	if model == "" {
		model, _ = awsErrorModelOf(w)
	}
	if model == "" {
		return map[string]string{"__type": code, "message": message, "Message": message}
	}
	return awsErrorBody(model, code, message)
}

var awsJSONErrorContentTypes = map[awsProtocol]string{
	awsJSON10:    "application/x-amz-json-1.0",
	awsJSON11:    "application/x-amz-json-1.1",
	awsRestJSON1: "application/json",
}

// The SDKs' restJson1 deserializers read X-Amzn-Errortype before the body.
func awsWriteJSONErrorBody(w http.ResponseWriter, protocol awsProtocol, status int, code string, body any) {
	contentType, ok := awsJSONErrorContentTypes[protocol]
	if !ok {
		panic(fmt.Sprintf("awsWriteJSONErrorBody: protocol %d is not a JSON protocol", protocol))
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func awsWriteXMLError(w http.ResponseWriter, env awsErrorEnvelope, status int, code, message, requestID string) {
	if requestID == "" {
		requestID = sim.NewUUID()
	}
	contentType := "text/xml"
	if env.Protocol == awsRestXML {
		contentType = "application/xml"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if env.Protocol == awsEC2Query {
		_, _ = fmt.Fprintf(w, `<Response><Errors><Error><Code>%s</Code><Message>%s</Message></Error></Errors><RequestID>%s</RequestID></Response>`,
			xmlEscape(code), xmlEscape(message), xmlEscape(requestID))
		return
	}
	if env.Protocol == awsRestXML {
		_, _ = io.WriteString(w, xml.Header)
	}
	xmlns := ""
	if env.Namespace != "" {
		xmlns = ` xmlns="` + xmlEscape(env.Namespace) + `"`
	}
	_, _ = fmt.Fprintf(w, `<ErrorResponse%s><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>%s</RequestId></ErrorResponse>`,
		xmlns, xmlEscape(code), xmlEscape(message), xmlEscape(requestID))
}
