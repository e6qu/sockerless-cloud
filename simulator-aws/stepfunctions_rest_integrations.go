package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

type sfnRESTBinding struct {
	Name       string
	WireName   string
	Location   string
	Target     string
	TargetType string
	XMLName    string
	Flattened  bool
}

type sfnRESTOperation struct {
	Method         string
	URI            string
	InputTarget    string
	Bindings       []sfnRESTBinding
	OutputBindings []sfnRESTBinding
}

type sfnRESTXMLMember struct {
	Name      string
	Target    string
	XMLName   string
	Flattened bool
}

type sfnRESTXMLShape struct {
	Type    string
	XMLName string
	Member  sfnRESTXMLMember
	Key     sfnRESTXMLMember
	Value   sfnRESTXMLMember
	Members []sfnRESTXMLMember
}

var sfnAWSServer *sim.Server

func sfnSignInternalAWSRequest(request *http.Request, service string, payload []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	request.Host = "localhost"
	request.Header.Set("X-Amz-Date", amzDate)
	signedHeaders := []string{"host", "x-amz-date"}
	payloadHash := hexSHA256(payload)
	if service == "s3" {
		// Amazon S3 verifies the declared streaming payload hash without
		// consuming the request body in its authentication middleware.
		request.Header.Set("X-Amz-Content-Sha256", payloadHash)
		signedHeaders = []string{"host", "x-amz-content-sha256", "x-amz-date"}
	}
	credential := credScope{
		accessKeyID: seedAdminAccessKey,
		date:        date,
		region:      awsRegion(),
		service:     service,
	}
	canonical := sigv4CanonicalRequest(
		request,
		signedHeaders,
		sigv4CanonicalQuery(request.URL.Query(), false),
		payloadHash,
		service != "s3",
	)
	signature := sigv4Signature(seedAdminSecretKey, credential, amzDate, canonical)
	request.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s/%s/%s/aws4_request, SignedHeaders=%s, Signature=%s",
		seedAdminAccessKey, date, awsRegion(), service, strings.Join(signedHeaders, ";"), signature,
	))
}

func sfnInputMap(input any) (map[string]any, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func sfnInputValue(values map[string]any, name string) (any, bool) {
	for candidate, value := range values {
		if strings.EqualFold(candidate, name) {
			return value, true
		}
	}
	return nil, false
}

func sfnRESTScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%v", typed)
	default:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
}

func sfnInvokeRESTJSONService(service, action string, input any) (any, *sfnExecutionError) {
	operations, ok := sfnAWSRESTJSONOperations[service]
	if !ok {
		return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "AWS REST-JSON service is not implemented: " + service}
	}
	operation, ok := operations[action]
	if !ok {
		return nil, &sfnExecutionError{
			Name:  "States.TaskFailed",
			Cause: fmt.Sprintf("The operation %s:%s is not implemented by the AWS service slice", service, action),
		}
	}
	values, err := sfnInputMap(input)
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
	}
	requestPath := operation.URI
	query := url.Values{}
	headers := http.Header{}
	document := map[string]any{}
	var payload []byte
	for _, binding := range operation.Bindings {
		value, exists := sfnInputValue(values, binding.Name)
		if !exists {
			continue
		}
		switch binding.Location {
		case "label":
			escaped := url.PathEscape(sfnRESTScalar(value))
			requestPath = strings.ReplaceAll(requestPath, "{"+binding.Name+"}", escaped)
			requestPath = strings.ReplaceAll(requestPath, "{"+binding.Name+"+}", strings.ReplaceAll(escaped, "%2F", "/"))
		case "query":
			switch list := value.(type) {
			case []any:
				for _, member := range list {
					query.Add(binding.WireName, sfnRESTScalar(member))
				}
			default:
				query.Set(binding.WireName, sfnRESTScalar(value))
			}
		case "queryParams":
			if entries, mapOK := value.(map[string]any); mapOK {
				for key, member := range entries {
					query.Set(key, sfnRESTScalar(member))
				}
			}
		case "header":
			headers.Set(binding.WireName, sfnRESTScalar(value))
		case "payload":
			switch binding.TargetType {
			case "blob":
				text, textOK := value.(string)
				if !textOK {
					return nil, &sfnExecutionError{Name: "States.Runtime", Cause: binding.Name + " must be base64 text"}
				}
				payload, err = base64.StdEncoding.DecodeString(text)
			case "string":
				payload = []byte(sfnRESTScalar(value))
			default:
				payload, err = json.Marshal(value)
			}
			if err != nil {
				return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
			}
		default:
			document[binding.WireName] = value
		}
	}
	if len(payload) == 0 && operation.Method != http.MethodGet && operation.Method != http.MethodDelete {
		payload, err = json.Marshal(document)
		if err != nil {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
		}
	}
	if encoded := query.Encode(); encoded != "" {
		separator := "?"
		if strings.Contains(requestPath, "?") {
			separator = "&"
		}
		requestPath += separator + encoded
	}
	request := httptest.NewRequest(operation.Method, requestPath, bytes.NewReader(payload))
	request.Header = headers
	if len(payload) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	sfnSignInternalAWSRequest(request, service, payload)
	response := httptest.NewRecorder()
	sfnAWSServer.ServeHTTP(response, request)
	if response.Code >= http.StatusBadRequest {
		code, message := awsJSONError(response.Body.Bytes())
		if code == "" {
			code = http.StatusText(response.Code)
		}
		return nil, &sfnExecutionError{Name: service + "." + code, Cause: message}
	}
	output, err := sfnRESTJSONOutput(operation, response.Result())
	if err != nil {
		return nil, &sfnExecutionError{
			Name:  "States.Runtime",
			Cause: fmt.Sprintf("%s:%s returned an invalid modeled response: %v", service, action, err),
		}
	}
	return output, nil
}

func sfnRESTJSONOutput(operation sfnRESTOperation, response *http.Response) (any, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	output := map[string]any{}
	hasPayload := false
	for _, binding := range operation.OutputBindings {
		switch binding.Location {
		case "payload":
			hasPayload = true
			switch binding.TargetType {
			case "blob":
				output[binding.Name] = base64.StdEncoding.EncodeToString(body)
			case "string":
				output[binding.Name] = string(body)
			default:
				var value any
				if len(body) > 0 {
					if err := json.Unmarshal(body, &value); err != nil {
						return nil, err
					}
				}
				output[binding.Name] = value
			}
		case "header":
			if value := response.Header.Get(binding.WireName); value != "" {
				output[binding.Name] = value
			}
		}
	}
	if !hasPayload && len(body) > 0 {
		var document any
		if err := json.Unmarshal(body, &document); err != nil {
			return nil, err
		}
		if members, ok := document.(map[string]any); ok {
			for key, value := range members {
				output[key] = value
			}
		} else {
			return document, nil
		}
	}
	return output, nil
}
