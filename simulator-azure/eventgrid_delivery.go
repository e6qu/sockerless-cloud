package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/eventbus"
)

type eventGridDelivery struct {
	SubscriptionID string
	Destination    string
	Endpoint       string
	Events         []json.RawMessage
	PublishedAt    time.Time
	MaxAttempts    int
	TimeToLive     time.Duration
	DeadLetter     bool
}

var eventGridBus *eventbus.Bus[json.RawMessage, EventGridEventSubscription, eventGridDelivery]

// The waits Event Grid documents before each retry; the last repeats until
// the event's time to live runs out.
var eventGridRetrySchedule = delivery.Steps(10*time.Second, 30*time.Second, time.Minute, 5*time.Minute,
	10*time.Minute, 30*time.Minute, time.Hour, 3*time.Hour, 6*time.Hour, 12*time.Hour)

func registerEventGridDelivery(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[eventGridDelivery]](srv.DB(), "eventgrid_deliveries")
	eventGridBus = eventbus.New(srv, eventbus.Config[json.RawMessage, EventGridEventSubscription, eventGridDelivery]{
		Subscriptions: func(scopeID string) []EventGridEventSubscription {
			return eventGridSubscriptionsByTopic.LookupAll(eventGridSubscriptions, scopeID, eventGridSubscriptionTopics)
		},
		Admits: func(es EventGridEventSubscription, event json.RawMessage) bool {
			_, endpoint := eventGridDestination(es)
			return endpoint != "" && es.Properties["provisioningState"] == "Succeeded" && eventGridFilterAdmits(es, event)
		},
		Batch:     eventGridBatchLimits,
		Size:      func(event json.RawMessage) int { return len(event) },
		Transform: eventGridPrepare,
		Delivery: &eventbus.Delivery[eventGridDelivery]{
			Name:  "Event Grid deliveries",
			Store: store,
			Policy: func(d eventGridDelivery) delivery.Policy {
				return delivery.Policy{
					MaxAttempts: d.MaxAttempts,
					MaxAge:      d.TimeToLive,
					Backoff:     eventGridRetrySchedule,
					MinWait:     eventGridStatusWait,
				}
			},
			Attempt:    eventGridAttempt,
			DeadLetter: eventGridDeadLetter,
		},
	})
	eventGridBus.Resume()
}

func eventGridStatusWait(last delivery.Outcome) time.Duration {
	switch last.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return 5 * time.Minute
	case http.StatusRequestTimeout:
		return 2 * time.Minute
	case http.StatusServiceUnavailable:
		return 30 * time.Second
	}
	return 0
}

func eventGridAttempt(ctx context.Context, item *delivery.Item[eventGridDelivery]) delivery.Outcome {
	d := item.Payload
	es, ok := eventGridSubscriptions.Get(d.SubscriptionID)
	if !ok {
		return delivery.Permanent(fmt.Errorf("event subscription %s no longer exists", d.SubscriptionID))
	}
	body, err := json.Marshal(d.Events)
	if err != nil {
		return delivery.Permanent(err)
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("aeg-event-type", "Notification")
	header.Set("aeg-subscription-name", strings.ToUpper(es.Name))
	header.Set("aeg-delivery-count", strconv.Itoa(item.Attempts-1))
	header.Set("aeg-metadata-version", "1")
	var first struct {
		DataVersion string `json:"dataVersion"`
	}
	if len(d.Events) > 0 && json.Unmarshal(d.Events[0], &first) == nil {
		header.Set("aeg-data-version", first.DataVersion)
	}
	classify := func(status int) delivery.Class {
		switch {
		case status >= 200 && status <= 204:
			return delivery.Accept
		case d.DeadLetter && (status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusRequestEntityTooLarge):
			return delivery.Reject
		}
		return delivery.Retry
	}
	if d.Destination != "" && d.Destination != eventGridWebHook {
		return eventGridDeliverToService(ctx, es, d, classify)
	}
	return delivery.Post(ctx, delivery.Request{URL: d.Endpoint, Header: header, Body: body, Timeout: 30 * time.Second}, classify)
}

// eventGridSubmit delivers to every provisioned subscription of scopeID whose
// filter admits the events.
func eventGridSubmit(scopeID string, events []json.RawMessage) {
	eventGridBus.Publish(scopeID, events...)
}

// retryPolicy: maxDeliveryAttempts (default 30) and eventTimeToLiveInMinutes
// (default 1440).
func eventGridPrepare(es EventGridEventSubscription, events []json.RawMessage) (eventGridDelivery, bool) {
	kind, endpoint := eventGridDestination(es)
	d := eventGridDelivery{
		SubscriptionID: es.ID,
		Destination:    kind,
		Endpoint:       endpoint,
		Events:         events,
		PublishedAt:    time.Now().UTC(),
		MaxAttempts:    30,
		TimeToLive:     24 * time.Hour,
		DeadLetter:     eventGridDeadLetterTarget(es) != nil,
	}
	if retry, ok := es.Properties["retryPolicy"].(map[string]any); ok {
		if n, ok := retry["maxDeliveryAttempts"].(float64); ok && n > 0 {
			d.MaxAttempts = int(n)
		}
		if n, ok := retry["eventTimeToLiveInMinutes"].(float64); ok && n > 0 {
			d.TimeToLive = time.Duration(n) * time.Minute
		}
	}
	return d, true
}

// A webhook's maxEventsPerBatch (default 1) and preferredBatchSizeInKilobytes
// (default 64); every other destination takes one event per delivery.
func eventGridBatchLimits(es EventGridEventSubscription) eventbus.Limits {
	limits := eventbus.Limits{MaxEvents: 1, MaxBytes: 64 * 1024}
	if kind, _ := eventGridDestination(es); kind != eventGridWebHook {
		return limits
	}
	if dest, ok := es.Properties["destination"].(map[string]any); ok {
		if props, ok := dest["properties"].(map[string]any); ok {
			if n, ok := props["maxEventsPerBatch"].(float64); ok && n > 0 {
				limits.MaxEvents = int(n)
			}
			if n, ok := props["preferredBatchSizeInKilobytes"].(float64); ok && n > 0 {
				limits.MaxBytes = int(n) * 1024
			}
		}
	}
	return limits
}

// includedEventTypes, subjectBeginsWith and subjectEndsWith, case-insensitive
// unless isSubjectCaseSensitive, and every advanced filter.
func eventGridFilterAdmits(es EventGridEventSubscription, raw json.RawMessage) bool {
	filter, ok := es.Properties["filter"].(map[string]any)
	if !ok {
		return true
	}
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil {
		return false
	}
	// An Event Grid schema event names its type eventType; a CloudEvents one,
	// type.
	eventType, _ := event["eventType"].(string)
	if eventType == "" {
		eventType, _ = event["type"].(string)
	}
	if types, ok := filter["includedEventTypes"].([]any); ok && len(types) > 0 {
		matched := false
		for _, t := range types {
			if s, _ := t.(string); strings.EqualFold(s, eventType) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	onArrays, _ := filter["enableAdvancedFilteringOnArrays"].(bool)
	if advanced, ok := filter["advancedFilters"].([]any); ok {
		for _, f := range advanced {
			spec, _ := f.(map[string]any)
			if !eventGridAdvancedFilterMatches(spec, event, onArrays) {
				return false
			}
		}
	}
	caseSensitive, _ := filter["isSubjectCaseSensitive"].(bool)
	subject, _ := event["subject"].(string)
	fold := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	if prefix, _ := filter["subjectBeginsWith"].(string); prefix != "" && !strings.HasPrefix(fold(subject), fold(prefix)) {
		return false
	}
	if suffix, _ := filter["subjectEndsWith"].(string); suffix != "" && !strings.HasSuffix(fold(subject), fold(suffix)) {
		return false
	}
	return true
}

type eventGridBlobTarget struct {
	Account, Container string
}

// A StorageBlob deadLetterDestination, or the one inside
// deadLetterWithResourceIdentity.
func eventGridDeadLetterTarget(es EventGridEventSubscription) *eventGridBlobTarget {
	dest, ok := es.Properties["deadLetterDestination"].(map[string]any)
	if !ok {
		if identity, ok := es.Properties["deadLetterWithResourceIdentity"].(map[string]any); ok {
			dest, _ = identity["deadLetterDestination"].(map[string]any)
		}
	}
	if dest == nil || !strings.EqualFold(fmt.Sprint(dest["endpointType"]), "StorageBlob") {
		return nil
	}
	props, _ := dest["properties"].(map[string]any)
	resourceID, _ := props["resourceId"].(string)
	container, _ := props["blobContainerName"].(string)
	const marker = "/storageAccounts/"
	i := sim.CaseInsensitiveLastIndex(resourceID, marker)
	if i < 0 || container == "" {
		return nil
	}
	return &eventGridBlobTarget{Account: strings.Trim(resourceID[i+len(marker):], "/"), Container: container}
}

var eventGridDeadLetterReasons = map[delivery.Reason]string{
	delivery.AttemptsExhausted: "MaxDeliveryAttemptsExceeded",
	delivery.AgeExceeded:       "TimeToLiveExceeded",
	delivery.Rejected:          "NonRetriableFailure",
}

// Named as the dead-lettered event's lastDeliveryOutcome names it.
func eventGridDeliveryOutcome(status int) string {
	switch status {
	case 0:
		return "TimedOut"
	case http.StatusBadRequest:
		return "BadRequest"
	case http.StatusUnauthorized:
		return "Unauthorized"
	case http.StatusForbidden:
		return "Forbidden"
	case http.StatusNotFound:
		return "NotFound"
	case http.StatusServiceUnavailable:
		return "Busy"
	}
	return "GenericError"
}

// eventGridDeadLetter writes to
// <TOPIC>/<SUBSCRIPTION>/<yyyy>/<MM>/<dd>/<HH>/<guid>.json, each event carrying
// why and how it was dead-lettered.
func eventGridDeadLetter(item delivery.Item[eventGridDelivery], reason delivery.Reason) {
	d := item.Payload
	es, ok := eventGridSubscriptions.Get(d.SubscriptionID)
	if !ok {
		return
	}
	target := eventGridDeadLetterTarget(es)
	if target == nil {
		return
	}
	var out []map[string]any
	for _, raw := range d.Events {
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil {
			continue
		}
		event["deadLetterReason"] = eventGridDeadLetterReasons[reason]
		event["deliveryAttempts"] = item.Attempts
		event["lastDeliveryOutcome"] = eventGridDeliveryOutcome(item.LastStatus)
		event["publishTime"] = d.PublishedAt.Format(time.RFC3339Nano)
		event["lastDeliveryAttemptTime"] = item.LastAttemptAt.Format(time.RFC3339Nano)
		if item.LastStatus != 0 {
			event["lastHttpStatusCode"] = item.LastStatus
		}
		out = append(out, event)
	}
	body, err := json.Marshal(out)
	if err != nil {
		return
	}
	scope, _ := es.Properties["topic"].(string)
	now := time.Now().UTC()
	name := fmt.Sprintf("%s/%s/%s/%s.json", strings.ToUpper(scope[strings.LastIndex(scope, "/")+1:]),
		strings.ToUpper(es.Name), now.Format("2006/01/02/15"), sim.NewUUID())
	header := http.Header{
		"X-Ms-Version":   {"2021-12-02"},
		"X-Ms-Blob-Type": {"BlockBlob"},
		"Content-Type":   {"application/json"},
	}
	status, answer, err := azureCallAs(context.Background(), "Microsoft.EventGrid", es.ID, http.MethodPut,
		"/"+target.Account+"/"+target.Container+"/"+name, header, body)
	if err == nil && status >= 300 {
		err = fmt.Errorf("the Put Blob call answered %d: %s", status, answer)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sim-azure-eventgrid] dead-letter to %s/%s failed: %v\n", target.Account, target.Container, err)
	}
}
