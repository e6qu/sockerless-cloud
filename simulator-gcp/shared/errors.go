package simulator

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

// GCPError writes a GCP-style JSON error response.
//
// GCP error format:
//
//	{"error": {"code": 404, "message": "details", "status": "NOT_FOUND", "details": []}}
func GCPError(w http.ResponseWriter, code int, message string, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
			"details": []any{},
		},
	})
}

// GCPErrorf writes a GCP-style error with a formatted message.
func GCPErrorf(w http.ResponseWriter, code int, status string, format string, args ...any) {
	GCPError(w, code, fmt.Sprintf(format, args...), status)
}

// AzureError writes an Azure ARM-style JSON error response.
//
// Azure error format:
//
//	{"error": {"code": "ResourceNotFound", "message": "details"}}
func AzureError(w http.ResponseWriter, code string, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// AzureErrorf writes an Azure-style error with a formatted message.
func AzureErrorf(w http.ResponseWriter, code string, statusCode int, format string, args ...any) {
	AzureError(w, code, fmt.Sprintf(format, args...), statusCode)
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

// EC2ErrorXML writes an AWS Query Protocol XML error response.
// Used by EC2, IAM, and STS services.
//
// Format:
//
//	<Response><Errors><Error><Code>...</Code><Message>...</Message></Error></Errors><RequestId>...</RequestId></Response>
func EC2ErrorXML(w http.ResponseWriter, code string, message string, requestID string, statusCode int) {
	type item struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	type errList struct {
		Error item `xml:"Error"`
	}
	type resp struct {
		XMLName   xml.Name `xml:"Response"`
		Errors    errList  `xml:"Errors"`
		RequestID string   `xml:"RequestId"`
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(statusCode)
	_ = xml.NewEncoder(w).Encode(resp{
		Errors:    errList{Error: item{Code: code, Message: message}},
		RequestID: requestID,
	})
}

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(v)
}

// WriteXML writes an XML response with the given status code.
func WriteXML(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	_ = xml.NewEncoder(w).Encode(v)
}
