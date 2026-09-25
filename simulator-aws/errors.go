package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
)

// AWSError writes an AWS-style JSON error response.
//
// AWS error format:
//
//	{"__type": "SomeException", "message": "details", "Message": "details"}
//
// The message is written under both spellings because AWS models both and a
// client now reads only the one its own model names. The vendored Smithy
// models spell the member "message" on 760 exception shapes and "Message" on
// 677, and 49 exception names -- ResourceNotFoundException and
// LimitExceededException among them -- are spelled one way by one service and
// the other way by another. Until aws-sdk-go-v2 1.47 the generated
// deserializers matched the key case-insensitively and either spelling
// served; the schema-driven deserializers that replaced them match the
// modelled member exactly, so a single spelling here leaves half of AWS
// reading an empty message.
//
// Writing one spelling per exception is what the real services do, and it
// needs the service: the same exception name is spelled differently by
// different services, and this writer has no service to consult at its 2,326
// call sites. BUGS.md carries that as the repair -- a table generated from
// the vendored models, keyed by service and exception, consulted where the
// response is written.
func AWSError(w http.ResponseWriter, code string, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": message,
		"Message": message,
	})
}

// AWSErrorf writes an AWS-style error with a formatted message.
func AWSErrorf(w http.ResponseWriter, code string, statusCode int, format string, args ...any) {
	AWSError(w, code, fmt.Sprintf(format, args...), statusCode)
}

// S3Error writes an S3-style XML error response.
//
// S3 uses XML for error responses, unlike other AWS services.
type S3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
}

// S3ErrorXML writes an S3-style XML error response.
func S3ErrorXML(w http.ResponseWriter, code string, message string, resource string, requestID string, statusCode int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	_ = xml.NewEncoder(w).Encode(S3ErrorResponse{
		Code:      code,
		Message:   message,
		Resource:  resource,
		RequestID: requestID,
	})
}

// WriteXML writes an XML response with the given status code.
func WriteXML(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	_ = xml.NewEncoder(w).Encode(v)
}

// awsErrorBody is an awsJson error document for a service whose Smithy model is
// the one named: the message goes under the member that model declares for the
// error, which is what the SDKs' deserializers read. Where the model agrees on
// one spelling for all its errors, an error it does not declare takes that
// spelling too; only an undeclared error in a model that mixes the two carries
// both.
func awsErrorBody(model, code, message string) map[string]string {
	members, ok := awsErrorMessageMembers[model]
	if !ok {
		panic("awsErrorBody: no vendored Smithy model named " + model)
	}
	body := map[string]string{"__type": code}
	if member, declared := members[code]; declared {
		body[member] = message
		return body
	}
	spellings := map[string]bool{}
	for _, member := range members {
		spellings[member] = true
	}
	for member := range spellings {
		body[member] = message
	}
	return body
}
