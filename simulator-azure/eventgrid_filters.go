package main

import (
	"strings"
)

// Event Grid advanced filters ("Understand event filtering"): a filter reads
// the value at its key — an event property such as Subject, or a data field
// such as data.key1 — and compares it with its operator, ignoring values of
// another type. With advanced filtering on arrays, each element of an array is
// compared: a positive operator matches when any element matches a value, a
// negated one fails when any does. Strings compare case-insensitively. A
// subscription's filters are ANDed; one filter's values are ORed.

// Every other operator refuses an event lacking the key.
var eventGridMatchWhenAbsent = map[string]bool{
	"NumberNotIn":       true,
	"StringNotIn":       true,
	"IsNullOrUndefined": true,
}

func eventGridAdvancedFilterMatches(spec map[string]any, event map[string]any, onArrays bool) bool {
	operator, _ := spec["operatorType"].(string)
	key, _ := spec["key"].(string)
	value, present := eventGridFilterKey(event, key)
	switch operator {
	case "IsNullOrUndefined":
		return !present || value == nil
	case "IsNotNull":
		return present && value != nil
	}
	if !present || value == nil {
		return eventGridMatchWhenAbsent[operator]
	}
	keys := []any{value}
	if elements, ok := value.([]any); ok {
		if !onArrays {
			keys = nil
		} else {
			keys = elements
		}
	}
	strs, nums, bools := eventGridFilterKeyValues(keys)
	switch operator {
	case "NumberIn":
		return eventGridAnyNumber(nums, spec["values"], func(k, f float64) bool { return k == f })
	case "NumberNotIn":
		return !eventGridAnyNumber(nums, spec["values"], func(k, f float64) bool { return k == f })
	case "NumberLessThan":
		return eventGridAnyNumber(nums, []any{spec["value"]}, func(k, f float64) bool { return k < f })
	case "NumberGreaterThan":
		return eventGridAnyNumber(nums, []any{spec["value"]}, func(k, f float64) bool { return k > f })
	case "NumberLessThanOrEquals":
		return eventGridAnyNumber(nums, []any{spec["value"]}, func(k, f float64) bool { return k <= f })
	case "NumberGreaterThanOrEquals":
		return eventGridAnyNumber(nums, []any{spec["value"]}, func(k, f float64) bool { return k >= f })
	case "NumberInRange":
		return eventGridAnyInRange(nums, spec["values"])
	case "NumberNotInRange":
		return !eventGridAnyInRange(nums, spec["values"])
	case "BoolEquals":
		want, ok := spec["value"].(bool)
		if !ok {
			return false
		}
		for _, b := range bools {
			if b == want {
				return true
			}
		}
		return false
	case "StringIn":
		return eventGridAnyString(strs, spec["values"], func(k, f string) bool { return k == f })
	case "StringNotIn":
		return !eventGridAnyString(strs, spec["values"], func(k, f string) bool { return k == f })
	case "StringBeginsWith":
		return eventGridAnyString(strs, spec["values"], strings.HasPrefix)
	case "StringNotBeginsWith":
		return !eventGridAnyString(strs, spec["values"], strings.HasPrefix)
	case "StringEndsWith":
		return eventGridAnyString(strs, spec["values"], strings.HasSuffix)
	case "StringNotEndsWith":
		return !eventGridAnyString(strs, spec["values"], strings.HasSuffix)
	case "StringContains":
		return eventGridAnyString(strs, spec["values"], strings.Contains)
	case "StringNotContains":
		return !eventGridAnyString(strs, spec["values"], strings.Contains)
	}
	return false
}

// Each dot-separated segment selects a member case-insensitively, so Subject
// reads an Event Grid event's subject.
func eventGridFilterKey(event map[string]any, key string) (any, bool) {
	var current any = event
	for _, segment := range strings.Split(key, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, found := object[segment]
		if !found {
			for name, v := range object {
				if strings.EqualFold(name, segment) {
					value, found = v, true
					break
				}
			}
		}
		if !found {
			return nil, false
		}
		current = value
	}
	return current, true
}

func eventGridFilterKeyValues(keys []any) (strs []string, nums []float64, bools []bool) {
	for _, k := range keys {
		switch v := k.(type) {
		case string:
			strs = append(strs, strings.ToLower(v))
		case float64:
			nums = append(nums, v)
		case bool:
			bools = append(bools, v)
		}
	}
	return strs, nums, bools
}

func eventGridAnyNumber(keys []float64, values any, match func(key, filter float64) bool) bool {
	filters, _ := values.([]any)
	for _, f := range filters {
		n, ok := f.(float64)
		if !ok {
			continue
		}
		for _, k := range keys {
			if match(k, n) {
				return true
			}
		}
	}
	return false
}

func eventGridAnyInRange(keys []float64, values any) bool {
	ranges, _ := values.([]any)
	for _, r := range ranges {
		bounds, _ := r.([]any)
		if len(bounds) != 2 {
			continue
		}
		low, lok := bounds[0].(float64)
		high, hok := bounds[1].(float64)
		if !lok || !hok {
			continue
		}
		for _, k := range keys {
			if k >= low && k <= high {
				return true
			}
		}
	}
	return false
}

func eventGridAnyString(keys []string, values any, match func(key, filter string) bool) bool {
	filters, _ := values.([]any)
	for _, f := range filters {
		s, ok := f.(string)
		if !ok {
			continue
		}
		s = strings.ToLower(s)
		for _, k := range keys {
			if match(k, s) {
				return true
			}
		}
	}
	return false
}
