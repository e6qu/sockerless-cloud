package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const eventarcPubSubEventType = "google.cloud.pubsub.topic.v1.messagePublished"

// eventarcIsPubSubTrigger reports whether the trigger routes Pub/Sub
// messagePublished events, the event source whose messages the simulator
// carries.
func eventarcIsPubSubTrigger(t EventarcTrigger) bool {
	for _, f := range t.EventFilters {
		if f.Attribute == "type" && f.Value == eventarcPubSubEventType {
			return true
		}
	}
	return false
}

// eventarcDestinationURL resolves the trigger's destination: a Cloud Run
// service the simulator runs (its uri plus the destination path) or an
// httpEndpoint uri.
func eventarcDestinationURL(t EventarcTrigger, project string) (string, bool) {
	if run, ok := t.Destination["cloudRun"].(map[string]any); ok {
		service, _ := run["service"].(string)
		region, _ := run["region"].(string)
		path, _ := run["path"].(string)
		svc, ok := crv2Services.Get(fmt.Sprintf("projects/%s/locations/%s/services/%s", project, region, service))
		if !ok || svc.URI == "" {
			return "", false
		}
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return strings.TrimRight(svc.URI, "/") + path, true
	}
	if endpoint, ok := t.Destination["httpEndpoint"].(map[string]any); ok {
		uri, _ := endpoint["uri"].(string)
		return uri, uri != ""
	}
	return "", false
}

// eventarcProvisionTransport gives a Pub/Sub trigger its transport the way
// Eventarc does: the named topic, or one Eventarc creates, and a push
// subscription on it that delivers to the destination. It records both in
// transport.pubsub.
func eventarcProvisionTransport(t *EventarcTrigger, project, location, triggerID string) {
	if !eventarcIsPubSubTrigger(*t) {
		return
	}
	endpoint, ok := eventarcDestinationURL(*t, project)
	if !ok {
		return
	}
	pubsub, _ := t.Transport["pubsub"].(map[string]any)
	if pubsub == nil {
		pubsub = map[string]any{}
	}
	topic, _ := pubsub["topic"].(string)
	suffix := strings.ReplaceAll(t.Uid, "-", "")
	suffix = suffix[:min(len(suffix), 3)]
	if topic == "" {
		topic = fmt.Sprintf("projects/%s/topics/eventarc-%s-%s-%s", project, location, triggerID, suffix)
		psTopics.Put(topic, PSTopic{Name: topic})
	}
	if _, ok := psTopics.Get(topic); !ok {
		return
	}
	subscription, _ := pubsub["subscription"].(string)
	if subscription == "" {
		subscription = fmt.Sprintf("projects/%s/subscriptions/eventarc-%s-%s-sub-%s", project, location, triggerID, suffix)
	}
	push := &PSPushConfig{PushEndpoint: endpoint}
	if t.ServiceAccount != "" {
		push.OidcToken = &PSOidcToken{ServiceAccountEmail: t.ServiceAccount, Audience: endpoint}
	}
	psSubscriptions.Upsert(subscription, func(s *PSSubscription) {
		s.Name = subscription
		s.Topic = topic
		s.PushConfig = push
		if s.AckDeadlineSeconds == 0 {
			s.AckDeadlineSeconds = 10
		}
		if s.MessageRetentionDuration == "" {
			s.MessageRetentionDuration = "86400s"
		}
	})
	if _, ok := psQueues.Get(subscription); !ok {
		psQueues.Put(subscription, psQueue{Subscription: subscription})
	}
	pubsub["topic"], pubsub["subscription"] = topic, subscription
	if t.Transport == nil {
		t.Transport = map[string]any{}
	}
	t.Transport["pubsub"] = pubsub
}

// eventarcReleaseTransport deletes the push subscription a deleted trigger
// delivered through.
func eventarcReleaseTransport(t EventarcTrigger) {
	pubsub, _ := t.Transport["pubsub"].(map[string]any)
	if subscription, _ := pubsub["subscription"].(string); subscription != "" {
		psSubscriptions.Delete(subscription)
		psQueues.Delete(subscription)
	}
}

// eventarcDeliversThrough reports whether a trigger delivers through the
// subscription.
func eventarcDeliversThrough(subscription string) bool {
	for _, t := range eventarcTriggers.List() {
		pubsub, _ := t.Transport["pubsub"].(map[string]any)
		if s, _ := pubsub["subscription"].(string); s == subscription {
			return true
		}
	}
	return false
}

// eventarcCloudEvent renders a Pub/Sub message as the binary-mode CloudEvent
// Eventarc delivers: ce-* attributes in headers and the push envelope as
// data.
func eventarcCloudEvent(sub PSSubscription, message PSMessage) (http.Header, []byte, error) {
	body, err := json.Marshal(psPushEnvelope(sub, message, 0))
	if err != nil {
		return nil, nil, err
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("ce-specversion", "1.0")
	header.Set("ce-id", message.MessageId)
	header.Set("ce-type", eventarcPubSubEventType)
	header.Set("ce-source", "//pubsub.googleapis.com/"+sub.Topic)
	header.Set("ce-time", message.PublishTime)
	return header, body, nil
}
