package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// Event Grid delivers to a Storage queue, a Service Bus queue or a Service Bus
// topic through that service's data plane, one event per message, writing as
// the trusted Microsoft service it is: a queue message carries the event's
// JSON as its text, and a Service Bus message carries it as its body.

const (
	eventGridWebHook         = "WebHook"
	eventGridStorageQueue    = "StorageQueue"
	eventGridServiceBusQueue = "ServiceBusQueue"
	eventGridServiceBusTopic = "ServiceBusTopic"
)

// eventGridDestination is the kind of an event subscription's destination and
// the URL its events are posted to; the URL is empty for a destination Event
// Grid cannot reach.
func eventGridDestination(es EventGridEventSubscription) (kind, target string) {
	dest, _ := es.Properties["destination"].(map[string]any)
	kind, _ = dest["endpointType"].(string)
	props, _ := dest["properties"].(map[string]any)
	resourceID, _ := props["resourceId"].(string)
	segments := strings.Split(strings.Trim(resourceID, "/"), "/")
	segment := func(name string) string {
		for i := 0; i+1 < len(segments); i++ {
			if strings.EqualFold(segments[i], name) {
				return segments[i+1]
			}
		}
		return ""
	}
	switch {
	case strings.EqualFold(kind, eventGridStorageQueue):
		account, queue := segment("storageAccounts"), fmt.Sprint(props["queueName"])
		if account == "" || props["queueName"] == nil {
			return eventGridStorageQueue, ""
		}
		target = "http://" + account + ".queue.core.windows.net/" + queue + "/messages"
		if ttl, ok := props["queueMessageTimeToLiveInSeconds"].(float64); ok && ttl != 0 {
			target += "?messagettl=" + strconv.FormatInt(int64(ttl), 10)
		}
		return eventGridStorageQueue, target
	case strings.EqualFold(kind, eventGridServiceBusQueue), strings.EqualFold(kind, eventGridServiceBusTopic):
		entity := segment("queues")
		if strings.EqualFold(kind, eventGridServiceBusTopic) {
			entity = segment("topics")
		}
		namespace := segment("namespaces")
		if namespace == "" || entity == "" {
			return kind, ""
		}
		return kind, "http://" + namespace + ".servicebus.windows.net/" + entity + "/messages"
	case kind == "" || strings.EqualFold(kind, eventGridWebHook):
		return eventGridWebHook, eventGridWebhookEndpoint(es)
	}
	return kind, ""
}

// eventGridDeliverToService posts one event to a Storage queue or a Service
// Bus entity as Event Grid, and classifies the answer as a webhook's is.
func eventGridDeliverToService(ctx context.Context, es EventGridEventSubscription, d eventGridDelivery, classify delivery.Classifier) delivery.Outcome {
	if len(d.Events) != 1 {
		return delivery.Permanent(fmt.Errorf("a %s delivery carries one event, not %d", d.Destination, len(d.Events)))
	}
	header := http.Header{}
	var body []byte
	if strings.EqualFold(d.Destination, eventGridStorageQueue) {
		var text strings.Builder
		if err := xml.EscapeText(&text, d.Events[0]); err != nil {
			return delivery.Permanent(err)
		}
		header.Set("x-ms-version", "2021-12-02")
		header.Set("Content-Type", "application/xml")
		body = []byte("<QueueMessage><MessageText>" + text.String() + "</MessageText></QueueMessage>")
	} else {
		header.Set("Content-Type", "application/json")
		body = d.Events[0]
	}
	status, answer, err := azureCallAs(ctx, "Microsoft.EventGrid", es.ID, http.MethodPost, d.Endpoint, header, body)
	if err != nil {
		return delivery.Retryable(err)
	}
	var outcome delivery.Outcome
	switch classify(status) {
	case delivery.Accept:
		outcome = delivery.Delivered()
	case delivery.Reject:
		outcome = delivery.Permanent(fmt.Errorf("%s answered %d", d.Endpoint, status))
	default:
		outcome = delivery.Retryable(fmt.Errorf("%s answered %d", d.Endpoint, status))
	}
	outcome.Response = answer
	return outcome.WithStatus(status)
}
