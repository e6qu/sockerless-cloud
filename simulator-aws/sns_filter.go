package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
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

// snsFilterPolicyDialect is the grammar a subscription's FilterPolicyScope
// admits: only a MessageBody-scoped policy may nest keys.
func snsFilterPolicyDialect(scope string) awsPatternDialect {
	if scope == "MessageBody" {
		return snsBodyPolicyDialect
	}
	return snsAttributePolicyDialect
}

// snsValidateSubscriptionFilter reports why a subscription's FilterPolicy and
// FilterPolicyScope attributes are not a pair Amazon SNS accepts. An empty
// policy, or "{}", filters nothing.
func snsValidateSubscriptionFilter(attributes map[string]string) error {
	scope := attributes["FilterPolicyScope"]
	if scope != "" && scope != "MessageAttributes" && scope != "MessageBody" {
		return fmt.Errorf("FilterPolicyScope: must be MessageAttributes or MessageBody")
	}
	policy := attributes["FilterPolicy"]
	if policy == "" || policy == "{}" {
		return nil
	}
	if _, err := awsParsePattern(snsFilterPolicyDialect(scope), policy); err != nil {
		return fmt.Errorf("FilterPolicy: %v", err)
	}
	return nil
}

func snsSubscriptionMatches(sub SNSSubscription, message string, attributes map[string]SQSMessageAttribute) bool {
	raw := sub.Attributes["FilterPolicy"]
	if raw == "" || raw == "{}" {
		return true
	}
	scope := sub.Attributes["FilterPolicyScope"]
	policy, err := awsParsePattern(snsFilterPolicyDialect(scope), raw)
	if err != nil {
		return false
	}
	var values map[string]any
	if scope == "MessageBody" {
		if json.Unmarshal([]byte(message), &values) != nil {
			return false
		}
	} else {
		values = make(map[string]any, len(attributes))
		for name, attribute := range attributes {
			if v, ok := snsFilterAttributeValue(attribute); ok {
				values[name] = v
			}
		}
	}
	return awsPatternMatches(policy, values)
}

// snsFilterAttributeValue is the value a filter policy sees for a message
// attribute: a Number as a number, a String.Array as its elements, a String
// as itself. Amazon SNS does not filter on Binary attributes.
func snsFilterAttributeValue(attribute SQSMessageAttribute) (any, bool) {
	switch strings.SplitN(attribute.DataType, ".", 2)[0] {
	case "Number":
		if number, err := strconv.ParseFloat(attribute.StringValue, 64); err == nil {
			return number, true
		}
	case "String":
		if strings.HasPrefix(attribute.DataType, "String.Array") {
			var values []any
			if json.Unmarshal([]byte(attribute.StringValue), &values) == nil {
				return values, true
			}
		}
	case "Binary":
		return nil, false
	}
	return attribute.StringValue, true
}
