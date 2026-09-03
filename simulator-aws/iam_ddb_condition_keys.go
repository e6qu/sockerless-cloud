package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// Amazon DynamoDB's fine-grained access-control condition keys.
//
// These are the keys a policy uses to let a principal reach only its own rows
// and only some of their columns: dynamodb:LeadingKeys restricts which
// partition-key values a request may name, and dynamodb:Attributes which
// attributes it may touch. The rest describe what the request asked the service
// to do — what it wants returned, and whether it arrived inside a batch or a
// transaction.
//
// Every one of them is settled by the request, which is why they belong here:
// the values are read out of what the caller sent, not looked up.

// iamPopulateDynamoDBConditionKeys adds the keys an Amazon DynamoDB request
// settles.
func iamPopulateDynamoDBConditionKeys(r *http.Request, action string, body []byte, ctx map[string][]string) {
	if len(body) == 0 {
		return
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return
	}
	_, operation, _ := strings.Cut(action, ":")

	if capacity, ok := request["ReturnConsumedCapacity"].(string); ok && capacity != "" {
		ctx["dynamodb:ReturnConsumedCapacity"] = []string{capacity}
	}
	if values, ok := request["ReturnValues"].(string); ok && values != "" {
		ctx["dynamodb:ReturnValues"] = []string{values}
	}
	// An operation that carries other operations names itself as the enclosing
	// one for the requests inside it.
	switch operation {
	case "BatchGetItem", "BatchWriteItem", "TransactGetItems", "TransactWriteItems":
		ctx["dynamodb:EnclosingOperation"] = []string{operation}
	}

	table, _ := request["TableName"].(string)
	if leading := ddbRequestLeadingKeys(table, request); len(leading) > 0 {
		ctx["dynamodb:LeadingKeys"] = leading
	}
	if attributes := ddbRequestAttributes(request); len(attributes) > 0 {
		ctx["dynamodb:Attributes"] = attributes
	}
}

// ddbRequestLeadingKeys is the partition-key values the request names. The
// leading key is the table's HASH attribute, so which member of the request
// carries it depends on the table's own schema.
func ddbRequestLeadingKeys(table string, request map[string]any) []string {
	if table == "" {
		return nil
	}
	held, ok := ddbTables.Get(table)
	if !ok {
		return nil
	}
	var partition string
	for _, entry := range held.KeySchema {
		if entry.KeyType == "HASH" {
			partition = entry.AttributeName
			break
		}
	}
	if partition == "" {
		return nil
	}

	seen := map[string]bool{}
	var values []string
	add := func(container any) {
		attributes, ok := container.(map[string]any)
		if !ok {
			return
		}
		value := ddbAttributeScalar(attributes[partition])
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		values = append(values, value)
	}
	add(request["Key"])
	add(request["Item"])
	// A query names the partition value in its key conditions.
	if conditions, ok := request["KeyConditions"].(map[string]any); ok {
		if condition, ok := conditions[partition].(map[string]any); ok {
			if list, ok := condition["AttributeValueList"].([]any); ok && len(list) > 0 {
				add(map[string]any{partition: list[0]})
			}
		}
	}
	sort.Strings(values)
	return values
}

// ddbAttributeScalar reads the string form of an AttributeValue, which is what
// a policy compares a leading key against.
func ddbAttributeScalar(value any) string {
	attribute, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, kind := range []string{"S", "N", "B"} {
		if scalar, ok := attribute[kind].(string); ok {
			return scalar
		}
	}
	return ""
}

// ddbRequestAttributes is the attribute names the request touches, from
// whichever member carries them.
func ddbRequestAttributes(request map[string]any) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	addKeys := func(container any) {
		attributes, ok := container.(map[string]any)
		if !ok {
			return
		}
		for name := range attributes {
			add(name)
		}
	}
	addKeys(request["Item"])
	addKeys(request["Key"])
	addKeys(request["AttributeUpdates"])
	addKeys(request["ExpressionAttributeNames"])
	if list, ok := request["AttributesToGet"].([]any); ok {
		for _, name := range list {
			if attribute, ok := name.(string); ok {
				add(attribute)
			}
		}
	}
	// A projection expression names the attributes a read returns, comma
	// separated. A name behind a placeholder is in ExpressionAttributeNames,
	// which is read above.
	if projection, ok := request["ProjectionExpression"].(string); ok {
		for _, name := range strings.Split(projection, ",") {
			if name = strings.TrimSpace(name); name != "" && !strings.HasPrefix(name, "#") {
				add(name)
			}
		}
	}
	sort.Strings(names)
	return names
}
