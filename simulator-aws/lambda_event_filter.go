package main

import (
	"encoding/json"
	"fmt"
)

// lambdaESMFilters decodes an event source mapping's FilterCriteria
// ({"Filters": [{"Pattern": "<json>"}, ...]}) into its patterns. No criteria
// means no filtering.
func lambdaESMFilters(criteria map[string]any) ([]map[string]any, error) {
	if len(criteria) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(criteria)
	if err != nil {
		return nil, fmt.Errorf("invalid FilterCriteria: %v", err)
	}
	var fc struct {
		Filters []struct {
			Pattern *string `json:"Pattern"`
		} `json:"Filters"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, fmt.Errorf("invalid FilterCriteria: %v", err)
	}
	patterns := make([]map[string]any, 0, len(fc.Filters))
	for _, f := range fc.Filters {
		if f.Pattern == nil {
			return nil, fmt.Errorf("invalid filter pattern definition: Pattern is required")
		}
		p, err := awsParsePattern(lambdaFilterDialect, *f.Pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid filter pattern definition: %v", err)
		}
		patterns = append(patterns, p)
	}
	return patterns, nil
}

// lambdaRecordPassesFilters reports whether a record matches any of the
// mapping's filters; with none, every record passes. Filters read the record
// with a JSON body decoded, so a pattern can reach into the message; a body
// that is not JSON stays the plain string it is.
func lambdaRecordPassesFilters(filters []map[string]any, record map[string]any) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}
	doc := make(map[string]any, len(record))
	for k, v := range record {
		doc[k] = v
	}
	if body, ok := record["body"].(string); ok {
		var decoded any
		if json.Unmarshal([]byte(body), &decoded) == nil {
			doc["body"] = decoded
		}
	}
	// Round-trip the record so every value is a decoded JSON value, the
	// shape the matcher compares.
	raw, err := json.Marshal(doc)
	if err != nil {
		return false, fmt.Errorf("encode record for filtering: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return false, fmt.Errorf("decode record for filtering: %v", err)
	}
	for _, f := range filters {
		if awsPatternMatches(f, event) {
			return true, nil
		}
	}
	return false, nil
}
