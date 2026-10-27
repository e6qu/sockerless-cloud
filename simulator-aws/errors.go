package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// AWSError writes {"__type": "SomeException", "message": "details"}, with the
// message under the member the serving service's model declares (`message` or
// `Message`), which the SDKs read exactly. A response no modelled service
// produced carries both spellings.
func AWSError(w http.ResponseWriter, code string, message string, statusCode int) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsJSON11}, statusCode, code, message, "")
}

// AWSError has only the writer, and its call sites have no request to pass.
var awsErrorModels sync.Map

// An empty model records nothing.
func awsErrorModelScope(w http.ResponseWriter, model string, next func()) {
	if model == "" {
		next()
		return
	}
	awsErrorModels.Store(w, model)
	defer awsErrorModels.Delete(w)
	next()
}

func awsErrorModelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awsErrorModelScope(w, awsErrorModelForRequest(r), func() { next.ServeHTTP(w, r) })
	})
}

// Also checks writers w wraps.
func awsErrorModelOf(w http.ResponseWriter) (string, bool) {
	for w != nil {
		if stored, ok := awsErrorModels.Load(w); ok {
			model, isName := stored.(string)
			return model, isName
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return "", false
		}
		w = unwrapper.Unwrap()
	}
	return "", false
}

// An awsJson request by its X-Amz-Target service, any other signed request by
// its credential scope's signing name. Empty for an unmodelled service or a
// model declaring no error message.
func awsErrorModelForRequest(r *http.Request) string {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		return awsErrorModelForTarget(target)
	}
	return awsErrorModelDeclaringMessages(awsModelForSigningName(awsRequestSigningName(r), r.URL.Path))
}

func awsErrorModelForTarget(target string) string {
	return awsErrorModelDeclaringMessages(awsModelByServiceShape[awsTargetService(target)])
}

func awsErrorModelDeclaringMessages(model string) string {
	if _, declared := awsErrorMessageMembers[model]; !declared {
		return ""
	}
	return model
}

// The AWS CLI qualifies some targets with a Java namespace
// (com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.CreateTrail), so the
// service is the segment before the operation.
func awsTargetService(target string) string {
	i := strings.LastIndex(target, ".")
	if i < 0 {
		return target
	}
	service := target[:i]
	return service[strings.LastIndex(service, ".")+1:]
}

// From the Authorization header or a presigned URL's X-Amz-Credential.
func awsRequestSigningName(r *http.Request) string {
	credential := ""
	if auth := r.Header.Get("Authorization"); auth != "" {
		if i := strings.Index(auth, "Credential="); i >= 0 {
			credential, _, _ = strings.Cut(auth[i+len("Credential="):], ",")
		}
	} else if r.URL.RawQuery != "" {
		credential = r.URL.Query().Get("X-Amz-Credential")
	}
	scope, ok := parseCredScope(strings.TrimSpace(credential))
	if !ok {
		return ""
	}
	return scope.service
}

// Models sharing a signing name differ by path prefix: Amazon API Gateway V2
// under /v2/, Amazon S3 Control under /v20180820/.
var awsModelPathPrefixes = map[string][]struct{ prefix, model string }{
	"apigateway": {{"/v2/", "apigatewayv2"}, {"/", "api-gateway"}},
	"s3":         {{"/v20180820/", "s3-control"}, {"/", "s3"}},
}

func awsModelForSigningName(signingName, path string) string {
	models := awsModelsBySigningName[signingName]
	if len(models) == 1 {
		return models[0]
	}
	for _, rule := range awsModelPathPrefixes[signingName] {
		if strings.HasPrefix(path, rule.prefix) {
			return rule.model
		}
	}
	return ""
}

func AWSErrorf(w http.ResponseWriter, code string, statusCode int, format string, args ...any) {
	AWSError(w, code, fmt.Sprintf(format, args...), statusCode)
}

// Amazon S3 errors are XML.
type S3ErrorResponse struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
	// InvalidObjectState names the object's class.
	StorageClass string `xml:"StorageClass,omitempty"`
}

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

// The caller has already written the status.
func WriteXMLBody(w http.ResponseWriter, v any) {
	_ = xml.NewEncoder(w).Encode(v)
}

func WriteXML(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(statusCode)
	_ = xml.NewEncoder(w).Encode(v)
}

// Where the model spells all its errors one way, an undeclared error takes
// that spelling; only an undeclared error in a mixed model carries both. Shapes
// with an ErrorCode member (all of Amazon EFS's) carry the code there too.
func awsErrorBody(model, code, message string) map[string]string {
	members, ok := awsErrorMessageMembers[model]
	if !ok {
		panic("awsErrorBody: no vendored Smithy model named " + model)
	}
	body := map[string]string{"__type": code}
	if awsErrorCarriesErrorCode(model, code) {
		body["ErrorCode"] = code
	}
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

func awsErrorCarriesErrorCode(model, code string) bool {
	shapes := awsErrorCodeMembers[model]
	if shapes[code] {
		return true
	}
	if _, declared := awsErrorMessageMembers[model][code]; declared || len(shapes) == 0 {
		return false
	}
	for declaredCode := range awsErrorMessageMembers[model] {
		if !shapes[declaredCode] {
			return false
		}
	}
	return true
}
