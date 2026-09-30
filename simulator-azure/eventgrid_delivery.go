package main

import (
	"bytes"
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
)

// eventGridDelivery is one batch of events Event Grid owes one event
// subscription's webhook.
type eventGridDelivery struct {
	SubscriptionID string
	Endpoint       string
	Events         []json.RawMessage
	PublishedAt    time.Time
	MaxAttempts    int
	TimeToLive     time.Duration
	DeadLetter     bool
}

var eventGridDeliveries *delivery.Dispatcher[eventGridDelivery]

// eventGridRetrySchedule is the wait Event Grid documents before each retry,
// the last step repeating until the event's time to live runs out.
var eventGridRetrySchedule = delivery.Steps(10*time.Second, 30*time.Second, time.Minute, 5*time.Minute,
	10*time.Minute, 30*time.Minute, time.Hour, 3*time.Hour, 6*time.Hour, 12*time.Hour)

func registerEventGridDelivery(srv *sim.Server) {
	store := sim.MakeStore[delivery.Item[eventGridDelivery]](srv.DB(), "eventgrid_deliveries")
	eventGridDeliveries = delivery.New(srv, "Event Grid deliveries", store, delivery.Handler[eventGridDelivery]{
		Policy: func(d eventGridDelivery) delivery.Policy {
			return delivery.Policy{
				MaxAttempts: d.MaxAttempts,
				MaxAge:      d.TimeToLive,
				Backoff:     eventGridRetrySchedule,
				MinWait:     eventGridStatusWait,
			}
		},
		Attempt: eventGridAttempt,
		Finish:  eventGridFinish,
	})
	eventGridDeliveries.Resume()
}

// eventGridStatusWait is the longer wait Event Grid holds after some answers.
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
	return delivery.Post(ctx, delivery.Request{URL: d.Endpoint, Header: header, Body: body, Timeout: 30 * time.Second}, classify)
}

// eventGridSubmit queues the events of one publish for every subscription of
// scopeID whose filter admits them, in batches of the subscription's
// maxEventsPerBatch (default 1) and preferredBatchSizeInKilobytes (default 64).
func eventGridSubmit(scopeID string, events []json.RawMessage) {
	now := time.Now().UTC()
	for _, es := range eventGridSubscriptionsByTopic.LookupAll(eventGridSubscriptions, scopeID, eventGridSubscriptionTopics) {
		endpoint := eventGridWebhookEndpoint(es)
		if endpoint == "" {
			continue
		}
		var admitted []json.RawMessage
		for _, event := range events {
			if eventGridFilterAdmits(es, event) {
				admitted = append(admitted, event)
			}
		}
		for _, batch := range eventGridBatches(es, admitted) {
			d := eventGridDelivery{
				SubscriptionID: es.ID,
				Endpoint:       endpoint,
				Events:         batch,
				PublishedAt:    now,
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
			eventGridDeliveries.Submit(sim.NewUUID(), d)
		}
	}
}

func eventGridBatches(es EventGridEventSubscription, events []json.RawMessage) [][]json.RawMessage {
	maxEvents, maxBytes := 1, 64*1024
	if dest, ok := es.Properties["destination"].(map[string]any); ok {
		if props, ok := dest["properties"].(map[string]any); ok {
			if n, ok := props["maxEventsPerBatch"].(float64); ok && n > 0 {
				maxEvents = int(n)
			}
			if n, ok := props["preferredBatchSizeInKilobytes"].(float64); ok && n > 0 {
				maxBytes = int(n) * 1024
			}
		}
	}
	var batches [][]json.RawMessage
	var current []json.RawMessage
	size := 0
	for _, event := range events {
		if len(current) > 0 && (len(current) == maxEvents || size+len(event) > maxBytes) {
			batches = append(batches, current)
			current, size = nil, 0
		}
		current = append(current, event)
		size += len(event)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

// eventGridFilterAdmits applies an event subscription's filter:
// includedEventTypes, subjectBeginsWith and subjectEndsWith, compared without
// case unless isSubjectCaseSensitive, and every advanced filter.
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

// eventGridDeadLetterTarget reads a StorageBlob deadLetterDestination, or the
// one inside deadLetterWithResourceIdentity.
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

// eventGridDeliveryOutcome names the last attempt's result the way the
// dead-lettered event's lastDeliveryOutcome does.
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

// eventGridFinish writes an undelivered batch to the subscription's
// dead-letter container as
// <TOPIC>/<SUBSCRIPTION>/<yyyy>/<MM>/<dd>/<HH>/<guid>.json, each event carrying
// why and how it was dead-lettered.
func eventGridFinish(item delivery.Item[eventGridDelivery], reason delivery.Reason) {
	if reason == delivery.Succeeded {
		return
	}
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
	if err := blobWriteServiceObject(target.Account, target.Container, name, "application/json", body); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-azure-eventgrid] dead-letter to %s/%s failed: %v\n", target.Account, target.Container, err)
	}
}

// blobWriteServiceObject writes a block blob on a service's behalf, as Put
// Blob would for a caller.
func blobWriteServiceObject(account, container, name, contentType string, body []byte) error {
	if _, ok := blobContainersData.Get(blobContainerKey(account, container)); !ok {
		return fmt.Errorf("container %s/%s does not exist", account, container)
	}
	b := BlobObject{
		Account:            account,
		Container:          container,
		Name:               name,
		BlobType:           "BlockBlob",
		CreationTime:       blobNowHTTP(),
		ContentType:        contentType,
		AccessTier:         "Hot",
		AccessTierInferred: true,
	}
	blobTouch(&b)
	digests, err := blobSetContentsFrom(&b, bytes.NewReader(body))
	if err != nil {
		return err
	}
	b.ContentMD5 = digests.MD5Base64()
	putBlobObject(b)
	return nil
}
