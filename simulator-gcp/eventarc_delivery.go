package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/eventbus"
)

const (
	eventarcPubSubEventType   = "google.cloud.pubsub.topic.v1.messagePublished"
	eventarcAuditLogEventType = "google.cloud.audit.log.v1.written"
	eventarcPathPatternOp     = "match-path-pattern"
)

// eventarcStorageEvents maps each Cloud Storage event type Eventarc routes
// directly to the notification event type Cloud Storage publishes for it.
var eventarcStorageEvents = map[string]string{
	"google.cloud.storage.object.v1.finalized":       gcsEventFinalize,
	"google.cloud.storage.object.v1.deleted":         gcsEventDelete,
	"google.cloud.storage.object.v1.archived":        "OBJECT_ARCHIVE",
	"google.cloud.storage.object.v1.metadataUpdated": gcsEventMetadataUpdate,
}

// eventarcFilterValue is the value a trigger's event filter gives attribute.
func eventarcFilterValue(t EventarcTrigger, attribute string) string {
	for _, f := range t.EventFilters {
		if f.Attribute == attribute {
			return f.Value
		}
	}
	return ""
}

// eventarcIsPubSubTrigger reports whether the trigger routes Pub/Sub
// messagePublished events, the event source whose messages the simulator
// carries.
func eventarcIsPubSubTrigger(t EventarcTrigger) bool {
	return eventarcFilterValue(t, "type") == eventarcPubSubEventType
}

// eventarcIsAuditLogTrigger reports whether the trigger routes Cloud Audit
// Logs entries.
func eventarcIsAuditLogTrigger(t EventarcTrigger) bool {
	return eventarcFilterValue(t, "type") == eventarcAuditLogEventType
}

// eventarcStorageEvent is the Cloud Storage notification event type a trigger
// routes, if it routes one.
func eventarcStorageEvent(t EventarcTrigger) (string, bool) {
	event, ok := eventarcStorageEvents[eventarcFilterValue(t, "type")]
	return event, ok
}

// eventarcCloudRunService is the Cloud Run service a trigger's destination
// names. The region defaults to the trigger's own location.
func eventarcCloudRunService(t EventarcTrigger, project, location string) (name, path string, ok bool) {
	run, ok := t.Destination["cloudRun"].(map[string]any)
	if !ok {
		return "", "", false
	}
	service, _ := run["service"].(string)
	region, _ := run["region"].(string)
	if region == "" {
		region = location
	}
	path, _ = run["path"].(string)
	return fmt.Sprintf("projects/%s/locations/%s/services/%s", project, region, service), path, true
}

// eventarcValidateTrigger makes the checks Eventarc makes before it accepts a
// trigger: the Cloud Run service it delivers to exists, and a Cloud Storage
// trigger names a bucket that exists.
func eventarcValidateTrigger(t EventarcTrigger, project, location string) error {
	if service, _, ok := eventarcCloudRunService(t, project, location); ok {
		if _, exists := crv2Services.Get(service); !exists {
			return fmt.Errorf("the request was invalid: destination Cloud Run service %s does not exist", service)
		}
	}
	if eventarcIsAuditLogTrigger(t) {
		if err := eventarcValidateAuditLogFilters(t); err != nil {
			return err
		}
	}
	if _, ok := eventarcStorageEvent(t); ok {
		bucket := eventarcFilterValue(t, "bucket")
		if bucket == "" {
			return fmt.Errorf("the request was invalid: a Cloud Storage trigger needs a bucket event filter")
		}
		if _, exists := gcsBuckets.Get(bucket); !exists {
			return fmt.Errorf("the request was invalid: bucket %q was not found", bucket)
		}
	}
	return nil
}

// eventarcValidateAuditLogFilters checks a Cloud Audit Logs trigger's event
// filters: it names the serviceName and methodName of the entries it routes,
// may narrow them by resourceName, and only resourceName takes a path pattern.
func eventarcValidateAuditLogFilters(t EventarcTrigger) error {
	for _, f := range t.EventFilters {
		switch f.Attribute {
		case "type", "serviceName", "methodName", "resourceName":
		default:
			return fmt.Errorf("the request was invalid: event filter attribute %q is not one the event type %s defines", f.Attribute, eventarcAuditLogEventType)
		}
		if f.Operator != "" && (f.Operator != eventarcPathPatternOp || f.Attribute != "resourceName") {
			return fmt.Errorf("the request was invalid: event filter attribute %q does not support the operator %q", f.Attribute, f.Operator)
		}
	}
	for _, required := range []string{"serviceName", "methodName"} {
		if eventarcFilterValue(t, required) == "" {
			return fmt.Errorf("the request was invalid: a trigger for %s needs a %s event filter", eventarcAuditLogEventType, required)
		}
	}
	return nil
}

// eventarcDestinationURL resolves the trigger's destination: a Cloud Run
// service the simulator runs (its uri plus the destination path) or an
// httpEndpoint uri.
func eventarcDestinationURL(t EventarcTrigger, project, location string) (string, bool) {
	if service, path, ok := eventarcCloudRunService(t, project, location); ok {
		svc, ok := crv2Services.Get(service)
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

// eventarcProvisionTransport gives a trigger its transport the way Eventarc
// does: a Pub/Sub trigger delivers from the named topic or one Eventarc
// creates, a Cloud Storage trigger from a topic Eventarc creates and the
// bucket's notification configuration that publishes to it, and a Cloud Audit
// Logs trigger from a topic Eventarc creates and publishes the matching
// entries to. Each way a push subscription on the topic delivers to the
// destination. It records topic and subscription in transport.pubsub.
func eventarcProvisionTransport(ctx context.Context, t *EventarcTrigger, project, location, triggerID string) error {
	storageEvent, isStorage := eventarcStorageEvent(*t)
	isAuditLog := eventarcIsAuditLogTrigger(*t)
	if !eventarcIsPubSubTrigger(*t) && !isStorage && !isAuditLog {
		return nil
	}
	endpoint, ok := eventarcDestinationURL(*t, project, location)
	if !ok {
		return nil
	}
	agent, err := eventarcServiceAgent(project)
	if err != nil {
		return err
	}
	pubsub, _ := t.Transport["pubsub"].(map[string]any)
	if pubsub == nil {
		pubsub = map[string]any{}
	}
	topic, _ := pubsub["topic"].(string)
	suffix := eventarcTransportSuffix(*t)
	if topic == "" || isStorage || isAuditLog {
		topic = eventarcCreatedTopicName(*t)
		if err := gcpCallAs(ctx, agent, http.MethodPut, "/v1/"+topic, map[string]any{}, nil); err != nil && !gcpCallStatus(err, "ALREADY_EXISTS") {
			return err
		}
	} else if err := gcpCallAs(ctx, agent, http.MethodGet, "/v1/"+topic, nil, nil); err != nil {
		return err
	}
	if isStorage {
		if err := eventarcReleaseNotifications(ctx, agent, *t); err != nil {
			return err
		}
		bucket := eventarcFilterValue(*t, "bucket")
		if err := gcpCallAs(ctx, agent, http.MethodPost, "/storage/v1/b/"+url.PathEscape(bucket)+"/notificationConfigs", map[string]any{
			"topic": "//pubsub.googleapis.com/" + topic, "payload_format": "JSON_API_V1", "event_types": []string{storageEvent},
		}, nil); err != nil {
			return err
		}
	}
	subscription, _ := pubsub["subscription"].(string)
	if subscription == "" {
		subscription = fmt.Sprintf("projects/%s/subscriptions/eventarc-%s-%s-sub-%s", project, location, triggerID, suffix)
	}
	push := map[string]any{"pushEndpoint": endpoint}
	if t.ServiceAccount != "" {
		push["oidcToken"] = map[string]any{"serviceAccountEmail": t.ServiceAccount, "audience": endpoint}
	}
	err = gcpCallAs(ctx, agent, http.MethodPut, "/v1/"+subscription, map[string]any{
		"topic": topic, "pushConfig": push, "ackDeadlineSeconds": 10, "messageRetentionDuration": "86400s",
	}, nil)
	if gcpCallStatus(err, "ALREADY_EXISTS") {
		err = gcpCallAs(ctx, agent, http.MethodPost, "/v1/"+subscription+":modifyPushConfig", map[string]any{"pushConfig": push}, nil)
	}
	if err != nil {
		return err
	}
	pubsub["topic"], pubsub["subscription"] = topic, subscription
	if t.Transport == nil {
		t.Transport = map[string]any{}
	}
	t.Transport["pubsub"] = pubsub
	return nil
}

// eventarcServiceAgent is the service account Eventarc acts as in a project.
func eventarcServiceAgent(project string) (string, error) {
	number, ok := crmProjectNumber(project)
	if !ok {
		return "", &gcpCallError{Code: http.StatusForbidden, Status: "PERMISSION_DENIED", Message: "Project " + project + " is not active"}
	}
	return gcpServiceAgent(number, "eventarc"), nil
}

// gcpCallStatus reports whether err is a refusal with the given status.
func gcpCallStatus(err error, status string) bool {
	callErr, ok := err.(*gcpCallError)
	return ok && callErr.Status == status
}

// eventarcReleaseNotifications deletes the Cloud Storage notification
// configurations that publish a trigger's events to its transport topic.
func eventarcReleaseNotifications(ctx context.Context, agent string, t EventarcTrigger) error {
	pubsub, _ := t.Transport["pubsub"].(map[string]any)
	topic, _ := pubsub["topic"].(string)
	bucket := eventarcFilterValue(t, "bucket")
	if topic == "" || bucket == "" {
		return nil
	}
	var listed struct {
		Items []GCSNotification `json:"items"`
	}
	path := "/storage/v1/b/" + url.PathEscape(bucket) + "/notificationConfigs"
	if err := gcpCallAs(ctx, agent, http.MethodGet, path, nil, &listed); err != nil {
		if gcpCallStatus(err, "NOT_FOUND") {
			return nil
		}
		return err
	}
	for _, notification := range listed.Items {
		if notification.Topic == "//pubsub.googleapis.com/"+topic {
			if err := gcpCallAs(ctx, agent, http.MethodDelete, path+"/"+notification.ID, nil, nil); err != nil && !gcpCallStatus(err, "NOT_FOUND") {
				return err
			}
		}
	}
	return nil
}

func eventarcTransportSuffix(t EventarcTrigger) string {
	suffix := strings.ReplaceAll(t.Uid, "-", "")
	return suffix[:min(len(suffix), 3)]
}

// eventarcCreatedTopicName is the transport topic Eventarc creates for a
// trigger that names none, projects/{p}/topics/eventarc-{location}-{trigger}-{suffix}.
func eventarcCreatedTopicName(t EventarcTrigger) string {
	parts := strings.Split(t.Name, "/")
	if len(parts) != 6 {
		return ""
	}
	return fmt.Sprintf("projects/%s/topics/eventarc-%s-%s-%s", parts[1], parts[3], parts[5], eventarcTransportSuffix(t))
}

// eventarcReleaseTransport deletes what a deleted trigger delivered through:
// its push subscription, the transport topic when Eventarc created it rather
// than the caller naming it, and, for a Cloud Storage trigger, the bucket's
// notification configuration.
func eventarcReleaseTransport(ctx context.Context, t EventarcTrigger) error {
	project := strings.Split(t.Name, "/")[1]
	agent, err := eventarcServiceAgent(project)
	if err != nil {
		return err
	}
	if err := eventarcReleaseNotifications(ctx, agent, t); err != nil {
		return err
	}
	pubsub, _ := t.Transport["pubsub"].(map[string]any)
	if subscription, _ := pubsub["subscription"].(string); subscription != "" {
		if err := gcpCallAs(ctx, agent, http.MethodDelete, "/v1/"+subscription, nil, nil); err != nil && !gcpCallStatus(err, "NOT_FOUND") {
			return err
		}
	}
	if topic, _ := pubsub["topic"].(string); topic != "" && topic == eventarcCreatedTopicName(t) {
		if err := gcpCallAs(ctx, agent, http.MethodDelete, "/v1/"+topic, nil, nil); err != nil && !gcpCallStatus(err, "NOT_FOUND") {
			return err
		}
	}
	return nil
}

// eventarcDeliveringThrough is the trigger that delivers through the
// subscription, if one does.
func eventarcDeliveringThrough(subscription string) (EventarcTrigger, bool) {
	for _, t := range eventarcTriggers.List() {
		pubsub, _ := t.Transport["pubsub"].(map[string]any)
		if s, _ := pubsub["subscription"].(string); s == subscription {
			return t, true
		}
	}
	return EventarcTrigger{}, false
}

// eventarcAuditEvent is a Cloud Audit Logs entry written in a project and
// location.
type eventarcAuditEvent struct {
	Entry             LogEntry
	Project, Location string
}

// eventarcAuditPublish is an audit entry bound for a trigger's transport
// topic.
type eventarcAuditPublish struct {
	Trigger, Project, Topic, Data string
}

// eventarcAuditBus hands each audit entry to the transport topic of every
// trigger that routes it; the topic's push subscription owns the delivery to
// the destination and its retries.
var eventarcAuditBus = eventbus.New(nil, eventbus.Config[eventarcAuditEvent, EventarcTrigger, eventarcAuditPublish]{
	Subscriptions: eventarcAuditLogTriggers,
	Admits: func(t EventarcTrigger, e eventarcAuditEvent) bool {
		return eventarcAuditLogMatches(t, e.Entry.ProtoPayload, e.Project, e.Location)
	},
	Transform: func(t EventarcTrigger, events []eventarcAuditEvent) (eventarcAuditPublish, bool) {
		pubsub, _ := t.Transport["pubsub"].(map[string]any)
		topic, _ := pubsub["topic"].(string)
		body, err := json.Marshal(events[0].Entry)
		if err != nil {
			log.Printf("eventarc: audit log entry %s: %v", events[0].Entry.InsertID, err)
			return eventarcAuditPublish{}, false
		}
		return eventarcAuditPublish{Trigger: t.Name, Project: events[0].Project, Topic: topic,
			Data: base64.StdEncoding.EncodeToString(body)}, topic != ""
	},
	Sink: func(_ EventarcTrigger, p eventarcAuditPublish) { eventarcPublishAuditEntry(p) },
})

func eventarcAuditLogTriggers(project string) []EventarcTrigger {
	var triggers []EventarcTrigger
	for _, row := range eventarcTriggers.ListPrefix(project + "/") {
		if eventarcIsAuditLogTrigger(row.Item) {
			triggers = append(triggers, row.Item)
		}
	}
	return triggers
}

// eventarcRouteAuditLog publishes a Cloud Audit Logs entry to the transport
// topic of every trigger of the project that routes it.
func eventarcRouteAuditLog(entry LogEntry, project, location string) {
	if eventarcTriggers == nil {
		return
	}
	eventarcAuditBus.Publish(project, eventarcAuditEvent{Entry: entry, Project: project, Location: location})
}

// eventarcPublishAuditEntry publishes the entry to the trigger's transport
// topic as the project's Eventarc service agent.
func eventarcPublishAuditEntry(p eventarcAuditPublish) {
	agent, err := eventarcServiceAgent(p.Project)
	if err == nil {
		err = gcpCallAs(context.Background(), agent, http.MethodPost, "/v1/"+p.Topic+":publish",
			map[string]any{"messages": []map[string]any{{"data": p.Data}}}, nil)
	}
	if err != nil {
		log.Printf("eventarc: trigger %s: %v", p.Trigger, err)
	}
}

// eventarcAuditLogMatches reports whether a trigger routes an audit entry: a
// trigger receives the entries of its own project and location, a global
// trigger those of every location, and each event filter matches the
// AuditLog field it names.
func eventarcAuditLogMatches(t EventarcTrigger, payload map[string]any, project, location string) bool {
	parts := strings.Split(t.Name, "/")
	if len(parts) != 6 || parts[1] != project {
		return false
	}
	if parts[3] != "global" && !strings.EqualFold(parts[3], location) {
		return false
	}
	for _, f := range t.EventFilters {
		if f.Attribute == "type" {
			continue
		}
		value, _ := payload[f.Attribute].(string)
		if f.Operator == eventarcPathPatternOp {
			if !eventarcPathPatternMatch(f.Value, value) {
				return false
			}
		} else if value != f.Value {
			return false
		}
	}
	return true
}

// eventarcPathPatternMatch matches a resource name against an Eventarc path
// pattern: "*" matches any run of characters within a segment and a "**"
// segment matches any number of segments.
func eventarcPathPatternMatch(pattern, name string) bool {
	return eventarcSegmentsMatch(strings.Split(strings.TrimPrefix(pattern, "/"), "/"), strings.Split(strings.TrimPrefix(name, "/"), "/"))
}

func eventarcSegmentsMatch(pattern, segments []string) bool {
	if len(pattern) == 0 {
		return len(segments) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(segments); i++ {
			if eventarcSegmentsMatch(pattern[1:], segments[i:]) {
				return true
			}
		}
		return false
	}
	if len(segments) == 0 || !(sim.Wildcard{}).Match(pattern[0], segments[0]) {
		return false
	}
	return eventarcSegmentsMatch(pattern[1:], segments[1:])
}

// eventarcCloudEvent renders a message as the binary-mode CloudEvent Eventarc
// delivers for the trigger: ce-* attributes in headers, and as data the push
// envelope of a Pub/Sub message, the object resource of a Cloud Storage
// notification, or the LogEntryData of a Cloud Audit Logs entry.
func eventarcCloudEvent(t EventarcTrigger, sub PSSubscription, message PSMessage) (http.Header, []byte, error) {
	header := http.Header{}
	header.Set("ce-specversion", "1.0")
	header.Set("ce-id", message.MessageId)
	if eventarcIsAuditLogTrigger(t) {
		body, err := base64.StdEncoding.DecodeString(message.Data)
		if err != nil {
			return nil, nil, err
		}
		var entry LogEntry
		if err := json.Unmarshal(body, &entry); err != nil {
			return nil, nil, err
		}
		project, logID, _ := strings.Cut(strings.TrimPrefix(entry.LogName, "projects/"), "/logs/cloudaudit.googleapis.com%2F")
		serviceName, _ := entry.ProtoPayload["serviceName"].(string)
		methodName, _ := entry.ProtoPayload["methodName"].(string)
		resourceName, _ := entry.ProtoPayload["resourceName"].(string)
		header.Set("Content-Type", "application/json; charset=utf-8")
		header.Set("ce-type", eventarcAuditLogEventType)
		header.Set("ce-source", "//cloudaudit.googleapis.com/projects/"+project+"/logs/"+logID)
		header.Set("ce-subject", serviceName+"/"+resourceName)
		header.Set("ce-time", entry.Timestamp)
		header.Set("ce-servicename", serviceName)
		header.Set("ce-methodname", methodName)
		header.Set("ce-resourcename", resourceName)
		header.Set("ce-recordedtime", entry.Timestamp)
		return header, body, nil
	}
	if _, ok := eventarcStorageEvent(t); ok {
		body, err := base64.StdEncoding.DecodeString(message.Data)
		if err != nil {
			return nil, nil, err
		}
		bucket := message.Attributes["bucketId"]
		header.Set("Content-Type", "application/json")
		header.Set("ce-type", eventarcFilterValue(t, "type"))
		header.Set("ce-source", "//storage.googleapis.com/projects/_/buckets/"+bucket)
		header.Set("ce-subject", "objects/"+message.Attributes["objectId"])
		header.Set("ce-time", message.Attributes["eventTime"])
		header.Set("ce-bucket", bucket)
		return header, body, nil
	}
	body, err := json.Marshal(psPushEnvelope(sub, message, 0))
	if err != nil {
		return nil, nil, err
	}
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("ce-type", eventarcPubSubEventType)
	header.Set("ce-source", "//pubsub.googleapis.com/"+sub.Topic)
	header.Set("ce-time", message.PublishTime)
	return header, body, nil
}
