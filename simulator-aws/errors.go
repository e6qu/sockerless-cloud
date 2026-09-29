package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// AWSError writes an AWS-style JSON error response:
//
//	{"__type": "SomeException", "message": "details"}
//
// The message goes under the member the serving service's model declares for
// the error — AWS's models spell it `message` on some services and `Message` on
// others, and the SDKs' deserializers read exactly the modelled member. The
// service is the one the request in flight on w addressed (see
// awsErrorModelScope). A response no modelled service produced — a handler
// called in-process with its own recorder, or a data plane that is not a Smithy
// API — has no model to consult and carries both spellings.
func AWSError(w http.ResponseWriter, code string, message string, statusCode int) {
	body := map[string]string{"__type": code, "message": message, "Message": message}
	if model, ok := awsErrorModelOf(w); ok {
		body = awsErrorBody(model, code, message)
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

// awsErrorModels holds, for each response writer a request is being served
// on, the vendored model of the service the request addressed. AWSError has
// only the writer, and its thousands of call sites have no request to pass.
var awsErrorModels sync.Map

// awsErrorModelScope records model against w for as long as next serves on it;
// an empty model records nothing.
func awsErrorModelScope(w http.ResponseWriter, model string, next func()) {
	if model == "" {
		next()
		return
	}
	awsErrorModels.Store(w, model)
	defer awsErrorModels.Delete(w)
	next()
}

// awsErrorModelMiddleware scopes every request the mux serves to the model of
// the service it addresses.
func awsErrorModelMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awsErrorModelScope(w, awsErrorModelForRequest(r), func() { next.ServeHTTP(w, r) })
	})
}

// awsErrorModelOf finds the model recorded for w or for a writer w wraps.
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

// awsErrorModelForRequest names the vendored model of the service r addresses,
// the way AWS routes it: an awsJson request by the service its X-Amz-Target
// names, any other signed request by the signing name of its credential scope.
// It is empty for a request that names no modelled service, and for a model
// that declares no error message.
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

// awsTargetService is the service shape an X-Amz-Target names. The AWS CLI
// qualifies some with their Java namespace
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

// awsRequestSigningName is the service of the request's SigV4 credential
// scope, from its Authorization header or a presigned URL's X-Amz-Credential.
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

// awsModelPathPrefixes tells apart the models that share a signing name, by
// the path prefix each one's operations live under: Amazon API Gateway serves
// its V2 API under /v2/, Amazon S3 Control its API under /v20180820/.
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
	// StorageClass is InvalidObjectState's member naming the object's class.
	StorageClass string `xml:"StorageClass,omitempty"`
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

// WriteXMLBody encodes v after the caller has written the status.
func WriteXMLBody(w http.ResponseWriter, v any) {
	_ = xml.NewEncoder(w).Encode(v)
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
// both. An error whose shape has an ErrorCode member (all of Amazon EFS's)
// carries its code there as well, and so does an undeclared error of a model
// whose every error has one.
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
