package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The content-filter grammar Amazon EventBridge event patterns, Amazon SNS
// subscription filter policies and AWS Lambda event filtering share:
//
//	pattern  = { key: (pattern | [ leaf, ... ]) , "$or": [ pattern, ... ] }
//	leaf     = string | number | true | false | null
//	         | {"prefix": s} | {"suffix": s} | {"equals-ignore-case": s}
//	         | {"wildcard": s} | {"anything-but": ...} | {"numeric": [op, n, ...]}
//	         | {"exists": bool} | {"cidr": s}
//
// Sibling keys AND, a leaf list ORs, and an event array matches when any
// element does. A dialect records where the consumers differ.
type awsPatternDialect struct {
	// nested admits an object as a key's value; Amazon SNS allows it only for a
	// MessageBody-scoped policy.
	nested bool
	// foldedAffix admits {"prefix": {"equals-ignore-case": s}} and its
	// suffix twin, which only Amazon EventBridge documents.
	foldedAffix         bool
	anythingButMatchers map[string]bool
}

var (
	// https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-create-pattern-operators.html
	ebPatternDialect = awsPatternDialect{
		nested:              true,
		foldedAffix:         true,
		anythingButMatchers: map[string]bool{"prefix": true, "suffix": true, "equals-ignore-case": true, "wildcard": true},
	}
	// https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html
	lambdaFilterDialect = ebPatternDialect
	// https://docs.aws.amazon.com/sns/latest/dg/sns-subscription-filter-policies.html
	snsBodyPolicyDialect = awsPatternDialect{
		nested:              true,
		anythingButMatchers: map[string]bool{"prefix": true, "suffix": true},
	}
	snsAttributePolicyDialect = awsPatternDialect{
		anythingButMatchers: map[string]bool{"prefix": true, "suffix": true},
	}
)

func awsParsePattern(d awsPatternDialect, raw string) (map[string]any, error) {
	var pattern map[string]any
	if err := json.Unmarshal([]byte(raw), &pattern); err != nil {
		return nil, fmt.Errorf("pattern is not a valid JSON object: %v", err)
	}
	if err := d.validateObject(pattern); err != nil {
		return nil, err
	}
	return pattern, nil
}

func (d awsPatternDialect) validateObject(pattern map[string]any) error {
	if len(pattern) == 0 {
		return fmt.Errorf("pattern must be a non-empty object")
	}
	for key, val := range pattern {
		if key == "$or" {
			alts, ok := val.([]any)
			if !ok || len(alts) < 2 {
				return fmt.Errorf(`"$or" must be an array of at least two objects`)
			}
			for _, alt := range alts {
				obj, ok := alt.(map[string]any)
				if !ok {
					return fmt.Errorf(`"$or" must be an array of objects`)
				}
				if err := d.validateObject(obj); err != nil {
					return err
				}
			}
			continue
		}
		switch v := val.(type) {
		case map[string]any:
			if !d.nested {
				return fmt.Errorf("%q: nested keys are not supported in this scope", key)
			}
			if err := d.validateObject(v); err != nil {
				return err
			}
		case []any:
			if len(v) == 0 {
				return fmt.Errorf("%q must be a non-empty array", key)
			}
			for _, leaf := range v {
				if err := d.validateLeaf(key, leaf); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%q must be an object or an array", key)
		}
	}
	return nil
}

func (d awsPatternDialect) validateLeaf(key string, leaf any) error {
	switch l := leaf.(type) {
	case string, float64, bool, nil:
		return nil
	case map[string]any:
		if len(l) != 1 {
			return fmt.Errorf("%q: a matcher object must hold exactly one operator", key)
		}
		op, operand := awsSoleEntry(l)
		return d.validateMatcher(key, op, operand)
	}
	return fmt.Errorf("%q: unsupported value %v", key, leaf)
}

func (d awsPatternDialect) validateMatcher(key, op string, operand any) error {
	switch op {
	case "exists":
		if _, ok := operand.(bool); !ok {
			return fmt.Errorf("%q: exists takes true or false", key)
		}
	case "prefix", "suffix":
		if _, ok := operand.(string); ok {
			return nil
		}
		if folded, ok := operand.(map[string]any); ok && d.foldedAffix && len(folded) == 1 {
			if s, ok := folded["equals-ignore-case"].(string); ok && s != "" {
				return nil
			}
		}
		return fmt.Errorf("%q: %s takes a string", key, op)
	case "equals-ignore-case":
		if _, ok := operand.(string); !ok {
			return fmt.Errorf("%q: equals-ignore-case takes a string", key)
		}
	case "wildcard":
		s, ok := operand.(string)
		if !ok {
			return fmt.Errorf("%q: wildcard takes a string", key)
		}
		if strings.Contains(s, "**") {
			return fmt.Errorf("%q: wildcard must not hold consecutive '*'", key)
		}
	case "cidr":
		s, ok := operand.(string)
		if !ok {
			return fmt.Errorf("%q: cidr takes a string", key)
		}
		if _, _, err := net.ParseCIDR(s); err != nil {
			return fmt.Errorf("%q: %v", key, err)
		}
	case "numeric":
		return validateNumeric(key, operand)
	case "anything-but":
		return d.validateAnythingBut(key, operand)
	default:
		return fmt.Errorf("%q: unrecognized match type %s", key, op)
	}
	return nil
}

func validateNumeric(key string, operand any) error {
	terms, ok := operand.([]any)
	if !ok || len(terms) == 0 || len(terms) > 4 || len(terms)%2 != 0 {
		return fmt.Errorf("%q: numeric takes one or two operator and number pairs", key)
	}
	for i := 0; i < len(terms); i += 2 {
		op, ok := terms[i].(string)
		if !ok || !awsNumericOps[op] {
			return fmt.Errorf("%q: unrecognized numeric operator %v", key, terms[i])
		}
		if _, ok := terms[i+1].(float64); !ok {
			return fmt.Errorf("%q: numeric %s takes a number", key, op)
		}
		if len(terms) == 4 && (op == "=" || i == 0 && op[0] != '>' || i == 2 && op[0] != '<') {
			return fmt.Errorf("%q: a numeric range is a lower bound followed by an upper bound", key)
		}
	}
	return nil
}

var awsNumericOps = map[string]bool{"=": true, "<": true, "<=": true, ">": true, ">=": true}

func (d awsPatternDialect) validateAnythingBut(key string, operand any) error {
	switch v := operand.(type) {
	case string, float64:
		return nil
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("%q: anything-but takes a non-empty list", key)
		}
		for _, e := range v {
			switch e.(type) {
			case string, float64:
			default:
				return fmt.Errorf("%q: anything-but lists hold strings or numbers", key)
			}
		}
		return nil
	case map[string]any:
		if len(v) != 1 {
			return fmt.Errorf("%q: anything-but takes one matcher", key)
		}
		op, inner := awsSoleEntry(v)
		if !d.anythingButMatchers[op] {
			return fmt.Errorf("%q: anything-but does not support %s", key, op)
		}
		if list, ok := inner.([]any); ok && len(list) > 0 {
			for _, e := range list {
				if err := d.validateMatcher(key, op, e); err != nil {
					return err
				}
			}
			return nil
		}
		return d.validateMatcher(key, op, inner)
	}
	return fmt.Errorf("%q: anything-but takes a value, a list or a matcher", key)
}

func awsPatternMatches(pattern map[string]any, event any) bool {
	obj, ok := event.(map[string]any)
	if !ok {
		return false
	}
	for key, val := range pattern {
		if key == "$or" {
			alts, _ := val.([]any)
			matched := false
			for _, alt := range alts {
				if sub, ok := alt.(map[string]any); ok && awsPatternMatches(sub, obj) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
			continue
		}
		ev, present := obj[key]
		switch pv := val.(type) {
		case map[string]any:
			if !awsPatternMatchesNested(pv, ev) {
				return false
			}
		case []any:
			if !awsLeavesMatch(pv, ev, present) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func awsPatternMatchesNested(pattern map[string]any, ev any) bool {
	if list, ok := ev.([]any); ok {
		for _, e := range list {
			if awsPatternMatches(pattern, e) {
				return true
			}
		}
		return false
	}
	return awsPatternMatches(pattern, ev)
}

func awsLeavesMatch(leaves []any, ev any, present bool) bool {
	values, isList := ev.([]any)
	if !isList {
		values = []any{ev}
	}
	for _, leaf := range leaves {
		if m, ok := leaf.(map[string]any); ok {
			if want, ok := m["exists"].(bool); ok {
				// exists tests leaf values: a key holding an object does not exist for it.
				_, isObject := ev.(map[string]any)
				if (present && !isObject) == want {
					return true
				}
				continue
			}
		}
		if !present {
			continue
		}
		for _, v := range values {
			if awsLeafMatches(leaf, v) {
				return true
			}
		}
	}
	return false
}

func awsLeafMatches(leaf, v any) bool {
	m, ok := leaf.(map[string]any)
	if !ok {
		return awsValuesEqual(leaf, v)
	}
	op, operand := awsSoleEntry(m)
	return awsMatcherMatches(op, operand, v)
}

// Validation holds a matcher object to exactly one entry.
func awsSoleEntry(m map[string]any) (key string, val any) {
	for k, v := range m {
		key, val = k, v
	}
	return key, val
}

func awsMatcherMatches(op string, operand, v any) bool {
	s, isString := v.(string)
	switch op {
	case "prefix", "suffix":
		affix, fold := awsAffixOperand(operand)
		if !isString {
			return false
		}
		if fold {
			s, affix = strings.ToLower(s), strings.ToLower(affix)
		}
		if op == "prefix" {
			return strings.HasPrefix(s, affix)
		}
		return strings.HasSuffix(s, affix)
	case "equals-ignore-case":
		want, _ := operand.(string)
		return isString && strings.EqualFold(s, want)
	case "wildcard":
		p, _ := operand.(string)
		return isString && awsWildcard.Match(p, s)
	case "cidr":
		c, _ := operand.(string)
		_, network, err := net.ParseCIDR(c)
		ip := net.ParseIP(s)
		return isString && err == nil && ip != nil && network.Contains(ip)
	case "numeric":
		return awsNumericMatches(operand, v)
	case "anything-but":
		return awsAnythingButMatches(operand, v)
	}
	return false
}

func awsAffixOperand(operand any) (string, bool) {
	if s, ok := operand.(string); ok {
		return s, false
	}
	if m, ok := operand.(map[string]any); ok {
		s, _ := m["equals-ignore-case"].(string)
		return s, true
	}
	return "", false
}

func awsAnythingButMatches(operand, v any) bool {
	switch ex := operand.(type) {
	case []any:
		for _, e := range ex {
			if awsValuesEqual(e, v) {
				return false
			}
		}
		return true
	case map[string]any:
		op, inner := awsSoleEntry(ex)
		if list, ok := inner.([]any); ok {
			for _, e := range list {
				if awsMatcherMatches(op, e, v) {
					return false
				}
			}
			return awsIsScalar(v)
		}
		return awsIsScalar(v) && !awsMatcherMatches(op, inner, v)
	default:
		return !awsValuesEqual(operand, v)
	}
}

func awsIsScalar(v any) bool {
	switch v.(type) {
	case string, float64, bool, nil, json.Number:
		return true
	}
	return false
}

func awsValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case float64:
		bv, ok := awsToFloat(b)
		return ok && av == bv
	case nil:
		return b == nil
	}
	return false
}

func awsNumericMatches(spec, v any) bool {
	terms, ok := spec.([]any)
	if !ok || len(terms)%2 != 0 {
		return false
	}
	val, ok := awsToFloat(v)
	if !ok {
		return false
	}
	for i := 0; i < len(terms); i += 2 {
		op, _ := terms[i].(string)
		bound, ok := awsToFloat(terms[i+1])
		if !ok {
			return false
		}
		var holds bool
		switch op {
		case "=":
			holds = val == bound
		case "<":
			holds = val < bound
		case "<=":
			holds = val <= bound
		case ">":
			holds = val > bound
		case ">=":
			holds = val >= bound
		}
		if !holds {
			return false
		}
	}
	return true
}

func awsToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// '*' matches any run of characters and "\*" a literal star.
var awsWildcard = sim.Wildcard{Escapes: "*"}

// '*' matches any run, '?' one character, and a backslash escapes '*', '?' or
// '\'.
var ec2FilterWildcard = sim.Wildcard{AnyOne: true, Escapes: `*?\`}
