package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// parseLogFilter parses a Cloud Logging query
// (https://cloud.google.com/logging/docs/view/logging-query-language). It
// shares the AIP-160 expression grammar and differs in its leaves: a bare
// value is a global restriction that matches any field containing it,
// `=~`/`!~` match an RE2 expression, severity compares by level, timestamp
// compares as a time, and a comparison on a field the entry lacks is false.
func parseLogFilter(filter string) (listq.Node, error) {
	return gcpParseFilter(filter, logFilterLeaf)
}

func logFilterLeaf(field, op, value string) (listq.Node, error) {
	if op == "" {
		return logGlobalRestriction(field), nil
	}
	var test listq.Test
	switch op {
	case "=~", "!~":
		re, err := regexp.Compile(value)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression %q: %v", value, err)
		}
		want := op == "=~"
		test = func(v string, present bool) bool { return present && re.MatchString(v) == want }
	case ":":
		test = func(v string, present bool) bool { return present && (value == "*" || strings.Contains(v, value)) }
	case "=", "!=", "<", "<=", ">", ">=":
		cmp := logCompare(field)
		test = func(v string, present bool) bool {
			if field == "severity" && (!present || v == "") {
				v, present = "DEFAULT", true
			}
			if !present {
				return false
			}
			c, ok := cmp(v, value)
			if !ok {
				return op == "!="
			}
			return logOrdered(op, c)
		}
	default:
		return nil, fmt.Errorf("unsupported operator %q", op)
	}
	return logFieldCmp{path: strings.Split(field, "."), test: test}, nil
}

// logFieldCmp compares the field a path names. A path that crosses a repeated
// field compares each of its elements, and holds when one of them does, as in
// protoPayload.serviceData.policyDelta.bindingDeltas.action="ADD".
type logFieldCmp struct {
	path []string
	test listq.Test
}

func (n logFieldCmp) Eval(d listq.Doc) bool {
	values := logFieldValues(d, n.path)
	if len(values) == 0 {
		return n.test("", false)
	}
	for _, v := range values {
		if n.test(listq.ScalarString(v), true) {
			return true
		}
	}
	return false
}

func logFieldValues(cur any, path []string) []any {
	if list, ok := cur.([]any); ok && len(path) > 0 {
		var out []any
		for _, element := range list {
			out = append(out, logFieldValues(element, path)...)
		}
		return out
	}
	if len(path) == 0 {
		return []any{cur}
	}
	m, ok := cur.(map[string]any)
	if !ok {
		return nil
	}
	next, ok := m[path[0]]
	if !ok {
		return nil
	}
	return logFieldValues(next, path[1:])
}

func logOrdered(op string, c int) bool {
	switch op {
	case "=":
		return c == 0
	case "!=":
		return c != 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	}
	return c >= 0
}

// logCompare returns how the query language orders two values of field:
// severity by level, timestamp by instant, everything else as text or,
// when both sides are numbers, numerically.
func logCompare(field string) func(a, b string) (int, bool) {
	switch field {
	case "severity":
		return func(a, b string) (int, bool) {
			x, xok := severityRank(a)
			y, yok := severityRank(b)
			return x - y, xok && yok
		}
	case "timestamp", "receiveTimestamp":
		return func(a, b string) (int, bool) {
			x, xerr := parseTimestamp(a)
			y, yerr := parseTimestamp(b)
			if xerr != nil || yerr != nil {
				return strings.Compare(a, b), true
			}
			return x.Compare(y), true
		}
	}
	return func(a, b string) (int, bool) { return listq.CompareOrdered(a, b), true }
}

type logGlobalRestriction string

func (g logGlobalRestriction) Eval(d listq.Doc) bool {
	return logAnyValueContains(d, string(g))
}

func logAnyValueContains(v any, needle string) bool {
	switch t := v.(type) {
	case map[string]any:
		for _, e := range t {
			if logAnyValueContains(e, needle) {
				return true
			}
		}
		return false
	case []any:
		for _, e := range t {
			if logAnyValueContains(e, needle) {
				return true
			}
		}
		return false
	}
	return strings.Contains(listq.ScalarString(v), needle)
}

func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	return t, err
}

func severityRank(s string) (int, bool) {
	switch strings.ToUpper(s) {
	case "DEFAULT":
		return 0, true
	case "DEBUG":
		return 100, true
	case "INFO":
		return 200, true
	case "NOTICE":
		return 300, true
	case "WARNING":
		return 400, true
	case "ERROR":
		return 500, true
	case "CRITICAL":
		return 600, true
	case "ALERT":
		return 700, true
	case "EMERGENCY":
		return 800, true
	default:
		return 0, false
	}
}
