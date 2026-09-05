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
//	{"__type": "SomeException", "message": "details"}
func AWSError(w http.ResponseWriter, code string, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": message,
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
