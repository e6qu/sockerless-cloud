package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type sfnAWSQueryService struct {
	Version   string
	EC2Syntax bool
}

var (
	sfnAWSQueryRouter *AWSQueryRouter

	sfnAWSQueryServices = map[string]sfnAWSQueryService{
		"autoscaling":            {Version: "2011-01-01"},
		"ec2":                    {Version: "2016-11-15", EC2Syntax: true},
		"elasticache":            {Version: "2015-02-02"},
		"elasticloadbalancingv2": {Version: "2015-12-01"},
		"elasticloadbalancing":   {Version: "2015-12-01"},
		"iam":                    {Version: "2010-05-08"},
		"rds":                    {Version: "2014-10-31"},
		"sns":                    {Version: "2010-03-31"},
		"sts":                    {Version: "2011-06-15"},
	}
)

func sfnQueryScalar(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func sfnEncodeQueryValue(values url.Values, prefix string, value any, ec2Syntax bool) {
	switch typed := value.(type) {
	case []any:
		for index, member := range typed {
			separator := ".member."
			if ec2Syntax {
				separator = "."
			}
			sfnEncodeQueryValue(values, prefix+separator+strconv.Itoa(index+1), member, ec2Syntax)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if prefix != "" {
				child = prefix + "." + key
			}
			sfnEncodeQueryValue(values, child, typed[key], ec2Syntax)
		}
	case nil:
	default:
		values.Set(prefix, sfnQueryScalar(typed))
	}
}

type sfnXMLNode struct {
	Name     xml.Name
	Text     string
	Children []sfnXMLNode
}

func (node *sfnXMLNode) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	node.Name = start.Name
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			var child sfnXMLNode
			if err := decoder.DecodeElement(&child, &typed); err != nil {
				return err
			}
			node.Children = append(node.Children, child)
		case xml.CharData:
			node.Text += string(typed)
		case xml.EndElement:
			return nil
		}
	}
}

func sfnXMLScalar(text string) any {
	text = strings.TrimSpace(text)
	if parsed, err := strconv.ParseBool(text); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseFloat(text, 64); err == nil {
		return parsed
	}
	return text
}

func sfnXMLValue(node sfnXMLNode) any {
	if len(node.Children) == 0 {
		return sfnXMLScalar(node.Text)
	}
	allMembers := true
	for _, child := range node.Children {
		if child.Name.Local != "member" {
			allMembers = false
			break
		}
	}
	if allMembers {
		members := make([]any, 0, len(node.Children))
		for _, child := range node.Children {
			members = append(members, sfnXMLValue(child))
		}
		return members
	}
	result := map[string]any{}
	for _, child := range node.Children {
		value := sfnXMLValue(child)
		if current, exists := result[child.Name.Local]; exists {
			switch list := current.(type) {
			case []any:
				result[child.Name.Local] = append(list, value)
			default:
				result[child.Name.Local] = []any{current, value}
			}
		} else {
			result[child.Name.Local] = value
		}
	}
	return result
}

func sfnQueryOutput(body io.Reader) (any, error) {
	var root sfnXMLNode
	if err := xml.NewDecoder(body).Decode(&root); err != nil {
		return nil, err
	}
	for _, child := range root.Children {
		if strings.HasSuffix(child.Name.Local, "Result") {
			return sfnXMLValue(child), nil
		}
	}
	return sfnXMLValue(root), nil
}

func sfnQueryError(body io.Reader) (string, string) {
	var root sfnXMLNode
	if xml.NewDecoder(body).Decode(&root) != nil {
		return "", ""
	}
	var visit func(sfnXMLNode)
	var code, message string
	visit = func(node sfnXMLNode) {
		switch node.Name.Local {
		case "Code":
			if code == "" {
				code = strings.TrimSpace(node.Text)
			}
		case "Message":
			if message == "" {
				message = strings.TrimSpace(node.Text)
			}
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root)
	return code, message
}

func sfnInvokeQueryService(service, action string, input any) (any, *sfnExecutionError) {
	config, ok := sfnAWSQueryServices[service]
	if !ok {
		return nil, &sfnExecutionError{
			Name:  "States.TaskFailed",
			Cause: fmt.Sprintf("The service %q is not implemented by an AWS service slice", service),
		}
	}
	if sfnAWSQueryRouter == nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: "AWS Query service router is unavailable"}
	}
	action = strings.ToUpper(action[:1]) + action[1:]
	handler, ok := sfnAWSQueryRouter.Handler(config.Version, action)
	if !ok {
		return nil, &sfnExecutionError{
			Name:  "States.TaskFailed",
			Cause: fmt.Sprintf("The operation %s:%s is not implemented by the AWS service slice", service, action),
		}
	}
	inputValues, err := sfnInputMap(input)
	if err != nil {
		return nil, &sfnExecutionError{Name: "States.Runtime", Cause: err.Error()}
	}
	form := url.Values{"Action": {action}, "Version": {config.Version}}
	keys := make([]string, 0, len(inputValues))
	for key := range inputValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sfnEncodeQueryValue(form, key, inputValues[key], config.EC2Syntax)
	}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code >= http.StatusBadRequest {
		code, message := sfnQueryError(response.Body)
		if code == "" {
			code = http.StatusText(response.Code)
		}
		return nil, &sfnExecutionError{Name: service + "." + code, Cause: message}
	}
	output, err := sfnQueryOutput(response.Body)
	if err != nil {
		return nil, &sfnExecutionError{
			Name:  "States.Runtime",
			Cause: fmt.Sprintf("%s:%s returned invalid XML: %v", service, action, err),
		}
	}
	return output, nil
}
