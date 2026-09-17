package main

import (
	"encoding/json"
	"strconv"
)

// iamSetConditionValues adds key with the distinct non-empty values, in the
// order the request lists them. A key another populator already set keeps its
// values, and a request that supplies no value leaves the key absent.
func iamSetConditionValues(ctx map[string][]string, key string, values ...string) {
	if _, set := ctx[key]; set {
		return
	}
	seen := map[string]bool{}
	var distinct []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		distinct = append(distinct, value)
	}
	if len(distinct) > 0 {
		ctx[key] = distinct
	}
}

// iamSetConditionBool adds key as "true" or "false" when the request states the
// member, and leaves it absent when the request omits it.
func iamSetConditionBool(ctx map[string][]string, key string, value *bool) {
	if value != nil {
		iamSetConditionValues(ctx, key, strconv.FormatBool(*value))
	}
}

// iamSetConditionInt adds key as a decimal number when the request states the
// member.
func iamSetConditionInt(ctx map[string][]string, key string, value *int64) {
	if value != nil {
		iamSetConditionValues(ctx, key, strconv.FormatInt(*value, 10))
	}
}

// iamDecodeJSONRequest decodes a JSON request body into request, and reports
// whether the body held a JSON document.
func iamDecodeJSONRequest(body []byte, request any) bool {
	return len(body) > 0 && json.Unmarshal(body, request) == nil
}
