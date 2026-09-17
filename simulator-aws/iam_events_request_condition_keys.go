package main

import (
	"encoding/json"
	"net/http"
)

func init() {
	registerIAMRequestConditionPopulator("events", iamPopulateEventsRequestConditionKeys)
}

type eventsConditionRequest struct {
	EventPattern string `json:"EventPattern"`
	Entries      []struct {
		Source     string `json:"Source"`
		DetailType string `json:"DetailType"`
	} `json:"Entries"`
	Targets []struct {
		Arn string `json:"Arn"`
	} `json:"Targets"`
	EventBuses []struct {
		EventBusArn string `json:"EventBusArn"`
	} `json:"EventBuses"`
}

// iamPopulateEventsRequestConditionKeys adds the Amazon EventBridge keys that
// describe the events a request sends or matches, the targets it attaches and
// the buses an endpoint spans.
func iamPopulateEventsRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	var request eventsConditionRequest
	if !iamDecodeJSONRequest(body, &request) {
		return
	}
	switch operation {
	case "PutEvents":
		var sources, detailTypes []string
		for _, entry := range request.Entries {
			sources = append(sources, entry.Source)
			detailTypes = append(detailTypes, entry.DetailType)
		}
		iamSetConditionValues(ctx, "events:source", sources...)
		iamSetConditionValues(ctx, "events:detail-type", detailTypes...)
	case "PutRule":
		iamPopulateEventPatternConditionKeys(request.EventPattern, ctx)
	case "PutTargets":
		arns := make([]string, 0, len(request.Targets))
		for _, target := range request.Targets {
			arns = append(arns, target.Arn)
		}
		iamSetConditionValues(ctx, "events:TargetArn", arns...)
	case "CreateEndpoint", "UpdateEndpoint":
		arns := make([]string, 0, len(request.EventBuses))
		for _, bus := range request.EventBuses {
			arns = append(arns, bus.EventBusArn)
		}
		iamSetConditionValues(ctx, "events:EventBusArn", arns...)
	}
}

// iamPopulateEventPatternConditionKeys adds the literal values a rule's event
// pattern matches on the fields EventBridge declares keys for. A content
// filter such as {"prefix": "aws."} is not a literal and settles nothing.
func iamPopulateEventPatternConditionKeys(pattern string, ctx map[string][]string) {
	if pattern == "" {
		return
	}
	var document map[string]any
	if json.Unmarshal([]byte(pattern), &document) != nil {
		return
	}
	iamSetConditionValues(ctx, "events:source", eventPatternLiterals(document, "source")...)
	iamSetConditionValues(ctx, "events:detail-type", eventPatternLiterals(document, "detail-type")...)
	detail, _ := document["detail"].(map[string]any)
	iamSetConditionValues(ctx, "events:detail.eventTypeCode", eventPatternLiterals(detail, "eventTypeCode")...)
	iamSetConditionValues(ctx, "events:detail.service", eventPatternLiterals(detail, "service")...)
	identity, _ := detail["userIdentity"].(map[string]any)
	iamSetConditionValues(ctx, "events:detail.userIdentity.principalId", eventPatternLiterals(identity, "principalId")...)
}

func eventPatternLiterals(document map[string]any, field string) []string {
	var literals []string
	switch value := document[field].(type) {
	case string:
		literals = append(literals, value)
	case []any:
		for _, item := range value {
			if literal, ok := item.(string); ok {
				literals = append(literals, literal)
			}
		}
	}
	return literals
}
