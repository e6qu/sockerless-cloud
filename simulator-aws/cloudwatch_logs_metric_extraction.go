package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// A metric extraction policy, account-level only, turns embedded metric
// format extraction off for the log groups it selects: their EMF events are
// still ingested, no metric is created from them, and each one counts in
// EMFDisabledErrors, as the PutAccountPolicy API reference and the
// CloudWatch Logs metrics page describe.

const (
	cwMetricExtractionScopedMax = 5
	cwEMFStatusDisabled         = "Disabled"
	cwEMFStatusEnabled          = "Enabled"
)

func cwParseMetricExtractionDocument(doc string) (string, error) {
	var parsed struct {
		EmbeddedMetricFormat *struct {
			Status string `json:"Status"`
		} `json:"EmbeddedMetricFormat"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return "", fmt.Errorf("the metric extraction policy document is not valid JSON: %v", err)
	}
	if parsed.EmbeddedMetricFormat == nil {
		return "", fmt.Errorf("a metric extraction policy document must include EmbeddedMetricFormat")
	}
	switch status := parsed.EmbeddedMetricFormat.Status; status {
	case cwEMFStatusDisabled, cwEMFStatusEnabled:
		return status, nil
	default:
		return "", fmt.Errorf("invalid EmbeddedMetricFormat Status %q: valid values are Disabled and Enabled", status)
	}
}

// cwMetricExtractionConflict returns why a metric extraction policy with
// criteria cannot sit beside others, and whether the reason is a limit: one
// policy without criteria or up to five scoped ones, one NOT IN at most, and
// no two whose selections overlap.
func cwMetricExtractionConflict(criteria string, others []CWAccountPolicy) (string, bool) {
	if criteria == "" {
		if len(others) > 0 {
			return fmt.Sprintf("An account can have either one metric extraction policy for all log groups or up to %d scoped ones; policy %s exists", cwMetricExtractionScopedMax, others[0].PolicyName), true
		}
		return "", false
	}
	mine, _ := cwParsePolicySelection(cwPolicyMetricExtraction, criteria)
	for _, p := range others {
		if p.SelectionCriteria == "" {
			return fmt.Sprintf("Metric extraction policy %s applies to all log groups; no scoped policy can be added", p.PolicyName), true
		}
	}
	if len(others) >= cwMetricExtractionScopedMax {
		return fmt.Sprintf("An account can have at most %d scoped metric extraction policies", cwMetricExtractionScopedMax), true
	}
	for _, p := range others {
		theirs, err := cwParsePolicySelection(cwPolicyMetricExtraction, p.SelectionCriteria)
		if err != nil {
			continue
		}
		if mine.cmp == "NOT IN" && theirs.cmp == "NOT IN" {
			return fmt.Sprintf("Only one metric extraction policy can use NOT IN; policy %s does", p.PolicyName), false
		}
		if cwExtractionSelectionsOverlap(mine, theirs) {
			return fmt.Sprintf("The selection criteria overlap with metric extraction policy %s", p.PolicyName), false
		}
	}
	return "", false
}

// cwSelectionValueWithin reports whether every log group v selects is one u
// selects.
func cwSelectionValueWithin(vField, v, uField, u string) bool {
	if uField == "LogGroupNamePrefix" {
		return strings.HasPrefix(v, u)
	}
	return vField == "LogGroupName" && v == u
}

func cwExtractionSelectionsOverlap(a, b *cwSelection) bool {
	if a.cmp == "NOT IN" {
		a, b = b, a
	}
	if b.cmp == "NOT IN" {
		for _, v := range a.values {
			within := false
			for _, u := range b.values {
				if cwSelectionValueWithin(a.field, v, b.field, u) {
					within = true
					break
				}
			}
			if !within {
				return true
			}
		}
		return false
	}
	for _, v := range a.values {
		for _, u := range b.values {
			if cwSelectionValueWithin(a.field, v, b.field, u) || cwSelectionValueWithin(b.field, u, a.field, v) {
				return true
			}
		}
	}
	return false
}

// cwEMFDisabled reports whether a metric extraction policy turns EMF
// extraction off for group.
func cwEMFDisabled(group string) bool {
	for _, p := range cwAccountPoliciesOf(cwPolicyMetricExtraction) {
		status, err := cwParseMetricExtractionDocument(p.PolicyDocument)
		if err != nil || status != cwEMFStatusDisabled {
			continue
		}
		if cwPolicySelects(p.PolicyType, p.SelectionCriteria, group) {
			return true
		}
	}
	return false
}

func cwIsEMF(message string) bool {
	var root struct {
		AWS *struct {
			CloudWatchMetrics json.RawMessage `json:"CloudWatchMetrics"`
		} `json:"_aws"`
	}
	return json.Unmarshal([]byte(message), &root) == nil && root.AWS != nil && len(root.AWS.CloudWatchMetrics) > 0
}

// cwExtractEMF creates the metrics of the EMF events group ingests, from the
// events as written, unless a metric extraction policy turns extraction off.
func cwExtractEMF(group string, events []CWLogEvent) {
	disabled := cwEMFDisabled(group)
	ignored := 0
	for _, e := range events {
		message := e.unmaskedMessage()
		if disabled {
			if cwIsEMF(message) {
				ignored++
			}
			continue
		}
		for _, datum := range extractEMFMetrics(message, e.Timestamp) {
			cwStoreDatum(datum)
		}
	}
	if ignored > 0 {
		cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "EMFDisabledErrors",
			Dimensions: []CWDimension{{Name: "LogGroupName", Value: group}},
			Value:      float64(ignored), Timestamp: float64(time.Now().Unix()), Unit: "None"})
	}
}
