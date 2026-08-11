package main

import (
	"strings"
	"time"
)

// parseFilter splits a GCP logging filter string into individual clauses
// joined by AND. Supports: field="value", field>"value", field>="value".
func parseFilter(filter string) []filterClause {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return nil
	}

	// Split on " AND " (case-sensitive, as GCP requires uppercase AND)
	parts := strings.Split(filter, " AND ")
	var clauses []filterClause
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		c := parseClause(part)
		clauses = append(clauses, c)
	}
	return clauses
}

type filterOp int

const (
	opEq  filterOp = iota // =
	opGt                  // >
	opGe                  // >=
	opHas                 // : (Cloud Logging substring/has operator)
)

type filterClause struct {
	field string
	op    filterOp
	value string
}

// parseClause parses a single clause like `field="value"`, `field >= "value"`,
// `field > "value"`, or `field:"value"`. Handles optional whitespace around
// the operator.
//
// The `:` operator is Cloud Logging's "has" / substring-match form
// (https://cloud.google.com/logging/docs/view/logging-query-language#operators).
// Real Cloud Logging supports it natively; the sockerless cloudrun backend
// uses it for the `logName:"run.googleapis.com"` clause that filters out
// Cloud Audit Logs. The sim must accept it too — without this
// branch the clause falls through to the wildcard `*` and silently matches
// nothing, dropping every container's stdout from `docker logs`.
func parseClause(s string) filterClause {
	// Try >= first (before > to avoid partial match)
	if idx := strings.Index(s, ">="); idx > 0 {
		field := strings.TrimSpace(s[:idx])
		value := unquote(strings.TrimSpace(s[idx+2:]))
		return filterClause{field: field, op: opGe, value: value}
	}
	if idx := strings.Index(s, ">"); idx > 0 {
		field := strings.TrimSpace(s[:idx])
		value := unquote(strings.TrimSpace(s[idx+1:]))
		return filterClause{field: field, op: opGt, value: value}
	}
	if idx := strings.Index(s, "="); idx > 0 {
		field := strings.TrimSpace(s[:idx])
		value := unquote(strings.TrimSpace(s[idx+1:]))
		return filterClause{field: field, op: opEq, value: value}
	}
	if idx := strings.Index(s, ":"); idx > 0 {
		field := strings.TrimSpace(s[:idx])
		value := unquote(strings.TrimSpace(s[idx+1:]))
		return filterClause{field: field, op: opHas, value: value}
	}
	// Fallback: bare string without operator — do a substring search
	// across textPayload, logName, and severity (like GCP's simple filter).
	return filterClause{field: "*", op: opEq, value: s}
}

// unquote removes surrounding double quotes from a string.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// resolveField extracts the value of a dot-notation field path from a LogEntry.
func resolveField(entry LogEntry, field string) (string, bool) {
	switch field {
	case "resource.type":
		if entry.Resource != nil {
			return entry.Resource.Type, true
		}
		return "", false
	case "logName":
		return entry.LogName, true
	case "severity":
		return entry.Severity, true
	case "textPayload":
		return entry.TextPayload, true
	case "timestamp":
		return entry.Timestamp, true
	default:
		// Handle resource.labels.X
		if strings.HasPrefix(field, "resource.labels.") {
			labelKey := field[len("resource.labels."):]
			if entry.Resource != nil && entry.Resource.Labels != nil {
				v, ok := entry.Resource.Labels[labelKey]
				return v, ok
			}
			return "", false
		}
		// Handle labels.X
		if strings.HasPrefix(field, "labels.") {
			labelKey := field[len("labels."):]
			if entry.Labels != nil {
				v, ok := entry.Labels[labelKey]
				return v, ok
			}
			return "", false
		}
		return "", false
	}
}

// parseTimestamp parses a timestamp string in RFC3339 or RFC3339Nano format.
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

// matchesFilter checks whether a LogEntry matches a structured filter string.
// Supports: field="value" AND field>"value" AND field>="value"
// with dot-notation paths (resource.type, resource.labels.X, timestamp, etc.)
func matchesFilter(entry LogEntry, filter string) bool {
	clauses := parseFilter(filter)
	if len(clauses) == 0 {
		return true // empty filter matches all
	}
	for _, c := range clauses {
		// Wildcard field: bare filter string — substring match across all text fields
		if c.field == "*" {
			found := strings.Contains(entry.TextPayload, c.value) ||
				strings.Contains(entry.LogName, c.value) ||
				strings.Contains(entry.Severity, c.value)
			if !found {
				return false
			}
			continue
		}
		val, ok := resolveField(entry, c.field)
		if !ok {
			return false
		}
		switch c.op {
		case opEq:
			if val != c.value {
				return false
			}
		case opGt:
			if c.field == "severity" {
				left, lok := severityRank(val)
				right, rok := severityRank(c.value)
				if !lok || !rok || left <= right {
					return false
				}
				continue
			}
			if val <= c.value {
				return false
			}
		case opGe:
			if c.field == "severity" {
				left, lok := severityRank(val)
				right, rok := severityRank(c.value)
				if !lok || !rok || left < right {
					return false
				}
				continue
			}
			if val < c.value {
				return false
			}
		case opHas:
			// Cloud Logging's `:` operator is substring-match: the
			// clause matches when the field's value CONTAINS the
			// query string. Real Cloud Logging additionally supports
			// glob/colon-prefix tokenisation; the sim implements the
			// substring subset since that's what the cloudrun + gcf
			// backends emit (`logName:"run.googleapis.com"`).
			if !strings.Contains(val, c.value) {
				return false
			}
		}
	}
	return true
}
