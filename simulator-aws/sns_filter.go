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
	matched, _ := snsFilterOutcome(sub, message, attributes)
	return matched
}

// snsFilterOutcome reports whether a subscription's filter policy admits a
// message and, when it does not, the variant of NumberOfNotificationsFilteredOut
// that says why: the message carries no attributes, an attribute or the body
// is not well formed, or the body-scoped policy rejected it.
func snsFilterOutcome(sub SNSSubscription, message string, attributes map[string]SQSMessageAttribute) (bool, string) {
	raw := sub.Attributes["FilterPolicy"]
	if raw == "" || raw == "{}" {
		return true, ""
	}
	scope := sub.Attributes["FilterPolicyScope"]
	policy, err := awsParsePattern(snsFilterPolicyDialect(scope), raw)
	if err != nil {
		// snsValidateSubscriptionFilter refused this policy when it was set.
		panic(fmt.Sprintf("sns: unparseable stored FilterPolicy on %s: %v", sub.ARN, err))
	}
	var values map[string]any
	if scope == "MessageBody" {
		if json.Unmarshal([]byte(message), &values) != nil {
			return false, "NumberOfNotificationsFilteredOut-InvalidMessageBody"
		}
		if !awsPatternMatches(policy, values) {
			return false, "NumberOfNotificationsFilteredOut-MessageBody"
		}
		return true, ""
	}
	if len(attributes) == 0 {
		return false, "NumberOfNotificationsFilteredOut-NoMessageAttributes"
	}
	values = make(map[string]any, len(attributes))
	for name, attribute := range attributes {
		v, ok, valid := snsFilterAttributeValue(attribute)
		if !valid {
			return false, "NumberOfNotificationsFilteredOut-InvalidAttributes"
		}
		if ok {
			values[name] = v
		}
	}
	if !awsPatternMatches(policy, values) {
		return false, "NumberOfNotificationsFilteredOut"
	}
	return true, ""
}

// snsFilterAttributeValue is the value a filter policy sees for a message
// attribute: a Number as a number, a String.Array as its elements, a String
// as itself. Amazon SNS does not filter on Binary attributes.
func snsFilterAttributeValue(attribute SQSMessageAttribute) (value any, filterable, valid bool) {
	switch strings.SplitN(attribute.DataType, ".", 2)[0] {
	case "Number":
		if number, err := strconv.ParseFloat(attribute.StringValue, 64); err == nil {
			return number, true, true
		}
	case "String":
		if strings.HasPrefix(attribute.DataType, "String.Array") {
			var values []any
			if json.Unmarshal([]byte(attribute.StringValue), &values) != nil {
				return nil, false, false
			}
			return values, true, true
		}
	case "Binary":
		return nil, false, true
	}
	return attribute.StringValue, true, true
}
