package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
)

func sfnRESTXMLName(member sfnRESTXMLMember) string {
	if member.XMLName != "" {
		return member.XMLName
	}
	return member.Name
}

func sfnWriteRESTXMLShape(
	builder *strings.Builder,
	service, target, elementName string,
	value any,
	flattened bool,
) error {
	shape, ok := sfnAWSRESTXMLShapes[service][target]
	if !ok {
		if elementName == "" {
			return nil
		}
		builder.WriteString("<" + elementName + ">")
		builder.WriteString(xmlEscape(sfnRESTScalar(value)))
		builder.WriteString("</" + elementName + ">")
		return nil
	}
	switch shape.Type {
	case "structure", "union":
		values, mapOK := value.(map[string]any)
		if !mapOK {
			return fmt.Errorf("%s must be an object", target)
		}
		if elementName != "" {
			builder.WriteString("<" + elementName + ">")
		}
		for _, member := range shape.Members {
			memberValue, exists := sfnInputValue(values, member.Name)
			if !exists {
				continue
			}
			if err := sfnWriteRESTXMLShape(
				builder, service, member.Target, sfnRESTXMLName(member), memberValue, member.Flattened,
			); err != nil {
				return err
			}
		}
		if elementName != "" {
			builder.WriteString("</" + elementName + ">")
		}
	case "list", "set":
		members, listOK := value.([]any)
		if !listOK {
			return fmt.Errorf("%s must be an array", target)
		}
		memberName := sfnRESTXMLName(shape.Member)
		if memberName == "" {
			memberName = "member"
		}
		if !flattened {
			builder.WriteString("<" + elementName + ">")
		}
		for _, member := range members {
			name := memberName
			if flattened {
				name = elementName
			}
			if err := sfnWriteRESTXMLShape(builder, service, shape.Member.Target, name, member, false); err != nil {
				return err
			}
		}
		if !flattened {
			builder.WriteString("</" + elementName + ">")
		}
	case "map":
		entries, mapOK := value.(map[string]any)
		if !mapOK {
			return fmt.Errorf("%s must be an object", target)
		}
		if !flattened {
			builder.WriteString("<" + elementName + ">")
		}
		keys := make([]string, 0, len(entries))
		for key := range entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entryName := "entry"
			if flattened {
				entryName = elementName
			}
			builder.WriteString("<" + entryName + ">")
			if err := sfnWriteRESTXMLShape(
				builder, service, shape.Key.Target, sfnRESTXMLName(shape.Key), key, false,
			); err != nil {
				return err
			}
			if err := sfnWriteRESTXMLShape(
				builder, service, shape.Value.Target, sfnRESTXMLName(shape.Value), entries[key], false,
			); err != nil {
				return err
			}
			builder.WriteString("</" + entryName + ">")
		}
		if !flattened {
			builder.WriteString("</" + elementName + ">")
		}
	default:
		builder.WriteString("<" + elementName + ">")
		builder.WriteString(xmlEscape(sfnRESTScalar(value)))
		builder.WriteString("</" + elementName + ">")
	}
	return nil
}

func sfnRESTXMLOutput(response *httptest.ResponseRecorder, operation sfnRESTOperation) (any, error) {
	output := map[string]any{}
	var payloadBinding *sfnRESTBinding
	for index := range operation.OutputBindings {
		binding := &operation.OutputBindings[index]
		if binding.Location == "payload" {
			payloadBinding = binding
		}
		if binding.Location == "header" {
			if value := response.Header().Get(binding.WireName); value != "" {
				output[binding.Name] = value
			}
		}
	}
	if payloadBinding != nil && payloadBinding.TargetType == "blob" {
		output[payloadBinding.Name] = base64.StdEncoding.EncodeToString(response.Body.Bytes())
		return output, nil
	}
	if response.Body.Len() > 0 {
		bodyOutput, err := sfnQueryOutput(bytes.NewReader(response.Body.Bytes()))
		if err != nil {
			return nil, err
		}
		if values, ok := bodyOutput.(map[string]any); ok {
			for key, value := range values {
				output[key] = value
			}
		} else if payloadBinding != nil {
			output[payloadBinding.Name] = bodyOutput
		}
	}
	return output, nil
}

func sfnInvokeRESTXMLService(service, action string, input any) (any, *sfnExecutionError) {
	operations, ok := sfnAWSRESTXMLOperations[service]
	if !ok {
		return nil, &sfnExecutionError{Name: "States.TaskFailed", Cause: "AWS REST-XML service is not implemented: " + service}
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
			if binding.TargetType == "blob" || binding.TargetType == "string" {
				text, textOK := value.(string)
				if !textOK {
					return nil, &sfnExecutionError{Name: "States.Runtime", Cause: binding.Name + " must be text"}
				}
				if binding.TargetType == "blob" {
					payload, err = base64.StdEncoding.DecodeString(text)
				} else {
					payload = []byte(text)
				}
				if err != nil {
					return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
				}
				continue
			}
			rootName := binding.XMLName
			if rootName == "" {
				rootName = binding.Name
			}
			var builder strings.Builder
			if err := sfnWriteRESTXMLShape(&builder, service, binding.Target, rootName, value, binding.Flattened); err != nil {
				return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
			}
			payload = []byte(builder.String())
		default:
			document[binding.Name] = value
		}
	}
	if len(payload) == 0 && len(document) > 0 {
		rootName := strings.TrimSuffix(operation.InputTarget, "Request")
		if shape := sfnAWSRESTXMLShapes[service][operation.InputTarget]; shape.XMLName != "" {
			rootName = shape.XMLName
		}
		var builder strings.Builder
		if err := sfnWriteRESTXMLShape(
			&builder, service, operation.InputTarget, rootName, document, false,
		); err != nil {
			return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
		}
		payload = []byte(builder.String())
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
		request.Header.Set("Content-Type", "application/xml")
	}
	sfnSignInternalAWSRequest(request, service, payload)
	response := httptest.NewRecorder()
	sfnAWSServer.ServeHTTP(response, request)
	if response.Code >= http.StatusBadRequest {
		code, message := sfnQueryError(response.Body)
		if code == "" {
			code = http.StatusText(response.Code)
		}
		return nil, &sfnExecutionError{Name: service + "." + code, Cause: message}
	}
	output, err := sfnRESTXMLOutput(response, operation)
	if err != nil {
		return nil, &sfnExecutionError{
			Name:  "States.Runtime",
			Cause: fmt.Sprintf("%s:%s returned invalid XML: %v", service, action, err),
		}
	}
	return output, nil
}
