package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// snsParseMessageAttributes reads the AWS Query map encoding used by the
// Amazon SNS Publish and PublishBatch operations. Both Name (the current wire
// spelling) and Key (accepted by older generated clients) are recognized.
func snsParseMessageAttributes(r *http.Request, prefix string) map[string]SQSMessageAttribute {
	attributes := map[string]SQSMessageAttribute{}
	for i := 1; ; i++ {
		entry := fmt.Sprintf("%s.entry.%d.", prefix, i)
		name := r.FormValue(entry + "Name")
		if name == "" {
			name = r.FormValue(entry + "Key")
		}
		dataType := r.FormValue(entry + "Value.DataType")
		stringValue := r.FormValue(entry + "Value.StringValue")
		binaryValue := r.FormValue(entry + "Value.BinaryValue")
		if name == "" && dataType == "" && stringValue == "" && binaryValue == "" {
			break
		}
		attribute := SQSMessageAttribute{DataType: dataType, StringValue: stringValue}
		if binaryValue != "" {
			decoded, err := base64.StdEncoding.DecodeString(binaryValue)
			if err == nil {
				attribute.BinaryValue = decoded
			}
		}
		attributes[name] = attribute
	}
	if len(attributes) == 0 {
		return nil
	}
	return attributes
}

func snsMessageAttributesEnvelope(attributes map[string]SQSMessageAttribute) map[string]any {
	if len(attributes) == 0 {
		return nil
	}
	result := make(map[string]any, len(attributes))
	for name, attribute := range attributes {
		value := attribute.StringValue
		if len(attribute.BinaryValue) > 0 {
			value = base64.StdEncoding.EncodeToString(attribute.BinaryValue)
		}
		result[name] = map[string]string{"Type": attribute.DataType, "Value": value}
	}
	return result
}

func snsValidateFilterPolicy(raw string) error {
	if raw == "" {
		return nil
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return fmt.Errorf("FilterPolicy must be a valid JSON object: %w", err)
	}
	for key, conditions := range policy {
		if key == "" {
			return fmt.Errorf("FilterPolicy keys must not be empty")
		}
		switch conditions.(type) {
		case []any, map[string]any:
		default:
			return fmt.Errorf("FilterPolicy value for %q must be an array or object", key)
		}
	}
	return nil
}

func snsSubscriptionMatches(sub SNSSubscription, message string, attributes map[string]SQSMessageAttribute) bool {
	raw := sub.Attributes["FilterPolicy"]
	if raw == "" || raw == "{}" {
		return true
	}
	var policy map[string]any
	if json.Unmarshal([]byte(raw), &policy) != nil {
		return false
	}
	var values map[string]any
	if strings.EqualFold(sub.Attributes["FilterPolicyScope"], "MessageBody") {
		if json.Unmarshal([]byte(message), &values) != nil {
			return false
		}
	} else {
		values = make(map[string]any, len(attributes))
		for name, attribute := range attributes {
			values[name] = snsFilterAttributeValue(attribute)
		}
	}
	return snsFilterObject(policy, values)
}

func snsFilterAttributeValue(attribute SQSMessageAttribute) any {
	baseType := strings.SplitN(attribute.DataType, ".", 2)[0]
	switch baseType {
	case "Number":
		if number, err := strconv.ParseFloat(attribute.StringValue, 64); err == nil {
			return number
		}
	case "String":
		if strings.HasPrefix(attribute.DataType, "String.Array") {
			var values []any
			if json.Unmarshal([]byte(attribute.StringValue), &values) == nil {
				return values
			}
		}
	case "Binary":
		return base64.StdEncoding.EncodeToString(attribute.BinaryValue)
	}
	return attribute.StringValue
}

func snsFilterObject(policy, values map[string]any) bool {
	for key, rawConditions := range policy {
		candidate, exists := values[key]
		if nested, ok := rawConditions.(map[string]any); ok {
			candidateObject, candidateOK := candidate.(map[string]any)
			if !candidateOK || !snsFilterObject(nested, candidateObject) {
				return false
			}
			continue
		}
		conditions, ok := rawConditions.([]any)
		if !ok || !snsFilterAny(conditions, candidate, exists) {
			return false
		}
	}
	return true
}

func snsFilterAny(conditions []any, candidate any, exists bool) bool {
	candidates, isArray := candidate.([]any)
	if !isArray {
		candidates = []any{candidate}
	}
	for _, condition := range conditions {
		if object, ok := condition.(map[string]any); ok {
			if expected, present := object["exists"].(bool); present && expected == exists {
				return true
			}
		}
		if !exists {
			continue
		}
		for _, value := range candidates {
			if snsFilterCondition(condition, value) {
				return true
			}
		}
	}
	return false
}

func snsFilterCondition(condition, candidate any) bool {
	object, isObject := condition.(map[string]any)
	if !isObject {
		return fmt.Sprint(condition) == fmt.Sprint(candidate)
	}
	for operator, operand := range object {
		switch operator {
		case "anything-but":
			return !snsFilterAnythingBut(operand, candidate)
		case "prefix":
			return strings.HasPrefix(fmt.Sprint(candidate), fmt.Sprint(operand))
		case "suffix":
			return strings.HasSuffix(fmt.Sprint(candidate), fmt.Sprint(operand))
		case "equals-ignore-case":
			return strings.EqualFold(fmt.Sprint(candidate), fmt.Sprint(operand))
		case "wildcard":
			matched, _ := path.Match(fmt.Sprint(operand), fmt.Sprint(candidate))
			return matched
		case "numeric":
			operators, ok := operand.([]any)
			return ok && snsFilterNumeric(operators, candidate)
		case "cidr":
			_, network, err := net.ParseCIDR(fmt.Sprint(operand))
			ip := net.ParseIP(fmt.Sprint(candidate))
			return err == nil && ip != nil && network.Contains(ip)
		case "exists":
			return operand == true
		}
	}
	return false
}

func snsFilterAnythingBut(operand, candidate any) bool {
	if values, ok := operand.([]any); ok {
		for _, value := range values {
			if snsFilterCondition(value, candidate) {
				return true
			}
		}
		return false
	}
	return snsFilterCondition(operand, candidate)
}

func snsFilterNumeric(operators []any, candidate any) bool {
	number, err := strconv.ParseFloat(fmt.Sprint(candidate), 64)
	if err != nil || len(operators)%2 != 0 {
		return false
	}
	for i := 0; i < len(operators); i += 2 {
		limit, err := strconv.ParseFloat(fmt.Sprint(operators[i+1]), 64)
		if err != nil {
			return false
		}
		switch fmt.Sprint(operators[i]) {
		case "=":
			if number != limit {
				return false
			}
		case ">":
			if number <= limit {
				return false
			}
		case ">=":
			if number < limit {
				return false
			}
		case "<":
			if number >= limit {
				return false
			}
		case "<=":
			if number > limit {
				return false
			}
		default:
			return false
		}
	}
	return true
}
