package main

import (
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

type sfnHistoryEvent map[string]any

var (
	sfnHistories sim.Store[[]sfnHistoryEvent]
	sfnHistoryMu sync.Mutex
)

func sfnAppendHistory(executionARN, eventType string, details map[string]any) {
	if executionARN == "" {
		return
	}
	sfnHistoryMu.Lock()
	defer sfnHistoryMu.Unlock()
	events, _ := sfnHistories.Get(executionARN)
	event := sfnHistoryEvent{
		"id":        int64(len(events) + 1),
		"timestamp": sfnEpochNow(),
		"type":      eventType,
	}
	if len(events) > 0 {
		event["previousEventId"] = int64(len(events))
	}
	if details != nil {
		event[sfnHistoryDetailsKey(eventType)] = details
	}
	sfnHistories.Put(executionARN, append(events, event))
}

func sfnHistoryDetailsKey(eventType string) string {
	switch {
	case strings.HasSuffix(eventType, "StateEntered"):
		return "stateEnteredEventDetails"
	case strings.HasSuffix(eventType, "StateExited"):
		return "stateExitedEventDetails"
	default:
		return sfnLowerInitial(eventType) + "EventDetails"
	}
}

func sfnLowerInitial(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToLower(value[:1]) + value[1:]
}

func sfnStateHistoryDetails(stateName string, value any, field string) map[string]any {
	encoded, err := sfnEncodeJSON(value)
	if err != nil {
		encoded = "null"
	}
	details := map[string]any{
		"name": stateName,
	}
	if field != "" {
		details[field] = encoded
		details[field+"Details"] = map[string]any{"truncated": false}
	}
	return details
}

func sfnHistoryWithoutExecutionData(events []sfnHistoryEvent) []sfnHistoryEvent {
	cloned := make([]sfnHistoryEvent, len(events))
	for index, event := range events {
		copyEvent := make(sfnHistoryEvent, len(event))
		for key, value := range event {
			details, ok := value.(map[string]any)
			if !ok || !strings.HasSuffix(key, "EventDetails") {
				copyEvent[key] = value
				continue
			}
			copyDetails := make(map[string]any, len(details))
			for detailKey, detailValue := range details {
				if detailKey == "input" || detailKey == "output" {
					continue
				}
				if detailKey == "inputDetails" || detailKey == "outputDetails" {
					copyDetails[detailKey] = map[string]any{"truncated": false}
					continue
				}
				copyDetails[detailKey] = detailValue
			}
			copyEvent[key] = copyDetails
		}
		cloned[index] = copyEvent
	}
	return cloned
}
